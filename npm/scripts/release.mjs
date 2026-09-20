import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync,
  rmSync, utimesSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";
import { buildBinaries, targets } from "./build.mjs";
import { verifyVersion } from "./version.mjs";

export const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");
const npm = process.env.NPM || "npm";
const maxBuffer = 128 * 1024 * 1024;
export function validateTag(tag) {
  assert.match(tag, /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/, "expected vX.Y.Z");
  return tag.slice(1);
}
const readTar = (archive, entry) => execFileSync("tar", ["-xOf", archive, entry], { maxBuffer });

// This command only builds and packages. It has no publication credentials or calls.
export function packRelease(source, output, tag, sha, runBuild = buildBinaries) {
  const version = validateTag(tag);
  assert.equal(verifyVersion(source), version, "requested/source version mismatch");
  assert.match(sha, /^[a-f0-9]{40}$/, "expected exact source commit");
  runBuild(source);
  rmSync(output, { recursive: true, force: true });
  mkdirSync(output, { recursive: true });
  const staging = mkdtempSync(path.join(tmpdir(), "comwit-pack-"));
  const manifest = { version, tag, sourceSha: sha, binaries: {}, files: {} };
  try {
    for (const target of targets) {
      const binary = path.join(source, "npm", "dist", target.output);
      manifest.binaries[target.output] = sha256(readFileSync(binary));
      if (target.goos === "windows") continue;
      copyFileSync(binary, path.join(staging, "comwit"));
      utimesSync(path.join(staging, "comwit"), 0, 0);
      const archive = `comwit_${target.goos}_${target.goarch}.tar.gz`;
      // Normalize owner/time and gzip headers so retries produce the same assets.
      const owner = process.platform === "darwin"
        ? ["--uid", "0", "--gid", "0", "--uname", "root", "--gname", "root"]
        : ["--owner=root:0", "--group=root:0"];
      const tar = execFileSync("tar", ["--format=ustar", ...owner, "-C", staging,
        "-cf", "-", "comwit"], { maxBuffer, env: { ...process.env, COPYFILE_DISABLE: "1" } });
      writeFileSync(path.join(output, archive), gzipSync(tar));
      manifest.files[archive] = sha256(readFileSync(path.join(output, archive)));
    }
    writeFileSync(path.join(output, "checksums.txt"), Object.entries(manifest.files)
      .map(([name, hash]) => `${hash}  ${name}\n`).join(""));
    // Old tags may still define prepack; it must never rebuild these binaries.
    execFileSync(npm, ["pack", "--ignore-scripts", "--pack-destination", output], {
      cwd: source, stdio: "inherit"
    });
    for (const name of ["checksums.txt", `comwit-cli-${version}.tgz`]) {
      manifest.files[name] = sha256(readFileSync(path.join(output, name)));
    }
    writeFileSync(path.join(output, "release.json"), JSON.stringify(manifest, null, 2) + "\n");
    verifyRelease(output, tag, sha);
    return manifest;
  } finally {
    rmSync(staging, { recursive: true, force: true });
  }
}

export function verifyRelease(output, tag, sha) {
  const version = validateTag(tag);
  const manifest = JSON.parse(readFileSync(path.join(output, "release.json"), "utf8"));
  assert.equal(manifest.tag, tag);
  assert.equal(manifest.version, version);
  assert.equal(manifest.sourceSha, sha);
  const archives = targets.filter(t => t.goos !== "windows")
    .map(t => `comwit_${t.goos}_${t.goarch}.tar.gz`);
  const npmArchive = `comwit-cli-${version}.tgz`;
  assert.deepEqual(Object.keys(manifest.files).sort(), [...archives, "checksums.txt", npmArchive].sort());
  assert.deepEqual(Object.keys(manifest.binaries).sort(), targets.map(t => t.output).sort());
  for (const [name, hash] of Object.entries(manifest.files)) {
    assert.equal(sha256(readFileSync(path.join(output, name))), hash, `asset changed: ${name}`);
  }
  const checksums = archives.map(name => `${manifest.files[name]}  ${name}\n`).join("");
  assert.equal(readFileSync(path.join(output, "checksums.txt"), "utf8"), checksums);
  const packed = path.join(output, npmArchive);
  const pkg = JSON.parse(readTar(packed, "package/package.json"));
  assert.equal(pkg.name, "comwit-cli");
  assert.equal(pkg.version, version);
  assert.equal(pkg.repository.url, "git+https://github.com/burrr-ai/comwit-cli.git");
  assert.ok(readTar(packed, "package/npm/bin/comwit.js").length, "missing npm launcher");
  for (const target of targets) {
    const hash = sha256(readTar(packed, `package/npm/dist/${target.output}`));
    assert.equal(hash, manifest.binaries[target.output], `npm binary changed: ${target.output}`);
    if (target.goos !== "windows") {
      const archive = `comwit_${target.goos}_${target.goarch}.tar.gz`;
      assert.equal(sha256(readTar(path.join(output, archive), "comwit")), hash,
        `GitHub/npm binary mismatch: ${target.output}`);
    }
    console.log(`${target.output} ${hash} verified`);
  }
  return manifest;
}

// Only 404 means absent. Authentication failures/outages must never look like a
// missing release, tag, or npm version and trigger a write.
export async function request(url, options = {}, allowMissing = false) {
  const response = await fetch(url, options);
  if (allowMissing && response.status === 404) return null;
  if (!response.ok) throw new Error(`HTTP ${response.status} for ${new URL(url).pathname}`);
  return response;
}

// A later dispatch (including npm-only repair) reuses the original qualified
// bytes even if the runner's compiler or npm version has changed since release.
export async function restoreRelease(output, tag, sha, apiRequest = request) {
  const version = validateTag(tag);
  assert.equal(process.env.GITHUB_REPOSITORY, "burrr-ai/comwit-cli");
  assert.ok(process.env.GH_TOKEN, "read token required");
  const headers = { Authorization: `Bearer ${process.env.GH_TOKEN}`,
    Accept: "application/vnd.github+json" };
  const response = await apiRequest(`https://api.github.com/repos/burrr-ai/comwit-cli/releases/tags/${tag}`,
    { headers }, true);
  if (!response) return false;
  const release = await response.json();
  const names = [...targets.filter(t => t.goos !== "windows")
    .map(t => `comwit_${t.goos}_${t.goarch}.tar.gz`),
  "checksums.txt", `comwit-cli-${version}.tgz`, "release.json"];
  // Legacy/incomplete releases cannot supply a full bundle. Rebuild once and
  // let publication verify every existing asset without overwriting anything.
  if (!names.every(name => release.assets.some(asset => asset.name === name))) return false;
  rmSync(output, { recursive: true, force: true });
  mkdirSync(output, { recursive: true });
  for (const name of names) {
    const asset = release.assets.find(item => item.name === name);
    const downloaded = await apiRequest(asset.url, {
      headers: { ...headers, Accept: "application/octet-stream" }
    });
    writeFileSync(path.join(output, name), Buffer.from(await downloaded.arrayBuffer()));
  }
  verifyRelease(output, tag, sha);
  return true;
}

export async function publishRelease(output, tag, sha, publishNpm, apiRequest = request, run = execFileSync) {
  const manifest = verifyRelease(output, tag, sha);
  const repo = process.env.GITHUB_REPOSITORY;
  assert.equal(repo, "burrr-ai/comwit-cli", "unexpected release repository");
  assert.ok(process.env.GH_TOKEN, "release-automation App token required");
  const headers = { Authorization: `Bearer ${process.env.GH_TOKEN}`,
    Accept: "application/vnd.github+json", "Content-Type": "application/json",
    "X-GitHub-Api-Version": "2022-11-28" };
  const api = async (endpoint, body, missing = false) => {
    const response = await apiRequest(`https://api.github.com/repos/${repo}/${endpoint}`, {
      headers, ...(body ? { method: "POST", body: JSON.stringify(body) } : {})
    }, missing);
    return response ? response.json() : null;
  };
  // Recheck protection and exact ancestry immediately before any write.
  assert.equal((await api("branches/main")).protected, true, "main must be protected");
  const compare = await api(`compare/${sha}...main`);
  assert.ok(["ahead", "identical"].includes(compare.status), "source is not on main");
  let ref = await api(`git/ref/tags/${tag}`, null, true);
  if (!ref) ref = await api("git/refs", { ref: `refs/tags/${tag}`, sha });
  let object = ref.object;
  while (object.type === "tag") object = (await api(`git/tags/${object.sha}`)).object;
  assert.equal(object.type, "commit");
  assert.equal(object.sha, sha, "existing tag points to a different source commit");
  let release = await api(`releases/tags/${tag}`, null, true);
  // The by-tag endpoint promises published releases only. Find a partial draft
  // through the authenticated list before attempting to create another release.
  if (!release) {
    for (let page = 1; ; page++) {
      const releases = await api(`releases?per_page=100&page=${page}`);
      release = releases.find(item => item.tag_name === tag);
      if (release || releases.length < 100) break;
    }
  }
  if (!release) release = await api("releases", { tag_name: tag, target_commitish: sha,
    name: tag, draft: true, make_latest: "legacy", body: `comwit CLI ${tag}\n\nSource: ${sha}` });
  // release.json makes all six binaries recoverable through the attached npm
  // tarball and binds every archived byte to its source commit.
  const files = { ...manifest.files, "release.json": sha256(readFileSync(path.join(output, "release.json"))) };
  for (const [name, hash] of Object.entries(files)) {
    const existing = release.assets.find(asset => asset.name === name);
    if (existing) {
      const response = await apiRequest(existing.url, { headers: { ...headers, Accept: "application/octet-stream" } });
      assert.equal(sha256(Buffer.from(await response.arrayBuffer())), hash, `existing asset differs: ${name}`);
      continue;
    }
    const upload = new URL(release.upload_url.split("{")[0]);
    upload.searchParams.set("name", name);
    const response = await apiRequest(upload, { method: "POST",
      headers: { ...headers, "Content-Type": "application/octet-stream" },
      body: readFileSync(path.join(output, name)) });
    const asset = await response.json();
    const downloaded = await apiRequest(asset.url, { headers: { ...headers, Accept: "application/octet-stream" } });
    assert.equal(sha256(Buffer.from(await downloaded.arrayBuffer())), hash, `uploaded asset differs: ${name}`);
  }
  if (release.draft) {
    await apiRequest(`https://api.github.com/repos/${repo}/releases/${release.id}`, {
      method: "PATCH", headers, body: JSON.stringify({ draft: false, make_latest: "legacy" })
    });
  }
  if (!publishNpm) {
    console.log("npm publication disabled; dispatch publish_npm=true after trusted-publisher setup");
    return;
  }
  const archive = path.join(output, `comwit-cli-${manifest.version}.tgz`);
  const integrity = `sha512-${createHash("sha512").update(readFileSync(archive)).digest("base64")}`;
  const registryURL = `https://registry.npmjs.org/comwit-cli/${manifest.version}`;
  let published = await apiRequest(registryURL, {}, true);
  if (!published) {
    run(npm, ["publish", archive, "--ignore-scripts", "--access", "public",
      "--registry", "https://registry.npmjs.org"], { stdio: "inherit" });
    published = await apiRequest(registryURL);
  }
  const metadata = await published.json();
  assert.equal(metadata.version, manifest.version);
  assert.equal(metadata.dist.integrity, integrity, "existing npm version differs from the qualified tarball");
  console.log(`verified GitHub and npm release ${tag}`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [command, sourceOrOutput = ".", tag, sha] = process.argv.slice(2);
  const root = path.resolve(sourceOrOutput);
  if (command === "pack") packRelease(root, path.join(root, "dist"), tag, sha);
  else if (command === "restore") {
    const restored = await restoreRelease(root, tag, sha);
    assert.ok(process.env.GITHUB_OUTPUT, "restore is a workflow operation");
    writeFileSync(process.env.GITHUB_OUTPUT, `restored=${restored}\n`, { flag: "a" });
  }
  else if (command === "verify") verifyRelease(root, tag, sha);
  else if (command === "publish") await publishRelease(root, tag, sha, process.env.PUBLISH_NPM === "true");
  else throw new Error("usage: release.mjs pack SOURCE vX.Y.Z SHA | verify/publish DIST vX.Y.Z SHA");
}

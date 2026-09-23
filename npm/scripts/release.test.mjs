import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { after, test } from "node:test";
import { buildBinaries, targets } from "./build.mjs";
import { packRelease, publishRelease, request, restoreRelease, validateTag, verifyRelease } from "./release.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const fixture = mkdtempSync(path.join(tmpdir(), "comwit-release-test-"));
after(() => rmSync(fixture, { recursive: true, force: true }));
const source = path.join(fixture, "source");
const output = path.join(fixture, "dist");
const sha = "a".repeat(40);
const tag = "v1.2.3";
mkdirSync(path.join(source, "cmd", "comwit"), { recursive: true });
mkdirSync(path.join(source, "npm", "bin"), { recursive: true });
writeFileSync(path.join(source, "cmd", "comwit", "main.go"), 'const version = "1.2.3"\n');
const pkg = JSON.parse(readFileSync(path.join(root, "package.json")));
pkg.version = "1.2.3";
pkg.scripts = { prepack: "node -e 'process.exit(99)'", prepublishOnly: "node -e 'process.exit(99)'" };
writeFileSync(path.join(source, "package.json"), JSON.stringify(pkg));
copyFileSync(path.join(root, "npm", "bin", "comwit.js"), path.join(source, "npm", "bin", "comwit.js"));
const builds = [];
const fakeGo = (_command, args, options) => {
  if (args[0] === "env") return path.join(fixture, "cache");
  assert.equal(args[0], "build");
  assert.equal(options.env.CGO_ENABLED, "0");
  assert.ok(args.includes("-buildvcs=false"));
  builds.push({ target: `${options.env.GOOS}/${options.env.GOARCH}`, cache: options.env.GOCACHE });
  writeFileSync(args[args.indexOf("-o") + 1], `binary for ${options.env.GOOS}/${options.env.GOARCH}\n`);
};
const manifest = packRelease(source, output, tag, sha, src => buildBinaries(src, fakeGo));
const integrity = `sha512-${createHash("sha512").update(readFileSync(path.join(output, "comwit-cli-1.2.3.tgz"))).digest("base64")}`;

test("one build per target and one cache; hooks disabled; both channels carry identical bytes", () => {
  assert.equal(builds.length, 6);
  assert.equal(new Set(builds.map(b => b.target)).size, 6);
  assert.equal(new Set(builds.map(b => b.cache)).size, 1);
  assert.deepEqual(verifyRelease(output, tag, sha), manifest);
  assert.equal(Object.keys(manifest.files).length, 6);
});

test("repacking existing binaries produces identical archives", () => {
  const repacked = packRelease(source, path.join(fixture, "repacked"), tag, sha, () => {});
  assert.deepEqual(repacked, manifest);
  assert.equal(builds.length, 6);
});

test("version and source mismatches fail before building or publishing", () => {
  for (const invalid of ["1.2.3", "v01.2.3", "v1.2.3;echo bad", "v1.2.3/evil"]) {
    assert.throws(() => validateTag(invalid));
  }
  assert.throws(() => packRelease(source, output, "v1.2.4", sha, () => assert.fail("built")));
  assert.throws(() => verifyRelease(output, tag, "b".repeat(40)));
});

test("changed archives are rejected", () => {
  const archive = path.join(output, "comwit_linux_amd64.tar.gz");
  const original = readFileSync(archive);
  try {
    writeFileSync(archive, "corrupt");
    assert.throws(() => verifyRelease(output, tag, sha), /asset changed/);
  } finally { writeFileSync(archive, original); }
});

function publication(options = {}) {
  const calls = [];
  const published = [];
  const assets = new Map();
  const allFiles = [...Object.keys(manifest.files), "release.json"];
  for (const name of options.existingAssets || []) assets.set(name, readFileSync(path.join(output, name)));
  let hasNpm = options.hasNpm ?? true;
  const json = data => new Response(JSON.stringify(data));
  const api = async (urlValue, init = {}, missing = false) => {
    const url = new URL(urlValue);
    const endpoint = url.pathname;
    const method = init.method || "GET";
    calls.push({ endpoint, method });
    if (url.hostname === "registry.npmjs.org") {
      if (!hasNpm) { assert.equal(missing, true); return null; }
      return json({ version: "1.2.3", dist: { integrity: options.integrity || integrity } });
    }
    if (endpoint.endsWith("/branches/main")) return json({ protected: options.protected ?? true });
    if (endpoint.includes("/compare/")) return json({ status: options.ancestry || "ahead" });
    if (endpoint.includes("/git/ref/tags/")) {
      if (options.missingTag) { assert.equal(missing, true); return null; }
      return json({ object: { type: options.annotated ? "tag" : "commit", sha: options.tagSha || sha } });
    }
    if (endpoint.includes("/git/tags/")) return json({ object: { type: "commit", sha } });
    if (endpoint.endsWith("/git/refs")) {
      assert.deepEqual(JSON.parse(init.body), { ref: `refs/tags/${tag}`, sha });
      return json({ object: { type: "commit", sha } });
    }
    if (endpoint.includes("/releases/tags/") || endpoint.endsWith("/releases")) {
      const listed = url.searchParams.has("page");
      if (method === "GET" && !listed && (options.missingRelease || options.draft)) return null;
      if (method === "POST") assert.equal(JSON.parse(init.body).draft, true);
      const release = { id: 1, tag_name: tag, draft: options.missingRelease || options.draft || false,
        upload_url: "https://uploads.github.com/assets{?name,label}",
        assets: [...assets.keys()].map(name => ({ name, url: `https://api.github.com/assets/${name}` })) };
      return json(listed ? (options.missingRelease ? [] : [release]) : release);
    }
    if (url.hostname === "uploads.github.com") {
      assert.equal(method, "POST");
      const name = url.searchParams.get("name");
      assets.set(name, Buffer.from(init.body));
      return json({ url: `https://api.github.com/assets/${name}` });
    }
    if (endpoint.startsWith("/assets/")) {
      const bytes = options.badAsset ? Buffer.from("different") : assets.get(endpoint.slice(8));
      return new Response(bytes);
    }
    if (method === "PATCH" && endpoint.endsWith("/releases/1")) return json({ draft: false });
    assert.fail(`unexpected request ${method} ${url}`);
  };
  const run = (command, args) => {
    assert.equal(command, process.env.NPM || "npm");
    assert.equal(args[0], "publish");
    assert.ok(args.includes("--ignore-scripts"));
    assert.ok(args[1].endsWith("comwit-cli-1.2.3.tgz"));
    published.push(args);
    hasNpm = true;
  };
  return { api, run, calls, published, assets, allFiles };
}

// Tests never use real credentials or network for publication.
const previousRepo = process.env.GITHUB_REPOSITORY;
const previousToken = process.env.GH_TOKEN;
process.env.GITHUB_REPOSITORY = "burrr-ai/comwit-cli";
process.env.GH_TOKEN = "test-token";
after(() => {
  if (previousRepo === undefined) delete process.env.GITHUB_REPOSITORY;
  else process.env.GITHUB_REPOSITORY = previousRepo;
  if (previousToken === undefined) delete process.env.GH_TOKEN;
  else process.env.GH_TOKEN = previousToken;
});

test("new release creates exact tag, draft, all assets, then publishes npm once", async () => {
  const f = publication({ missingTag: true, missingRelease: true, hasNpm: false });
  await publishRelease(output, tag, sha, true, f.api, f.run);
  assert.equal(f.published.length, 1);
  assert.deepEqual([...f.assets.keys()].sort(), f.allFiles.sort());
  assert.ok(f.calls.some(c => c.method === "PATCH"));
});

test("existing annotated tag, complete release, and matching npm version are a no-op", async () => {
  const f = publication({ annotated: true, existingAssets: [...Object.keys(manifest.files), "release.json"] });
  await publishRelease(output, tag, sha, true, f.api, f.run);
  assert.equal(f.published.length, 0);
  assert.ok(f.calls.every(c => c.method === "GET"));
});

test("a later repair restores the original bundle without compilation or packing", async () => {
  const f = publication({ existingAssets: [...Object.keys(manifest.files), "release.json"] });
  const restored = path.join(fixture, "restored");
  assert.equal(await restoreRelease(restored, tag, sha, f.api), true);
  assert.deepEqual(verifyRelease(restored, tag, sha), manifest);
  assert.equal(builds.length, 6);
  assert.ok(f.calls.every(c => c.method === "GET"));
  await assert.rejects(restoreRelease(restored, tag, "b".repeat(40), f.api));
});

test("missing or incomplete releases fall back to the single build", async () => {
  for (const options of [{ missingRelease: true }, { existingAssets: ["checksums.txt"] }]) {
    const f = publication(options);
    assert.equal(await restoreRelease(path.join(fixture, "absent"), tag, sha, f.api), false);
  }
});

test("partial draft resumes only missing assets; npm remains explicitly disabled", async () => {
  const f = publication({ draft: true, existingAssets: ["comwit_linux_amd64.tar.gz"] });
  await publishRelease(output, tag, sha, false, f.api, f.run);
  assert.equal(f.calls.filter(c => c.method === "POST").length, 6);
  assert.equal(f.published.length, 0);
  assert.ok(!f.calls.some(c => c.endpoint.startsWith("/comwit-cli/")));
});

test("unprotected main, off-main source, and moved tags fail before writes", async () => {
  for (const options of [{ protected: false }, { ancestry: "diverged" }, { tagSha: "b".repeat(40) }]) {
    const f = publication(options);
    await assert.rejects(publishRelease(output, tag, sha, true, f.api, f.run));
    assert.ok(f.calls.every(c => c.method === "GET"));
    assert.equal(f.published.length, 0);
  }
});

test("conflicting GitHub and npm assets fail without overwrite or republish", async () => {
  for (const options of [{ existingAssets: ["checksums.txt"], badAsset: true }, { integrity: "different" }]) {
    const f = publication(options);
    await assert.rejects(publishRelease(output, tag, sha, true, f.api, f.run), /differs/);
    assert.equal(f.published.length, 0);
  }
});

test("only HTTP 404 means absent, never authorization failure or outage", async () => {
  const original = globalThis.fetch;
  try {
    for (const status of [401, 403, 500]) {
      globalThis.fetch = async () => new Response("", { status });
      await assert.rejects(request("https://example.com/release", {}, true), new RegExp(`HTTP ${status}`));
    }
    globalThis.fetch = async () => new Response("", { status: 404 });
    assert.equal(await request("https://example.com/release", {}, true), null);
  } finally { globalThis.fetch = original; }
});

test("workflow contract: open PRs, one test/build pass, protected source, App and opt-in OIDC", () => {
  const ci = readFileSync(path.join(root, ".github/workflows/ci.yml"), "utf8");
  const release = readFileSync(path.join(root, ".github/workflows/release.yml"), "utf8");
  assert.match(ci, /types: \[opened, synchronize, reopened, ready_for_review\]/);
  assert.match(ci, /if: github.event.pull_request.state == 'open'/);
  assert.equal((ci.match(/runs-on:/g) || []).length, 1);
  for (const workflow of [ci, release]) {
    assert.equal((workflow.match(/run: go test \.\/\.\.\./g) || []).length, 1);
    assert.equal((workflow.match(/run: node npm\/scripts\/verify.mjs/g) || []).length, 1);
    for (const line of workflow.matchAll(/uses: ([^\n]+)/g)) {
      assert.match(line[1], /@[a-f0-9]{40} /, "actions must be immutable");
    }
  }
  assert.match(release, /github.ref == 'refs\/heads\/main'/);
  assert.match(release, /github.actor != 'burrr-ai-release-automation\[bot\]'/);
  assert.match(release, /branches\/main.*--jq \.protected/);
  assert.match(release, /git merge-base --is-ancestor "\$source_sha" origin\/main/);
  assert.match(release, /git rev-parse "refs\/tags\/\$VERSION\^\{commit\}"/);
  assert.equal((release.match(/release.mjs pack /g) || []).length, 1);
  assert.match(release, /if: steps.restore.outputs.restored != 'true'/);
  assert.match(release, /cache: false/);
  assert.match(release, /GOCACHE=\$RUNNER_TEMP\/comwit-release-go-cache/);
  assert.match(release, /needs: build/);
  assert.match(release, /id-token: write/);
  assert.match(release, /- name: Require release App client ID\n\s+env:\n\s+APP_CLIENT_ID: \$\{\{ vars\.COMWIT_RELEASE_APP_CLIENT_ID \}\}\n\s+run: \|\n\s+if \[\[ -z "\$APP_CLIENT_ID" \]\]; then\n\s+echo "::error::Set the COMWIT_RELEASE_APP_CLIENT_ID organization variable"\n\s+exit 1\n\s+fi/);
  assert.match(release, /app-id: \$\{\{ vars\.COMWIT_RELEASE_APP_CLIENT_ID \}\}/);
  assert.match(release, /permission-contents: write/);
  assert.match(release, /default: false/);
  assert.match(release, /PUBLISH_NPM: .*inputs.publish_npm/);
  assert.ok(!release.includes("NODE_AUTH_TOKEN"));
  assert.equal(JSON.parse(readFileSync(path.join(root, "package.json"))).scripts.prepack, undefined);
});

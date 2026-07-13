import { execFileSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { verifyVersion } from "./version.mjs";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDir, "..", "..");
const outputDir = path.join(repositoryRoot, "npm", "dist");
const goBinary = process.env.GO || "go";
const version = verifyVersion(repositoryRoot);
const targets = [
  { goos: "darwin", goarch: "amd64", output: "comwit-darwin-x64" },
  { goos: "darwin", goarch: "arm64", output: "comwit-darwin-arm64" },
  { goos: "linux", goarch: "amd64", output: "comwit-linux-x64" },
  { goos: "linux", goarch: "arm64", output: "comwit-linux-arm64" },
  { goos: "windows", goarch: "amd64", output: "comwit-win32-x64.exe" },
  { goos: "windows", goarch: "arm64", output: "comwit-win32-arm64.exe" }
];

rmSync(outputDir, { recursive: true, force: true });
mkdirSync(outputDir, { recursive: true });
console.log(`building comwit ${version} npm binaries`);

const managedCacheRoot = process.env.GOCACHE
  ? null
  : mkdtempSync(path.join(tmpdir(), "comwit-npm-go-cache-"));

try {
  for (const target of targets) {
    const outputPath = path.join(outputDir, target.output);
    const targetCache = managedCacheRoot
      ? path.join(managedCacheRoot, `${target.goos}-${target.goarch}`)
      : process.env.GOCACHE;
    if (managedCacheRoot) {
      mkdirSync(targetCache, { recursive: true });
    }
    console.log(`building npm binary ${target.goos}/${target.goarch}`);
    execFileSync(
      goBinary,
      ["build", "-trimpath", "-ldflags=-s -w", "-o", outputPath, "./cmd/comwit"],
      {
        cwd: repositoryRoot,
        env: {
          ...process.env,
          CGO_ENABLED: "0",
          GOCACHE: targetCache,
          GOOS: target.goos,
          GOARCH: target.goarch
        },
        stdio: "inherit"
      }
    );
    if (target.goos !== "windows") {
      chmodSync(outputPath, 0o755);
    }
    if (managedCacheRoot) {
      rmSync(targetCache, { recursive: true, force: true });
    }
  }
} finally {
  if (managedCacheRoot) {
    rmSync(managedCacheRoot, { recursive: true, force: true });
  }
}

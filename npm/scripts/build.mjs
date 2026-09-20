import { execFileSync } from "node:child_process";
import { chmodSync, mkdirSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { verifyVersion } from "./version.mjs";

export const targets = [
  { goos: "darwin", goarch: "amd64", output: "comwit-darwin-x64" },
  { goos: "darwin", goarch: "arm64", output: "comwit-darwin-arm64" },
  { goos: "linux", goarch: "amd64", output: "comwit-linux-x64" },
  { goos: "linux", goarch: "arm64", output: "comwit-linux-arm64" },
  { goos: "windows", goarch: "amd64", output: "comwit-win32-x64.exe" },
  { goos: "windows", goarch: "arm64", output: "comwit-win32-arm64.exe" }
];

export function buildBinaries(repositoryRoot, run = execFileSync) {
  const version = verifyVersion(repositoryRoot);
  const outputDir = path.join(repositoryRoot, "npm", "dist");
  const goBinary = process.env.GO || "go";
  // Go's cache already keys on OS/architecture. Keep one cache for all targets
  // and the preceding tests; never delete it between compiler invocations.
  const cache = process.env.GOCACHE || run(goBinary, ["env", "GOCACHE"], {
    encoding: "utf8"
  }).trim();
  rmSync(outputDir, { recursive: true, force: true });
  mkdirSync(outputDir, { recursive: true });
  console.log(`building comwit ${version}: six targets with one GOCACHE`);
  for (const target of targets) {
    const outputPath = path.join(outputDir, target.output);
    run(goBinary, ["build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w",
      "-o", outputPath, "./cmd/comwit"], {
      cwd: repositoryRoot,
      env: { ...process.env, CGO_ENABLED: "0", GOCACHE: cache,
        GOOS: target.goos, GOARCH: target.goarch },
      stdio: "inherit"
    });
    chmodSync(outputPath, 0o755);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  buildBinaries(path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", ".."));
}

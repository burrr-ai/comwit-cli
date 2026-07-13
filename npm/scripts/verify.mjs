import { execFileSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { verifyVersion } from "./version.mjs";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDir, "..", "..");
const goBinary = process.env.GO || "go";

const version = verifyVersion(repositoryRoot);
console.log(`verified comwit version ${version}`);
execFileSync(goBinary, ["test", "./..."], {
  cwd: repositoryRoot,
  env: process.env,
  stdio: "inherit"
});

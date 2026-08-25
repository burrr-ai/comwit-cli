import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { verifyVersion } from "./version.mjs";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDir, "..", "..");
const goBinary = process.env.GO || "go";

const version = verifyVersion(repositoryRoot);
console.log(`verified comwit version ${version}`);
const expectedContract = JSON.parse(
  readFileSync(
    path.join(repositoryRoot, "contracts", "comwit-infra-migration.expected.json"),
    "utf8",
  ),
);
if (
  expectedContract.contractId !== "comwit-infra-migration-expected-v1" ||
  expectedContract.ports.projectToken.runtimeScopeNames !== null ||
  !expectedContract.pendingErrorCodes.includes("storage_cors_contract")
) {
  throw new Error("Comwit infrastructure expected contract fixture is invalid");
}
console.log(`verified expected contract ${expectedContract.contractId}`);
execFileSync(goBinary, ["test", "./..."], {
  cwd: repositoryRoot,
  env: process.env,
  stdio: "inherit"
});

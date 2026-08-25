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

const hasExactItems = (actual, expected) =>
  Array.isArray(actual) &&
  actual.length === expected.length &&
  actual.every((item, index) => item === expected[index]);

if (
  expectedContract.contractId !== "comwit-infra-migration-expected-v2" ||
  !hasExactItems(expectedContract.template?.values, ["mvpstar", "comwit"]) ||
  expectedContract.template?.default !== "mvpstar" ||
  Object.keys(expectedContract.credentials ?? {}).length !== 3 ||
  expectedContract.credentials?.mcp !== "COMWIT_PAT" ||
  expectedContract.credentials?.runtime !== "COMWIT_CLOUD_TOKEN" ||
  expectedContract.credentials?.deploy !== "COMWIT_DEPLOY_TOKEN" ||
  !hasExactItems(expectedContract.ports?.cgSessionCreate?.requiredFields, [
    "memberId",
    "projectName",
  ]) ||
  !hasExactItems(expectedContract.ports?.cgSessionCreate?.optionalFields, [
    "template",
  ]) ||
  !hasExactItems(expectedContract.ports?.cgConfiguration?.environmentKeys, [
    "COMWIT_PROJECT",
    "COMWIT_APP",
  ]) ||
  expectedContract.resourceBindings?.mode !== "tracked_source" ||
  expectedContract.resourceBindings?.sourceFile !==
    "src/server/resource-config.ts" ||
  expectedContract.resourceBindings?.exportName !== "comwitResources" ||
  expectedContract.resourceBindings?.databaseField !== "databaseUrl" ||
  expectedContract.resourceBindings?.storageFields?.id !== "id" ||
  expectedContract.resourceBindings?.storageFields?.endpoint !== "endpoint" ||
  expectedContract.resourceBindings?.storageFields?.bucket !== "bucket" ||
  expectedContract.resourceBindings?.storageFields?.publicBaseUrl !==
    "publicBaseUrl" ||
  expectedContract.resourceBindings?.stagingEnvCleanup?.after !==
    "verified_source_update" ||
  !hasExactItems(
    expectedContract.resourceBindings?.stagingEnvCleanup?.database,
    ["DATABASE_URL"],
  ) ||
  !hasExactItems(expectedContract.resourceBindings?.stagingEnvCleanup?.storage, [
    "COMWIT_STORAGE_ID",
    "COMWIT_STORAGE_ENDPOINT",
    "COMWIT_STORAGE_BUCKET",
    "COMWIT_STORAGE_PUBLIC_BASE_URL",
  ]) ||
  expectedContract.ports?.projectToken?.runtimeScopeNames !== null ||
  expectedContract.pendingErrorCodes?.includes("storage_cors_contract") !== true
) {
  throw new Error("Comwit infrastructure expected contract fixture is invalid");
}
console.log(`verified expected contract ${expectedContract.contractId}`);
execFileSync(goBinary, ["test", "./..."], {
  cwd: repositoryRoot,
  env: process.env,
  stdio: "inherit"
});

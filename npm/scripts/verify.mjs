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

const requiredContextKeys = ["COMWIT_PROJECT", "COMWIT_APP"];
const optionalResourceKeys = [
  "COMWIT_DATABASE_ID",
  "DATABASE_URL",
  "COMWIT_STORAGE_ID",
  "COMWIT_STORAGE_ENDPOINT",
  "COMWIT_STORAGE_BUCKET",
  "COMWIT_STORAGE_PUBLIC_BASE_URL",
];

if (
  expectedContract.contractId !== "comwit-infra-migration-expected-v6" ||
  expectedContract.template !== undefined ||
  Object.keys(expectedContract.credentials ?? {}).length !== 3 ||
  expectedContract.credentials?.mcp !== "COMWIT_PAT" ||
  expectedContract.credentials?.runtime !== "COMWIT_CLOUD_TOKEN" ||
  expectedContract.credentials?.deploy !== "COMWIT_DEPLOY_TOKEN" ||
  expectedContract.localUserAuthentication?.method !== "device_flow" ||
  expectedContract.localUserAuthentication?.startCommand !== "comwit login" ||
  expectedContract.localUserAuthentication?.verificationCommand !==
    "comwit projects list" ||
  expectedContract.localUserAuthentication?.deviceStartPath !==
    "/v1/auth/device" ||
  expectedContract.localUserAuthentication?.devicePollPath !==
    "/v1/auth/device/token" ||
  expectedContract.localUserAuthentication?.serverTokenIssue !== false ||
  expectedContract.localUserAuthentication?.serverTokenRevoke !== false ||
  expectedContract.ports?.userToken !== undefined ||
  expectedContract.resourceBindings !== undefined ||
  expectedContract.localEnvironment?.mode !==
    "persistent_gitignored_env" ||
  expectedContract.localEnvironment?.path !== ".env" ||
  !hasExactItems(
    expectedContract.localEnvironment?.requiredContextKeys,
    requiredContextKeys,
  ) ||
  !hasExactItems(
    expectedContract.localEnvironment?.optionalResourceKeys,
    optionalResourceKeys,
  ) ||
  !hasExactItems(expectedContract.localEnvironment?.secretKeys, [
    "COMWIT_CLOUD_TOKEN",
  ]) ||
  !hasExactItems(
    expectedContract.deploymentEnvironment?.requiredVariableKeys,
    requiredContextKeys,
  ) ||
  !hasExactItems(
    expectedContract.deploymentEnvironment?.optionalResourceVariableKeys,
    optionalResourceKeys,
  ) ||
  !hasExactItems(expectedContract.deploymentEnvironment?.secretKeys, [
    "COMWIT_CLOUD_TOKEN",
    "COMWIT_DEPLOY_TOKEN",
  ]) ||
  expectedContract.projectResourceSetup?.controlPlane !== "comwit_mcp" ||
  expectedContract.projectResourceSetup?.listTool !== "list_cloud_resources" ||
  expectedContract.projectResourceSetup?.createDatabaseTool !==
    "create_cloud_database" ||
  expectedContract.projectResourceSetup?.createStorageTool !==
    "create_cloud_storage" ||
  expectedContract.projectResourceSetup?.deploymentSyncTool !==
    "sync_cloud_resource_env" ||
  expectedContract.projectResourceSetup?.localEnvWriter !==
    ".agents/scripts/sync-resource-env.mjs" ||
  expectedContract.projectResourceSetup?.manualEnvEditing !== false ||
  expectedContract.projectResourceSetup?.resourceCliRequired !== false ||
  expectedContract.ports?.cgRouting !== undefined ||
  expectedContract.ports?.cgSessionTemplateSelection?.status !==
    "pending_external_contract" ||
  expectedContract.ports?.cgSessionTemplateSelection?.baseUrl !==
    "https://cg.mvpstar.net" ||
  expectedContract.ports?.cgSessionTemplateSelection?.path !== "/session" ||
  expectedContract.ports?.cgSessionTemplateSelection?.selectorField !== null ||
  expectedContract.ports?.cgSessionTemplateSelection?.intendedValue !==
    "comwit" ||
  !hasExactItems(
    expectedContract.ports?.cgSessionTemplateSelection?.requestFields,
    ["memberId", "projectName"],
  ) ||
  expectedContract.ports?.cgSessionTemplateSelection?.responseEnvelope !==
    "session" ||
  expectedContract.ports?.cgConfiguration?.status !==
    "pending_external_contract" ||
  expectedContract.ports?.cgConfiguration?.path !==
    "/session/{sessionId}/env" ||
  expectedContract.ports?.cgConfiguration?.apiSurface !== "same_as_legacy" ||
  !hasExactItems(
    expectedContract.ports?.cgConfiguration?.requiredVariableKeys,
    requiredContextKeys,
  ) ||
  !hasExactItems(
    expectedContract.ports?.cgConfiguration?.optionalResourceVariableKeys,
    optionalResourceKeys,
  ) ||
  !hasExactItems(expectedContract.ports?.cgConfiguration?.secretKeys, [
    "COMWIT_CLOUD_TOKEN",
    "COMWIT_DEPLOY_TOKEN",
  ]) ||
  !hasExactItems(
    expectedContract.ports?.cgConfiguration?.requiredGuarantees,
    [
      "idempotent",
      "available_to_actions",
      "masked",
      "rotatable",
      "deletable",
      "retryable",
    ],
  ) ||
  expectedContract.ports?.projectToken?.runtimeScopeNames !== null ||
  expectedContract.ports?.databaseAuthentication?.status !==
    "pending_external_contract" ||
  expectedContract.stableExternalContracts?.storagePresignPath !==
    "/v1/projects/{projectId}/storages/{storageId}/presigned-urls" ||
  expectedContract.stableExternalContracts?.storageCorsProvisioning !==
    "automatic_on_storage_setup" ||
  !hasExactItems(expectedContract.runtimeErrorCodes, [
    "comwit_cloud_resource_binding_selection",
  ]) ||
  !hasExactItems(expectedContract.pendingErrorCodes, [
    "comwit_cloud_project_token_scopes",
    "comwit_cloud_database_server_create_auth",
    "cg_template_selection",
    "cg_environment_secret_upsert",
    "database_comwit_token_auth",
    "storage_comwit_token_auth",
    "storage_presign_contract",
    "prism_member_pat_revoke",
  ]) ||
  expectedContract.pendingErrorCodes?.includes("comwit_cloud_user_token_issue") ||
  expectedContract.pendingErrorCodes?.includes("comwit_cloud_user_token_revoke") ||
  expectedContract.pendingErrorCodes?.includes("cg_comwit_session_create")
) {
  throw new Error("Comwit infrastructure expected contract fixture is invalid");
}
console.log(`verified expected contract ${expectedContract.contractId}`);
execFileSync(goBinary, ["test", "./..."], {
  cwd: repositoryRoot,
  env: process.env,
  stdio: "inherit"
});

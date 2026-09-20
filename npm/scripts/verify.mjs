import path from "node:path";
import { fileURLToPath } from "node:url";

import { verifyVersion } from "./version.mjs";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = process.argv[2]
  ? path.resolve(process.argv[2])
  : path.resolve(scriptDir, "..", "..");

const version = verifyVersion(repositoryRoot);
console.log(`verified comwit version ${version}`);

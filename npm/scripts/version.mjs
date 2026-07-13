import { readFileSync } from "node:fs";
import path from "node:path";

export function verifyVersion(repositoryRoot) {
  const packageJSON = JSON.parse(
    readFileSync(path.join(repositoryRoot, "package.json"), "utf8")
  );
  const mainSource = readFileSync(
    path.join(repositoryRoot, "cmd", "comwit", "main.go"),
    "utf8"
  );
  const match = mainSource.match(/\bversion\s*=\s*"([^"]+)"/);
  const goVersion = match?.[1] ?? "missing";
  if (goVersion !== packageJSON.version) {
    throw new Error(
      `version mismatch: package=${packageJSON.version}, go=${goVersion}`
    );
  }
  return packageJSON.version;
}

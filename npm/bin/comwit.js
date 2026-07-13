#!/usr/bin/env node
"use strict";

const { spawnSync } = require("node:child_process");
const { existsSync } = require("node:fs");
const path = require("node:path");

const targets = {
  "darwin-x64": "comwit-darwin-x64",
  "darwin-arm64": "comwit-darwin-arm64",
  "linux-x64": "comwit-linux-x64",
  "linux-arm64": "comwit-linux-arm64",
  "win32-x64": "comwit-win32-x64.exe",
  "win32-arm64": "comwit-win32-arm64.exe"
};

const target = `${process.platform}-${process.arch}`;
const binaryName = targets[target];

if (!binaryName) {
  console.error(`comwit-cli does not ship a binary for ${target}.`);
  process.exit(1);
}

const packageRoot = path.resolve(__dirname, "..");
const binaryPath = path.join(packageRoot, "dist", binaryName);

if (!existsSync(binaryPath)) {
  console.error(`comwit-cli package is missing ${path.relative(packageRoot, binaryPath)}.`);
  process.exit(1);
}

const result = spawnSync(binaryPath, process.argv.slice(2), {
  stdio: "inherit",
  env: process.env
});

if (result.error) {
  console.error(`failed to start comwit: ${result.error.message}`);
  process.exit(1);
}

if (result.signal) {
  process.kill(process.pid, result.signal);
}

process.exit(result.status === null ? 1 : result.status);

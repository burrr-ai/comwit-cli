# comwit CLI

Command-line client for the **comwit.io** cloud platform — create databases and
Storage, manage apps, and deploy, against `https://api.cloud.comwit.io`.

This repository is the canonical source for the CLI implementation, tests,
installer, setup action, versioning, and GitHub releases. The public API
contract and product guides live in `burrr-ai/comwit-cloud`.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/burrr-ai/comwit-cli/main/install.sh | sh
```

This downloads the binary for your OS/arch from the latest [release](https://github.com/burrr-ai/comwit-cli/releases),
verifies its checksum, and installs it to `/usr/local/bin` (or `~/.local/bin`).

## GitHub Actions

Use the setup action to install `comwit` and add it to `PATH` for later steps:

```yaml
steps:
  - uses: actions/checkout@v4
  - uses: burrr-ai/comwit-cli@v0

  - run: comwit version
```

Or with Go:

```sh
go install github.com/burrr-ai/comwit-cli/cmd/comwit@latest
```

## Usage

```sh
comwit login --token <cwt_token>     # authenticate (token from the dashboard)
comwit projects list
comwit databases create --project <id> --name <name>
comwit databases create --project <id> --name <name> --from-file ./app.sqlite --token-out ./database.token
comwit databases create --project <id> --name <name> --from-dump ./dump.sql --sqlite-out ./app.sqlite --token-out ./database.token
comwit databases import-dump --project <id> --name <name> --from-dump dump.sql
comwit databases list --project <id>
comwit databases execute --project <id> --database <id> --command 'select 1;'
comwit databases execute --project <id> --database <id> --file ./migration.sql
comwit databases restore-points list --project <id> --database <id>
comwit databases restore --project <id> --database <id> --at 2026-07-13T00:00:00Z
comwit databases operation status --project <id> --database <id> --operation <op-id> --wait
comwit storage create --project <id> --name <globally-unique-bucket> --public
comwit storage list --project <id>
comwit storage get --project <id> --storage <id>
comwit storage public enable --project <id> --storage <id>
comwit storage delete --project <id> --storage <id>
comwit domains ...                   # delegated DNS and records
comwit apps ...                      # see `comwit --help`
comwit update                        # self-update to the latest release
comwit version
```

`databases execute` and the PITR commands above are available in v0.1.6 and
require the matching platform-api deployment.

`databases create` requests Comwit authentication and prints the logged-in
`cwt_` as the local connection token; it never exposes the deprecated Louhi
tenant token. The token needs `database:connect`, and tokens issued before that
scope existed must be replaced. Use a project-owned `cwp_` with only
`database:connect` for a deployed workload instead of copying your personal
token into app environment.

`databases create --from-file` validates a standalone SQLite file, streams it
with an exact content length, and waits for the new database to become ready.
Use `--skip-local-checks` to omit the local integrity and foreign-key checks,
`--idempotency-key` to resume a known attempt, or `--no-wait` to return after
the upload is accepted; `--token-out` writes the logged-in connection token
with mode `0600`. Seed and restore creation remain hybrid compatibility flows
in the current platform contract, but the CLI ignores their legacy token and
uses the logged-in token for Gateway connections.

`databases create --from-dump` converts a SQLite-compatible SQL dump into a
temporary SQLite file with the built-in engine, then runs the same validation,
upload, and wait flow as `--from-file`. It does not require a `sqlite3` binary
and never sends SQL text to the API. Pass `--sqlite-out <path>` to keep the
converted file for inspection or a later retry; the destination must not
already exist. `--from-dump` and `--from-file` are mutually exclusive.

`databases create --from-file`, `databases create --from-dump`, and
`databases operation status` are available in v0.1.8 and require the matching
platform-api deployment.

Storage lifecycle and public-access commands are available in v0.1.7 and
require the matching Storage platform-api deployment.

Get a `cwt_` token from the platform dashboard, or use `comwit login` (device flow).
The device requests no explicit scope list; the console applies its current
default scopes, including `database:connect` for write-capable users.
SQL execution is remote by default and goes through the project-scoped Comwit
API; use `--json` for the stable API result envelope. See the
[full CLI guide](https://github.com/burrr-ai/comwit-cloud/blob/main/docs/guides/cli.md)
for Storage/S3 setup, query limits, PITR, domains, apps, and deploy workflows.

## Releasing

```sh
GO=/path/to/go ./release.sh vX.Y.Z
```

`release.sh` requires GitHub CLI and npm authentication up front, verifies the
Go and npm versions, runs the tests, publishes the darwin/linux GitHub assets
with `checksums.txt`, and publishes the same version to npm. It is safe to rerun
when the GitHub Release already exists at the current commit or the npm version
is already published. To repair only a missing npm mirror, run
`./publish-npm.sh vX.Y.Z`.

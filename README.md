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
comwit login                         # authenticate through browser device approval
comwit projects list
comwit auth export-token --env-out .env
comwit databases create --project <id> --name <name> --env-out .env
comwit databases configure [--database <id>] --env-out .env
comwit databases import-dump --project <id> --name <name> --from-dump dump.sql
comwit databases list --project <id>
comwit databases execute --project <id> --database <id> --command 'select 1;'
comwit databases execute --project <id> --database <id> --file ./migration.sql
comwit databases restore-points list --project <id> --database <id>
comwit databases restore --project <id> --database <id> --at 2026-07-13T00:00:00Z
comwit storage create --project <id> --name <globally-unique-bucket> --public --env-out .env
comwit storage list --project <id>
comwit storage get --project <id> [--storage <id>] --env-out .env
comwit storage public enable --project <id> --storage <id>
comwit storage delete --project <id> --storage <id>
comwit domains ...                   # delegated DNS and records
comwit apps ...                      # see `comwit --help`
comwit update                        # self-update to the latest release
comwit version
```

`databases execute` and the PITR commands above are available in v0.1.6 and
require the matching platform-api deployment.

Storage lifecycle and public-access commands are available in v0.1.7 and
require the matching Storage platform-api deployment.

For project handoff, intentionally run `comwit login`. The command opens the
Comwit Cloud device-authorization page, shows the one-time approval code, and
waits while the user approves the connection. Treat the handoff as incomplete
until `comwit login` reports success and a following `comwit projects list`
also succeeds. The CLI stores the returned `cwt_` itself and never prints that
token, so the user must not be asked to copy or paste it into chat or a shell.
If the browser does not open automatically, use the verification URL and code
printed by the command. A denied, expired, timed-out, or failed request must be
reported and retried with a new `comwit login`; do not invent or request a
replacement token.
Project-aware commands resolve `--project`, then `COMWIT_PROJECT`, then the
current directory's gitignored `.env`, then the config default. App-aware
commands resolve `--app`, then `COMWIT_APP`, then `.env`. Context resolution
does not read `COMWIT_CLOUD_TOKEN` from `.env`. `databases configure` resolves
its database ID from `--database`, then `COMWIT_DATABASE_ID` in the safe cwd
`.env`; `storage get` does the same with `--storage` and
`COMWIT_STORAGE_ID`. Resource-ID shell variables are deliberately ignored. A
tracked, unignored, non-regular, unreadable, malformed, or duplicate context
file/key is an error; the CLI does not silently fall back or choose another
Cloud resource.

`comwit auth export-token --env-out .env` copies the logged-in user `cwt_` into
`COMWIT_CLOUD_TOKEN` without printing it. Use that file-writing command instead
of exporting the credential into the shell or parsing command stdout.
Database and Storage `--env-out .env` flags use the same protected writer and
maintain persistent local environment values:

- `databases create --env-out .env` and `databases configure --env-out .env`
  atomically update the complete `COMWIT_DATABASE_ID` and `DATABASE_URL` pair.
  Create neither prints nor persists the legacy one-time `database_token` when
  `--env-out` is used.
- `storage create --env-out .env` and `storage get --env-out .env` update only
  the four `COMWIT_STORAGE_*` values. `COMWIT_STORAGE_PUBLIC_BASE_URL` may be
  empty while Storage is private or public delivery is still provisioning.

The writer refuses tracked or non-gitignored files, preserves unrelated keys
and comments, replaces the file atomically, and applies user-only permissions
where supported. Existing-resource export commands resolve their resource ID
from the explicit flag, then the matching key in the safe cwd `.env`; without
either they stop with a missing-ID error. Create exports only the resource
created by that request. The CLI does not aggregate a project environment or
select the first resource.
`COMWIT_PROJECT` and `COMWIT_APP` remain caller-supplied project context.
`COMWIT_DATABASE_ID` and `DATABASE_URL` are an inseparable selected-database
pair; an incomplete Cloud response is rejected before the env file changes.

For deployment configuration, store `COMWIT_PROJECT` and `COMWIT_APP` as
Variables. Add `COMWIT_DATABASE_ID`, `DATABASE_URL`, and the
`COMWIT_STORAGE_*` values as Variables only for selected resources. Store
`COMWIT_CLOUD_TOKEN` and
`COMWIT_DEPLOY_TOKEN` as Secrets, never as Variables.

Template Database and Storage setup skills use this CLI as their only resource
control plane. They first run `databases configure --env-out .env` or
`storage get --env-out .env` to refresh an ID already stored in the safe cwd
`.env`. Only a missing-ID error triggers a list. For that resource type, zero
results means confirm creation with the user, one means pass the sole ID to
configure/get, and two or more means stop with an ambiguity error and no
selection prompt. They must not derive env values by parsing stdout. General
CLI commands continue to accept explicit resource IDs.
This CLI remains a client of the Comwit Cloud public API and does not depend on
Comwit product bindings, MCP, or CG/Gitea env. It does not select or replace a
production binding and does not provide a resource-sync command. Production
deployment-environment reconciliation belongs to the Comwit server's publish
flow, outside this CLI.

Creating the remote resource and writing `.env` are separate steps. If a
`databases create --env-out .env` invocation creates the database but the local
write fails, recover with `databases configure --database <id> --env-out .env`
instead of creating another database. For the equivalent Storage failure, use
`storage get --project <id> --storage <id> --env-out .env`. An invocation
without `--env-out` retains the existing one-time database token output for
compatibility; automated setup must not scrape that output.

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

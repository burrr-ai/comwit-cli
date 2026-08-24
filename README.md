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
comwit databases import-dump --project <id> --name <name> --from-dump dump.sql
comwit databases list --project <id>
comwit databases execute --project <id> --database <id> --command 'select 1;'
comwit databases execute --project <id> --database <id> --file ./migration.sql
comwit databases restore-points list --project <id> --database <id>
comwit databases restore --project <id> --database <id> --at 2026-07-13T00:00:00Z
comwit storage create --project <id> --name <globally-unique-bucket> --public
comwit storage list --project <id>
comwit storage get --project <id> --storage <id>
comwit storage public enable --project <id> --storage <id>
comwit storage delete --project <id> --storage <id>
comwit domains ...                   # delegated DNS and records
comwit apps create --project <id> --name web
comwit apps get --project <id> --app <id>
comwit deploy --project <id> --app <id> --package ./dist
comwit apps rollback --project <id> --app <id> --build <build-id>
comwit update                        # self-update to the latest release
comwit version
```

`databases execute` and the PITR commands above are available in v0.1.6 and
require the matching platform-api deployment.

Storage lifecycle and public-access commands are available in v0.1.7 and
require the matching Storage platform-api deployment.

The unreleased `0.1.8` source candidate adds app create/get/list output for the
`default_domain` returned by platform-api and deploy/rollback output for its
`default_url` once a build is active. Storage detail output likewise preserves
the API-provided domain. The CLI does not construct either hostname from a
resource id. At the 2026-08-24 candidate handoff, the published `0.1.7` did not
include the new app commands/output. The canonical hostname examples require
the matching platform-api hostname rollout before the `0.1.8` release.

Get a `cwt_` token from the platform dashboard, or use `comwit login` (device flow).
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

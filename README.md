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

After `comwit login`, Git clone and push at `https://git.cloud.comwit.io/<project>/<repository>.git` use the CLI token without a prompt.
Remove the helper with `git config --global --unset credential.https://git.cloud.comwit.io.helper`.

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

The **CLI release** workflow (`.github/workflows/release.yml`) owns publication.
Merge matching versions in `cmd/comwit/main.go` and `package.json` into protected
`main`, then dispatch from `main`:

```sh
gh workflow run release.yml --ref main -f version=vX.Y.Z
```

Pushing a `vX.Y.Z` tag also releases its exact commit, provided that commit is
on protected `main`. A dispatch uses an existing tag's commit for repair, or the
dispatch's main commit for a new version. Annotated tags are supported; a moved
tag or mismatched version is rejected. App-created tags during dispatch do not
start another build.

The workflow runs Go tests once, validates version identity, and builds the six
darwin/linux/windows × amd64/arm64 targets once with a shared Go cache. It packages
the four installer-compatible GitHub tarballs and the npm tarball from those same
binaries, then verifies their hashes and contents. Lifecycle scripts are disabled
for packing and publication. The GitHub release includes `checksums.txt`, the npm
tarball, and `release.json` with the source SHA and asset/binary hashes. Publication
uses a repository-scoped release-automation App token; the workflow's built-in
GitHub token has read-only repository access.

One-time setup before the first release:

1. Protect `main` with required review and the `CLI tests` check. The workflow
   checks GitHub's branch protection status and fails closed if it is absent.
   Limit release-tag creation to maintainers and the release App.
2. Expose `COMWIT_RELEASE_APP_CLIENT_ID` and `COMWIT_RELEASE_APP_PRIVATE_KEY` as
   Actions secrets, with the App installed on this repository and Contents write
   permission.
3. In the npm `comwit-cli` package settings, configure a
   [GitHub Actions trusted publisher](https://docs.npmjs.com/trusted-publishers/)
   for organization `burrr-ai`, repository `comwit-cli`, workflow filename
   `release.yml`, and no environment name. Enable direct `npm publish` for this
   publisher. The workflow uses Node 24, npm 11.17.0, and `id-token: write`; no
   npm token is needed.

npm publication is disabled by default, including for tag pushes. After the
trusted publisher is configured, publish both channels or repair the npm mirror:

```sh
gh workflow run release.yml --ref main -f version=vX.Y.Z -f publish_npm=true
```

For a failed publication, use **Re-run failed jobs**: the publish job downloads
the original qualified artifact (retained for 90 days) without testing or
compiling again. A later dispatch can restore the complete bundle from the GitHub
release, also without recompiling. Missing assets are uploaded; matching assets
and matching npm versions are verified and skipped. Conflicting existing bytes
fail rather than being overwritten. Legacy or incomplete releases without a
complete bundle are rebuilt once and checked against any existing assets; if
historical assets differ, retain those assets and release a new version instead.

For local validation without publishing:

```sh
npm test
npm run release:pack -- . vX.Y.Z "$(git rev-parse HEAD)"
npm pack --dry-run --ignore-scripts
```

The packaging command writes ignored `npm/dist/` binaries and `dist/` archives,
verifies all six npm binaries and all four GitHub/npm binary pairs, and never
publishes. `release.sh` and `publish-npm.sh` have been retired.

# comwit CLI

Command-line client for the **comwit.io** cloud platform — create databases, manage
apps, and deploy, against `https://api.cloud.comwit.io`.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/burrr-ai/comwit-cli/main/install.sh | sh
```

This downloads the binary for your OS/arch from the latest [release](https://github.com/burrr-ai/comwit-cli/releases),
verifies its checksum, and installs it to `/usr/local/bin` (or `~/.local/bin`).

Or with Go:

```sh
go install github.com/burrr-ai/comwit-cli/cmd/comwit@latest
```

## Usage

```sh
comwit login --token <cwt_token>     # authenticate (token from the dashboard)
comwit projects list
comwit databases create --project <id> --name <name>
comwit databases list --project <id>
comwit apps ...                      # see `comwit --help`
comwit update                        # self-update to the latest release
comwit version
```

Get a `cwt_` token from the platform dashboard, or use `comwit login` (device flow).

## Releasing

```sh
./release.sh v0.1.0
```

Builds darwin/linux × amd64/arm64, writes `checksums.txt`, and publishes a GitHub Release.

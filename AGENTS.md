# AGENTS.md

This repository is the standalone Go CLI for the comwit-cloud platform. Treat it
as a subproject of `../comwit-cloud`, not as an independent product surface.

## Product Relationship

- `comwit` is the command-line client for `api.cloud.comwit.io`.
- The public platform API is owned by `../comwit-cloud/services/platform-api`.
- Product behavior, route shape, auth flows, and user-facing terminology must
  follow the comwit-cloud docs and contracts.
- Do not invent CLI-only product concepts when the same behavior belongs in the
  platform API or console.

Before changing commands, flags, output shapes, auth behavior, project/database
flows, app deployment flows, or domain workflows, read the relevant comwit-cloud
context first:

- `../comwit-cloud/AGENTS.md`
- `../comwit-cloud/docs/README.md`
- `../comwit-cloud/docs/platform-api-contract.md`
- `../comwit-cloud/docs/control-plane.md`
- `../comwit-cloud/docs/guides/`
- `../comwit-cloud/docs/product/`

When a CLI change exposes new or changed platform behavior, update the
comwit-cloud docs/contracts in the same work, then keep this CLI aligned with
that documented contract.

## Adjacent System Boundaries

Louhi and brrrd are upstream platform planes used through comwit-cloud:

- Louhi database/server behavior lives in `../louhi`.
- brrrd runtime/deployment behavior lives in `../brrrd`.

The CLI should talk to `api.cloud.comwit.io`/platform-api product routes. It
must not call internal Louhi or brrrd management APIs directly unless the user
explicitly asks for a temporary diagnostic tool.

## Development Rules

- Use the existing Go toolchain and keep code under `cmd/comwit`.
- Keep command names, flags, errors, and help text consistent with
  comwit-cloud terminology.
- Prefer explicit HTTP contracts over inferred backend behavior.
- Do not add AWS SDKs, cloud credentials, or direct infrastructure mutations to
  the CLI.
- Do not commit tokens, release credentials, generated archives, or local secret
  files.

## Verification

Use the narrowest verification that matches the change:

```sh
go test ./...
```

For release changes, inspect `release.sh` and verify the generated artifacts
before publishing a GitHub Release.

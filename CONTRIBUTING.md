# Contributing

Thanks for improving tidy-wow.

## Prerequisites

- macOS
- Go 1.26 or later
- GNU Make or the macOS-provided `make`

## Development

Build the CLI with:

```sh
make build
```

Run the required local checks before opening a pull request:

```sh
gofmt -w .
make test
go test -race ./...
```

## Pull Requests

- Create a focused branch and open a pull request against `main`.
- Keep behavioral changes covered by focused tests.
- Do not commit generated archives, local configuration, logs, account names, realm names, character names, or WoW data.
- Do not test destructive restore behavior against a live WoW installation. Use a synthetic tree under `t.TempDir()`.
- Follow the safety and Go conventions in [AGENTS.md](AGENTS.md).

Pull requests require approval from the repository owner and passing CI before merge. Repository administrators can bypass these rules when necessary.

## Versioning

Releases use Semantic Versioning and are automated from `main`:

- Production changes increment the patch version.
- Any release containing an added or modified `*_test.go` file increments the minor version instead.
- Major releases are produced only by the manually dispatched major-release workflow, which verifies administrator permission.

Do not manually create release tags.

## Security

Do not disclose vulnerabilities in commits, pull requests, or public discussion. Follow [SECURITY.md](SECURITY.md).

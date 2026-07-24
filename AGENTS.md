# Agent Guidelines

## Project Context

This repository contains `tidy-wow`, a Go CLI for macOS. Product scope, accepted behavior, archive contents, and implementation milestones are defined in [PLAN.md](PLAN.md). Treat that document as the source of truth instead of duplicating product decisions elsewhere.

## Language

- Write code, comments, documentation, fixtures, command help, errors, commits, and other persistent artifacts in English.
- Keep identifiers and user-facing terminology consistent with World of Warcraft product IDs and macOS conventions.

## Engineering Principles

- Prefer the smallest correct implementation and Go's standard library where practical.
- Keep domain logic independent from command parsing, prompts, the real filesystem, wall-clock time, and process execution so it can be tested safely.
- Model filesystem paths with `path/filepath`; use `path` only for normalized ZIP entry names.
- Never rely on the current working directory for configuration, installation discovery, backups, restores, or scheduling.
- Return contextual errors and preserve underlying causes with `%w`. Do not log and return the same error at lower layers.
- Avoid global mutable state. Pass dependencies explicitly when code needs a filesystem boundary, clock, executable path, or command runner.
- Do not add compatibility behavior for archive or configuration versions until a released format requires it.

## Safety Rules

- Treat archives and WoW directories as untrusted input.
- Validate paths before reading, archiving, extracting, deleting, or replacing files.
- Do not follow symbolic links during backup or restore unless a later documented design explicitly supports them.
- Use staging files or directories and atomic renames for persistent writes whenever the filesystem permits it.
- Never run destructive restore tests against `/Applications/World of Warcraft` or another live installation.
- Keep restore operations constrained to the explicitly managed path set.
- Do not invoke a shell for LaunchAgent commands or path handling. Pass argument arrays directly to processes.

## Go Conventions

- Keep packages cohesive and avoid package names such as `util`, `common`, or `helpers`.
- Accept `context.Context` for operations that may scan files, create archives, restore data, or execute subprocesses.
- Use table-driven tests where they improve clarity, but keep simple cases simple.
- Use `t.TempDir()` and synthetic WoW trees for filesystem tests.
- Inject time in tests rather than weakening filename assertions.
- Preserve deterministic ordering when walking files, writing manifests, presenting flavors, or applying retention.
- Keep exported APIs minimal and document every exported identifier.

## Verification

Run these checks before considering a change complete once the Go module exists:

```sh
gofmt -w .
go vet ./...
go test ./...
```

Add focused regression tests for behavioral fixes. Security-sensitive archive and restore changes require negative tests, not only happy-path coverage.

## Change Discipline

- Update `PLAN.md` only when an accepted product decision or implementation sequence changes.
- Keep user documentation focused on operating the CLI; keep contributor rules here.
- Do not commit generated archives, local configuration, logs, WoW data, account names, realm names, character names, or other personal data.
- Do not commit, tag, publish, or modify release state unless the user explicitly requests it.

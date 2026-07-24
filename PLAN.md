# tidy-wow Implementation Plan

## Goal

Build a standalone macOS CLI in Go that backs up and restores portable World of Warcraft add-on profiles. Each ZIP archive targets one WoW flavor and excludes machine-specific game settings.

## Validated Decisions

- Support macOS only in the first release, for both Apple Silicon and Intel.
- Discover WoW installations automatically, while allowing an explicit path override.
- Discover flavors from the installation instead of maintaining a fixed list. Accept product IDs and convenient aliases such as `era`, `anniversary`, and `retail`.
- Produce one ZIP archive per flavor.
- Continue a backup without warning when WoW is running.
- Restore to the same WoW account, realms, and characters; no identity remapping is required.
- Require the user to choose the backup destination during initialization.
- Keep three automatic backups per flavor by default. Make the limit configurable.
- Support globally configured glob patterns that exclude matching add-on directories from backups and restores. Exclude `AI_VoiceOverData_Vanilla` by default because its MP3 data is already compressed and dominates archive size.
- Never automatically delete manual backups or pre-restore safety backups.
- Require an explicit daily or weekly calendar when installing the scheduled job.
- Let the LaunchAgent reference the executable by its absolute path. Moving the executable requires reinstalling the job.

## Portable Profile

Include:

- `Interface/AddOns/**`
- Every `SavedVariables/**` directory, including `.bak` files
- `AddOns.txt`
- `macros-cache.txt`
- `bindings-cache.wtf`
- `chat-cache.txt`
- Account-level and character-level `config-cache.wtf`

Exclude:

- `WTF/Config.wtf`, which contains graphics, audio, hardware, and other machine-level settings
- `layout-local.txt` and `edit-mode-cache-*`
- `cache.md5`, TTS caches, and `.old` files
- Game caches, logs, errors, Curse metadata, and custom fonts

ElvUI does not depend on Blizzard's excluded layout files for its profile. Its positions, action bars, unit frames, and related settings are stored in `SavedVariables/ElvUI.lua` through `ElvDB`, `ElvPrivateDB`, and `ElvCharacterDB`. The exclusion can still omit positions for Blizzard windows or add-ons that do not persist their own layout correctly.

## Archive Format

Create archives atomically, using a temporary file in the destination directory followed by a rename. Use a filename containing the flavor, UTC timestamp, and backup kind.

Each archive contains a versioned `manifest.json` with:

- Archive schema version
- tidy-wow version
- Creation time
- Backup kind: `manual`, `automatic`, or `pre-restore`
- WoW product/flavor ID and build version
- Relative source paths
- File sizes and SHA-256 checksums

All archive paths must be relative to the flavor directory and use `/` separators.

## CLI Surface

Initial command set:

```text
tidy-wow init
tidy-wow flavor list
tidy-wow backup --flavor <flavor>
tidy-wow backup --all
tidy-wow restore <archive.zip>
tidy-wow schedule install --daily-at <HH:MM>
tidy-wow schedule install --weekly-on <day> --at <HH:MM>
tidy-wow schedule status
tidy-wow schedule remove
```

Internal flags needed by scheduled execution may be public but should remain clearly documented, for example `backup --all --automatic`.

## Configuration

Store configuration at:

```text
~/Library/Application Support/tidy-wow/config.toml
```

The initial schema should contain only durable user choices:

- WoW installation path
- Backup destination
- Automatic retention count, defaulting to `3`
- Add-on exclusion patterns, initially including `AI_VoiceOverData_Vanilla`

Schedule details live in the generated LaunchAgent and can be reported by `schedule status`. Runtime flags override configuration without silently rewriting it.

`tidy-wow init` will:

1. Detect and validate the installation.
2. Show the discovered flavors.
3. Require a backup destination.
4. Ask for or accept the retention count.
5. Scan add-on directories across discovered flavors and offer a multi-selection with recursive sizes to define exclusions.
6. Write the configuration atomically with user-only write permissions.

## Installation And Flavor Discovery

Discovery order:

1. Explicit CLI path.
2. Configured installation path.
3. Battle.net metadata and its configured default installation location.
4. `/Applications/World of Warcraft` and the user's `Applications` directory.
5. A bounded macOS metadata search as a final fallback.

Validate an installation using `.build.info` and flavor directories. Read `.flavor.info` where available and correlate it with `.build.info`; do not infer support solely from hard-coded directory names. A flavor is eligible for backup only when it has portable profile content, such as `Interface/AddOns` or `WTF`.

## Backup Flow

1. Load and validate configuration and command flags.
2. Resolve the selected flavor or all eligible flavors.
3. Enumerate files from explicit inclusion rules, then apply exclusions.
4. Reject unsupported file types and unsafe symbolic links.
5. Write file entries and checksums to a temporary ZIP.
6. Add the completed manifest.
7. Flush, close, and atomically rename the archive.
8. For automatic backups only, delete older automatic archives beyond the configured limit for that flavor.

A failure for one flavor during `--all` must be reported without hiding successful archives for other flavors, and the command must return a non-zero status.

Excluded add-ons are not written to archives. During restore they are left untouched in the target installation, so changing the exclusion list cannot delete an add-on that tidy-wow intentionally does not manage.

## Restore Flow

Restoration is destructive and must refuse to proceed while WoW is running unless a future explicit force option is introduced.

1. Open the archive and validate the manifest schema and flavor.
2. Validate every ZIP path against absolute paths, traversal, duplicates, links, and unsupported entry types.
3. Verify every declared size and checksum before changing the installation.
4. Confirm that the target installation contains the matching flavor.
5. Create a `pre-restore` archive from the currently managed paths.
6. Extract into a staging directory on the target filesystem.
7. Replace only paths managed by tidy-wow, removing stale managed content where replacement semantics require it.
8. Roll back from the safety archive if replacement fails.

Pre-restore archives are never subject to automatic retention.

## Scheduling

Use a per-user LaunchAgent under `~/Library/LaunchAgents/` and `launchctl` rather than cron. Generate the plist with Go's XML support and no shell interpolation.

The installer must:

- Require either a daily time or a weekday plus time.
- Reference the current executable by absolute path.
- Run a global automatic backup, producing one archive per eligible flavor.
- Direct stdout and stderr to logs under `~/Library/Logs/tidy-wow/`.
- Install or replace the agent idempotently.
- Validate that configuration is complete before loading it.

## Delivery Sequence

1. Bootstrap the Go module, CLI parser, version metadata, and test fixtures.
2. Implement configuration loading, validation, initialization, and atomic persistence.
3. Implement installation and flavor discovery.
4. Implement portable file selection and ZIP manifest generation.
5. Implement single-flavor and global backup commands.
6. Implement automatic retention by flavor.
7. Implement archive validation, safety backup, restore, and rollback.
8. Implement LaunchAgent generation and lifecycle commands.
9. Add end-to-end tests, user documentation, and release automation.

## Verification

Automated coverage must include:

- Installation and flavor discovery with incomplete and unusual directory layouts
- Inclusion and exclusion rules at all `WTF` nesting levels
- Unicode account, realm, and character paths
- Atomic archive and configuration writes
- Manifest generation and checksum validation
- Empty, corrupt, truncated, and malicious ZIP archives
- Zip Slip, absolute paths, duplicate paths, and unsafe links
- Restore replacement and rollback behavior
- Automatic retention isolated by flavor and backup kind
- LaunchAgent calendar generation and idempotent installation

Before release, test backup and restore against synthetic fixtures and perform a manual round trip using real WoW data copied to a temporary installation tree. Never use the live installation as a destructive test target.

## Release

- Build signed or checksum-verifiable binaries for `darwin/arm64` and `darwin/amd64`.
- Publish SHA-256 checksums with each release.
- Run formatting, static analysis, tests, and cross-compilation in CI.
- Document installation, initialization, backup, restore, scheduling, retention, and the exact portable-profile boundary.

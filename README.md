# tidy-wow

`tidy-wow` is a standalone macOS CLI that backs up and restores portable World of Warcraft add-on profiles. It creates one verifiable ZIP archive per game flavor and deliberately leaves machine-specific game settings behind.

## Status

The CLI currently supports installation and flavor discovery, configuration, manual and automatic backups, retention, restore safety backups, and per-user LaunchAgent scheduling. macOS on Apple Silicon and Intel is supported.

## Build

Go 1.26 or later and `make` are required to build from source.

```sh
make build
```

The resulting binary is `bin/tidy-wow` and has no runtime dependencies.

Run the quality checks with:

```sh
make test
```

Remove local build artifacts with:

```sh
make clean
```

## Initialize

Run initialization once and choose a backup directory:

```sh
./tidy-wow init
```

Non-interactive initialization is also available:

```sh
./tidy-wow init \
  --wow-path "/Applications/World of Warcraft" \
  --backup-dir "$HOME/Documents/WoW Backups" \
  --retention 3
```

Configuration is stored at `~/Library/Application Support/tidy-wow/config.toml` by default.

Initialization scans add-on directories across discovered flavors, displays their recursive sizes, and accepts a comma-separated multi-selection of add-ons to exclude. Press Enter to retain the current selection. `AI_VoiceOverData_Vanilla` is excluded by default: its audio consists of already-compressed MP3 files and would otherwise add about 1.3 GiB to a backup.

## Back Up

List flavors that currently contain portable profile data:

```sh
./tidy-wow flavor list
```

Back up one flavor using an alias, directory, or product ID:

```sh
./tidy-wow backup --flavor era
./tidy-wow backup --flavor wow_anniversary
```

Back up every eligible installed flavor:

```sh
./tidy-wow backup --all
```

Manual backups are never deleted automatically. Scheduled backups use the configured retention count independently for each flavor.

## Exclusions

Exclusions are glob patterns matched against an add-on directory name. They are global across flavors. An excluded add-on is neither archived nor removed during restore.

```sh
# Show configured patterns.
./bin/tidy-wow exclusion list

# Exclude one add-on exactly, or a family of add-ons with a glob.
./bin/tidy-wow exclusion add AI_VoiceOverData_Vanilla
./bin/tidy-wow exclusion add "AI_VoiceOverData_*"

# Resume managing a previously excluded add-on.
./bin/tidy-wow exclusion remove AI_VoiceOverData_Vanilla
```

Patterns match one directory name only. Path separators are rejected, so an exclusion cannot escape `Interface/AddOns`.

## Restore

Close World of Warcraft, then restore an archive:

```sh
./tidy-wow restore "/path/to/tidy-wow-wow_classic_era-...-manual.zip"
```

Before replacing any managed data, tidy-wow validates every archive path, size, and SHA-256 checksum and creates a `pre-restore` safety archive. Safety archives are never subject to automatic retention.

Restore replaces only the managed portable profile. Machine-level and excluded files such as `WTF/Config.wtf` and `layout-local.txt` remain untouched.

## Schedule

Install a daily per-user LaunchAgent:

```sh
./tidy-wow schedule install --daily-at 03:00
```

Or install a weekly one:

```sh
./tidy-wow schedule install --weekly-on monday --at 03:00
```

Inspect or remove it:

```sh
./tidy-wow schedule status
./tidy-wow schedule remove
```

The LaunchAgent refers to the binary by absolute path. Reinstall the schedule after moving the executable. Logs are written under `~/Library/Logs/tidy-wow/`.

## Archive Contents

Archives include installed add-ons that are not excluded, SavedVariables, add-on enablement, macros, key bindings, chat settings, and portable account or character configuration caches. They exclude global graphics, audio, and hardware settings, Blizzard local layouts, game caches, logs, Curse metadata, custom fonts, and configured add-on exclusions.

See [PLAN.md](PLAN.md) for the precise product boundary and implementation decisions.

## Development

```sh
make test
```

Restore tests use synthetic temporary WoW installations and must never target a live installation.

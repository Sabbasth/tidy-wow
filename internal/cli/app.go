// Package cli implements tidy-wow's command-line interface.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sabbasth/tidy-wow/internal/backup"
	"github.com/sabbasth/tidy-wow/internal/config"
	"github.com/sabbasth/tidy-wow/internal/restore"
	"github.com/sabbasth/tidy-wow/internal/schedule"
	"github.com/sabbasth/tidy-wow/internal/wow"
)

// App is a testable tidy-wow command-line application.
type App struct {
	version string
	in      io.Reader
	out     io.Writer
	errOut  io.Writer
}

// New constructs an application with explicit standard streams.
func New(version string, in io.Reader, out, errOut io.Writer) *App {
	return &App{version: version, in: in, out: out, errOut: errOut}
}

// Run executes one command.
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		a.printUsage()
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		a.printUsage()
		return nil
	case "version", "--version":
		fmt.Fprintln(a.out, a.version)
		return nil
	case "init":
		return a.runInit(ctx, args[1:])
	case "flavor":
		return a.runFlavor(ctx, args[1:])
	case "backup":
		return a.runBackup(ctx, args[1:])
	case "restore":
		return a.runRestore(ctx, args[1:])
	case "schedule":
		return a.runSchedule(ctx, args[1:])
	case "exclusion":
		return a.runExclusion(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (a *App) runExclusion(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tidy-wow exclusion <list|add|remove>")
	}
	set := flag.NewFlagSet("exclusion "+args[0], flag.ContinueOnError)
	set.SetOutput(a.errOut)
	configPath := set.String("config", "", "configuration file path")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	path := *configPath
	var err error
	if path == "" {
		path, err = config.DefaultPath()
		if err != nil {
			return err
		}
	} else {
		path, err = absolutePath(path)
		if err != nil {
			return fmt.Errorf("resolve configuration path: %w", err)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if set.NArg() != 0 {
			return errors.New("exclusion list accepts no patterns")
		}
		for _, pattern := range cfg.AddonExclusions {
			fmt.Fprintln(a.out, pattern)
		}
		return nil
	case "add":
		if set.NArg() == 0 {
			return errors.New("usage: tidy-wow exclusion add <pattern> [<pattern>...]")
		}
		patterns, err := config.NormalizeAddonExclusions(append(cfg.AddonExclusions, set.Args()...))
		if err != nil {
			return err
		}
		cfg.AddonExclusions = patterns
	case "remove":
		if set.NArg() == 0 {
			return errors.New("usage: tidy-wow exclusion remove <pattern> [<pattern>...]")
		}
		remove := make(map[string]bool, set.NArg())
		for _, pattern := range set.Args() {
			remove[pattern] = true
		}
		patterns := make([]string, 0, len(cfg.AddonExclusions))
		for _, pattern := range cfg.AddonExclusions {
			if !remove[pattern] {
				patterns = append(patterns, pattern)
			}
		}
		if len(patterns) == len(cfg.AddonExclusions) {
			return errors.New("none of the requested exclusion patterns are configured")
		}
		cfg.AddonExclusions = patterns
	default:
		return fmt.Errorf("unknown exclusion command %q", args[0])
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	for _, pattern := range cfg.AddonExclusions {
		fmt.Fprintln(a.out, pattern)
	}
	return nil
}

func (a *App) runSchedule(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tidy-wow schedule <install|status|remove>")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("locate home directory: %w", err)
	}
	manager := schedule.Manager{
		Runner: schedule.ExecRunner{},
		UID:    os.Getuid(),
		Paths:  schedule.DefaultPaths(home),
	}
	switch args[0] {
	case "install":
		set := flag.NewFlagSet("schedule install", flag.ContinueOnError)
		set.SetOutput(a.errOut)
		dailyAt := set.String("daily-at", "", "daily execution time in HH:MM")
		weeklyOn := set.String("weekly-on", "", "weekly execution weekday")
		at := set.String("at", "", "weekly execution time in HH:MM")
		configPath := set.String("config", "", "configuration file path")
		if err := set.Parse(args[1:]); err != nil {
			return err
		}
		if set.NArg() != 0 {
			return errors.New("schedule install accepts flags only")
		}
		calendar, err := parseScheduleCalendar(*dailyAt, *weeklyOn, *at)
		if err != nil {
			return err
		}
		path := *configPath
		if path == "" {
			path, err = config.DefaultPath()
			if err != nil {
				return err
			}
		} else {
			path, err = absolutePath(path)
			if err != nil {
				return fmt.Errorf("resolve configuration path: %w", err)
			}
		}
		if _, err := config.Load(path); err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate tidy-wow executable: %w", err)
		}
		executable, err = filepath.EvalSymlinks(executable)
		if err != nil {
			return fmt.Errorf("resolve tidy-wow executable: %w", err)
		}
		if err := manager.Install(ctx, executable, path, calendar); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "LaunchAgent installed at %s\n", manager.Paths.Plist)
		return nil
	case "status":
		if len(args) != 1 {
			return errors.New("schedule status accepts no arguments")
		}
		status, err := manager.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Fprint(a.out, status)
		return nil
	case "remove":
		if len(args) != 1 {
			return errors.New("schedule remove accepts no arguments")
		}
		if err := manager.Remove(ctx); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "LaunchAgent removed")
		return nil
	default:
		return fmt.Errorf("unknown schedule command %q", args[0])
	}
}

func parseScheduleCalendar(dailyAt, weeklyOn, at string) (schedule.Calendar, error) {
	daily := dailyAt != ""
	weekly := weeklyOn != "" || at != ""
	if daily == weekly {
		return schedule.Calendar{}, errors.New("choose either --daily-at or both --weekly-on and --at")
	}
	if daily {
		hour, minute, err := schedule.ParseTime(dailyAt)
		return schedule.Calendar{Hour: hour, Minute: minute}, err
	}
	if weeklyOn == "" || at == "" {
		return schedule.Calendar{}, errors.New("weekly scheduling requires both --weekly-on and --at")
	}
	hour, minute, err := schedule.ParseTime(at)
	if err != nil {
		return schedule.Calendar{}, err
	}
	weekday, err := schedule.ParseWeekday(weeklyOn)
	if err != nil {
		return schedule.Calendar{}, err
	}
	return schedule.Calendar{Hour: hour, Minute: minute, Weekday: &weekday}, nil
}

func (a *App) runRestore(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("restore", flag.ContinueOnError)
	set.SetOutput(a.errOut)
	configPath := set.String("config", "", "configuration file path")
	wowPath := set.String("wow-path", "", "World of Warcraft installation path")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 1 {
		return errors.New("usage: tidy-wow restore <archive.zip> [--config path] [--wow-path path]")
	}
	archivePath, err := absolutePath(set.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve archive path: %w", err)
	}
	path := *configPath
	if path == "" {
		path, err = config.DefaultPath()
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	configuredWoWPath := cfg.WoWPath
	if *wowPath != "" {
		configuredWoWPath = ""
	}
	installation, err := wow.Discover(ctx, wow.DiscoverOptions{
		ExplicitPath:   *wowPath,
		ConfiguredPath: configuredWoWPath,
	})
	if err != nil {
		return err
	}
	running, err := wow.IsRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		return errors.New("World of Warcraft is running; close the game before restoring")
	}
	r := restore.NewWithExclusions(backup.NewCreator(a.version, time.Now), cfg.AddonExclusions)
	result, err := r.Restore(ctx, archivePath, installation, cfg.BackupDirectory)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Restored %s\nSafety backup: %s\n", result.ProductID, result.SafetyArchive)
	return nil
}

func (a *App) runBackup(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("backup", flag.ContinueOnError)
	set.SetOutput(a.errOut)
	flavorName := set.String("flavor", "", "flavor product ID, directory, or alias")
	all := set.Bool("all", false, "back up every eligible flavor")
	automatic := set.Bool("automatic", false, "mark backup as automatic and apply retention")
	configPath := set.String("config", "", "configuration file path")
	wowPath := set.String("wow-path", "", "World of Warcraft installation path")
	backupDirectory := set.String("backup-dir", "", "backup destination directory")
	retention := set.Int("retention", 0, "automatic backups to keep per flavor")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("backup accepts flags only")
	}
	if (*flavorName == "") == !*all {
		return errors.New("exactly one of --flavor or --all is required")
	}

	path := *configPath
	var err error
	if path == "" {
		path, err = config.DefaultPath()
		if err != nil {
			return err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	configuredWoWPath := cfg.WoWPath
	if *wowPath != "" {
		configuredWoWPath = ""
	}
	installation, err := wow.Discover(ctx, wow.DiscoverOptions{
		ExplicitPath:   *wowPath,
		ConfiguredPath: configuredWoWPath,
	})
	if err != nil {
		return err
	}
	destination := cfg.BackupDirectory
	if *backupDirectory != "" {
		destination, err = absolutePath(*backupDirectory)
		if err != nil {
			return fmt.Errorf("resolve backup destination: %w", err)
		}
	}
	keep := cfg.Retention
	if *retention != 0 {
		keep = *retention
	}
	if keep < 1 {
		return errors.New("automatic retention must be at least 1")
	}

	flavors := installation.Flavors
	if !*all {
		flavor, resolveErr := installation.ResolveFlavor(*flavorName)
		if resolveErr != nil {
			return resolveErr
		}
		flavors = []wow.Flavor{flavor}
	}
	kind := backup.Manual
	if *automatic {
		kind = backup.Automatic
	}
	creator := backup.NewCreator(a.version, nil)
	var operationErrors []error
	for _, flavor := range flavors {
		archivePath, createErr := creator.Create(ctx, backup.Request{
			InstallationPath: installation.Path,
			Flavor:           flavor,
			Destination:      destination,
			Kind:             kind,
			AddonExclusions:  cfg.AddonExclusions,
		})
		if createErr != nil {
			operationErrors = append(operationErrors, fmt.Errorf("back up %s: %w", flavor.ProductID, createErr))
			continue
		}
		fmt.Fprintln(a.out, archivePath)
		if *automatic {
			if retentionErr := backup.ApplyRetention(destination, flavor.ProductID, keep); retentionErr != nil {
				operationErrors = append(operationErrors, retentionErr)
			}
		}
	}
	return errors.Join(operationErrors...)
}

func (a *App) runInit(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("init", flag.ContinueOnError)
	set.SetOutput(a.errOut)
	wowPath := set.String("wow-path", "", "World of Warcraft installation path")
	backupDirectory := set.String("backup-dir", "", "backup destination directory")
	retention := set.Int("retention", config.DefaultRetention(), "automatic backups to keep per flavor")
	configPath := set.String("config", "", "configuration file path")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("init accepts flags only")
	}

	installation, err := wow.Discover(ctx, wow.DiscoverOptions{ExplicitPath: *wowPath})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "WoW installation: %s\n", installation.Path)
	fmt.Fprintln(a.out, "Discovered flavors:")
	for _, flavor := range installation.Flavors {
		fmt.Fprintf(a.out, "  %s (%s)\n", flavor.ProductID, flavor.Version)
	}
	path := *configPath
	if path == "" {
		path, err = config.DefaultPath()
		if err != nil {
			return err
		}
	} else {
		path, err = absolutePath(path)
		if err != nil {
			return fmt.Errorf("resolve configuration path: %w", err)
		}
	}
	exclusions := config.DefaultAddonExclusions()
	if existing, loadErr := config.Load(path); loadErr == nil {
		exclusions = existing.AddonExclusions
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	}

	destination := strings.TrimSpace(*backupDirectory)
	reader := bufio.NewReader(a.in)
	if destination == "" {
		fmt.Fprint(a.out, "Backup destination: ")
		entered, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read backup destination: %w", readErr)
		}
		destination = strings.TrimSpace(entered)
	}
	if destination == "" {
		return errors.New("backup destination is required")
	}
	destination, err = absolutePath(destination)
	if err != nil {
		return fmt.Errorf("resolve backup destination: %w", err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("create backup destination %q: %w", destination, err)
	}
	addons, err := scanAddonOptions(ctx, installation)
	if err != nil {
		return err
	}
	exclusions, err = selectAddonExclusions(reader, a.out, addons, exclusions)
	if err != nil {
		return err
	}
	cfg := config.Config{
		WoWPath:         installation.Path,
		BackupDirectory: destination,
		Retention:       *retention,
		AddonExclusions: exclusions,
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Configuration written to %s\n", path)
	return nil
}

type addonOption struct {
	Name string
	Size int64
}

func scanAddonOptions(ctx context.Context, installation wow.Installation) ([]addonOption, error) {
	sizes := make(map[string]int64)
	for _, flavor := range installation.Flavors {
		addons, err := wow.ScanAddons(ctx, installation.Path, flavor)
		if err != nil {
			return nil, err
		}
		for _, addon := range addons {
			sizes[addon.Name] += addon.Size
		}
	}
	options := make([]addonOption, 0, len(sizes))
	for name, size := range sizes {
		options = append(options, addonOption{Name: name, Size: size})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Name < options[j].Name })
	return options, nil
}

func selectAddonExclusions(reader *bufio.Reader, output io.Writer, addons []addonOption, current []string) ([]string, error) {
	if len(addons) == 0 {
		return config.NormalizeAddonExclusions(current)
	}
	selected := make(map[int]bool, len(addons))
	matchedPatterns := make(map[string]bool, len(current))
	for index, addon := range addons {
		for _, pattern := range current {
			matched, _ := filepath.Match(pattern, addon.Name)
			if matched {
				selected[index+1] = true
				matchedPatterns[pattern] = true
				break
			}
		}
	}
	fmt.Fprintln(output, "Add-ons available for exclusion:")
	for index, addon := range addons {
		marker := " "
		if selected[index+1] {
			marker = "x"
		}
		fmt.Fprintf(output, "  [%s] %d. %s (%s)\n", marker, index+1, addon.Name, formatByteSize(addon.Size))
	}
	fmt.Fprint(output, "Enter comma-separated add-on numbers to exclude, or press Enter to keep the current selection: ")
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read add-on exclusions: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return config.NormalizeAddonExclusions(current)
	}
	selected = make(map[int]bool)
	for _, part := range strings.Split(line, ",") {
		index, parseErr := strconv.Atoi(strings.TrimSpace(part))
		if parseErr != nil || index < 1 || index > len(addons) {
			return nil, fmt.Errorf("invalid add-on selection %q", strings.TrimSpace(part))
		}
		selected[index] = true
	}
	patterns := make([]string, 0, len(selected)+len(current))
	for _, pattern := range current {
		if !matchedPatterns[pattern] {
			patterns = append(patterns, pattern)
		}
	}
	for index, addon := range addons {
		if selected[index+1] {
			patterns = append(patterns, addon.Name)
		}
	}
	return config.NormalizeAddonExclusions(patterns)
}

func formatByteSize(size int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", size, units[unit])
	}
	return fmt.Sprintf("%.2f %s", value, units[unit])
}

func (a *App) runFlavor(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: tidy-wow flavor list [--config path] [--wow-path path]")
	}
	set := flag.NewFlagSet("flavor list", flag.ContinueOnError)
	set.SetOutput(a.errOut)
	configPath := set.String("config", "", "configuration file path")
	wowPath := set.String("wow-path", "", "World of Warcraft installation path")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("flavor list accepts flags only")
	}

	configuredPath := ""
	if *configPath == "" {
		defaultPath, err := config.DefaultPath()
		if err == nil {
			if cfg, loadErr := config.Load(defaultPath); loadErr == nil {
				configuredPath = cfg.WoWPath
			}
		}
	} else {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		configuredPath = cfg.WoWPath
	}

	installation, err := wow.Discover(ctx, wow.DiscoverOptions{
		ExplicitPath:   *wowPath,
		ConfiguredPath: configuredPath,
	})
	if err != nil {
		return err
	}
	for _, flavor := range installation.Flavors {
		fmt.Fprintf(a.out, "%s\t%s\t%s\n", flavor.ProductID, flavor.Version, flavor.Directory)
	}
	return nil
}

func (a *App) printUsage() {
	fmt.Fprintln(a.out, `Usage: tidy-wow <command>

Commands:
  init          Detect WoW and create the configuration
  flavor list   List installed flavors with portable profile data
  backup        Create a portable profile archive
  restore       Restore an archive after creating a safety backup
  schedule      Install, inspect, or remove the automatic backup job
  exclusion     List, add, or remove add-on exclusion patterns
  version       Print the tidy-wow version
  help          Show this help`)
}

func absolutePath(value string) (string, error) {
	// Shells consume escaped spaces before passing flag values, but interactive
	// input is read verbatim. Accept the common pasted POSIX path form too.
	value = strings.ReplaceAll(value, `\ `, " ")
	if strings.HasPrefix(value, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~"+string(filepath.Separator)))
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

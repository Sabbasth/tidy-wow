package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sabbasth/tidy-wow/internal/config"
)

func TestInitWritesConfiguration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	buildInfo := "Region!STRING:0|Version!STRING:0|Product!STRING:0\neu|1.15.9|wow_classic_era\n"
	writeTestFile(t, filepath.Join(root, ".build.info"), buildInfo)
	writeTestFile(t, filepath.Join(root, "_classic_era_", ".flavor.info"), "Product Flavor!STRING:0\nwow_classic_era\n")
	if err := os.MkdirAll(filepath.Join(root, "_classic_era_", "WTF"), 0o700); err != nil {
		t.Fatal(err)
	}

	backupDirectory := filepath.Join(t.TempDir(), "backups")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	var output bytes.Buffer
	app := New("test", strings.NewReader(""), &output, &output)
	err := app.Run(context.Background(), []string{
		"init",
		"--wow-path", root,
		"--backup-dir", backupDirectory,
		"--retention", "5",
		"--config", configPath,
	})
	if err != nil {
		t.Fatalf("Run(init) error = %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if cfg.WoWPath != root || cfg.BackupDirectory != backupDirectory || cfg.Retention != 5 {
		t.Errorf("configuration = %#v", cfg)
	}
	if !strings.Contains(output.String(), "wow_classic_era") {
		t.Errorf("output does not list flavor: %s", output.String())
	}
}

func TestParseScheduleCalendar(t *testing.T) {
	t.Parallel()

	daily, err := parseScheduleCalendar("03:00", "", "")
	if err != nil || daily.Hour != 3 || daily.Minute != 0 || daily.Weekday != nil {
		t.Fatalf("daily calendar = %#v, %v", daily, err)
	}
	weekly, err := parseScheduleCalendar("", "monday", "04:30")
	if err != nil || weekly.Hour != 4 || weekly.Minute != 30 || weekly.Weekday == nil || *weekly.Weekday != 2 {
		t.Fatalf("weekly calendar = %#v, %v", weekly, err)
	}
	for _, args := range [][3]string{
		{"", "", ""},
		{"03:00", "monday", "04:00"},
		{"", "monday", ""},
		{"", "", "04:00"},
	} {
		if _, err := parseScheduleCalendar(args[0], args[1], args[2]); err == nil {
			t.Errorf("parseScheduleCalendar(%q, %q, %q) returned nil error", args[0], args[1], args[2])
		}
	}
}

func TestAbsolutePathUnescapesInteractiveSpaces(t *testing.T) {
	t.Parallel()

	got, err := absolutePath(`~/Google\ Drive/Mon\ Drive/WoW`)
	if err != nil {
		t.Fatalf("absolutePath() error = %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Google Drive", "Mon Drive", "WoW")
	if got != want {
		t.Errorf("absolutePath() = %q, want %q", got, want)
	}
}

func TestInitSelectsAddonExclusions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".build.info"), "Region!STRING:0|Version!STRING:0|Product!STRING:0\neu|1.15.9|wow_classic_era\n")
	writeTestFile(t, filepath.Join(root, "_classic_era_", ".flavor.info"), "Product Flavor!STRING:0\nwow_classic_era\n")
	writeTestFile(t, filepath.Join(root, "_classic_era_", "Interface", "AddOns", "AI_VoiceOverData_Vanilla", "audio.mp3"), "audio")
	writeTestFile(t, filepath.Join(root, "_classic_era_", "Interface", "AddOns", "Questie", "Questie.lua"), "questie")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	app := New("test", strings.NewReader("2\n"), io.Discard, io.Discard)
	if err := app.Run(context.Background(), []string{"init", "--wow-path", root, "--backup-dir", filepath.Join(t.TempDir(), "backups"), "--config", configPath}); err != nil {
		t.Fatalf("Run(init) error = %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.AddonExclusions, []string{"Questie"}) {
		t.Errorf("AddonExclusions = %v, want [Questie]", cfg.AddonExclusions)
	}
}

func TestExclusionCommands(t *testing.T) {
	t.Parallel()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(configPath, config.Config{WoWPath: "/Applications/World of Warcraft", BackupDirectory: "/tmp/backups", Retention: 3, AddonExclusions: []string{"AI_VoiceOverData_Vanilla"}}); err != nil {
		t.Fatal(err)
	}
	app := New("test", strings.NewReader(""), io.Discard, io.Discard)
	if err := app.Run(context.Background(), []string{"exclusion", "add", "--config", configPath, "Questie*"}); err != nil {
		t.Fatalf("Run(exclusion add) error = %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.AddonExclusions, []string{"AI_VoiceOverData_Vanilla", "Questie*"}) {
		t.Errorf("AddonExclusions after add = %v", cfg.AddonExclusions)
	}
	if err := app.Run(context.Background(), []string{"exclusion", "remove", "--config", configPath, "AI_VoiceOverData_Vanilla"}); err != nil {
		t.Fatalf("Run(exclusion remove) error = %v", err)
	}
	cfg, err = config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.AddonExclusions, []string{"Questie*"}) {
		t.Errorf("AddonExclusions after remove = %v", cfg.AddonExclusions)
	}
}

func TestSelectAddonExclusionsPreservesUnmatchedPatterns(t *testing.T) {
	t.Parallel()
	patterns, err := selectAddonExclusions(bufio.NewReader(strings.NewReader("1\n")), io.Discard, []addonOption{{Name: "Questie", Size: 1}, {Name: "WeakAuras", Size: 2}}, []string{"AI_VoiceOverData_*", "WeakAuras"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(patterns, []string{"AI_VoiceOverData_*", "Questie"}) {
		t.Errorf("selectAddonExclusions() = %v", patterns)
	}
}

func writeTestFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

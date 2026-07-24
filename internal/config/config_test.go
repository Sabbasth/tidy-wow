package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	filePath := filepath.Join(root, "settings", "config.toml")
	wowPath := filepath.Join(root, "World of Warcraft")
	backupPath := filepath.Join(root, "Backups with spaces")
	want := Config{WoWPath: wowPath, BackupDirectory: backupPath, Retention: 7, AddonExclusions: DefaultAddonExclusions()}

	if err := Save(filePath, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(filePath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}

	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Errorf("configuration mode = %o, want 600", gotMode)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(filePath), ".config-*.tmp")); err != nil {
		t.Fatalf("Glob() error = %v", err)
	} else if len(matches) != 0 {
		t.Errorf("temporary files remain: %v", matches)
	}
}

func TestLoadAssignsDefaultExclusionsToExistingConfiguration(t *testing.T) {
	t.Parallel()

	filePath := filepath.Join(t.TempDir(), "config.toml")
	content := "wow_path = \"/wow\"\nbackup_directory = \"/backups\"\nautomatic_retention = 3\n"
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.AddonExclusions, DefaultAddonExclusions()) {
		t.Errorf("AddonExclusions = %v, want %v", cfg.AddonExclusions, DefaultAddonExclusions())
	}
}

func TestNormalizeAddonExclusions(t *testing.T) {
	t.Parallel()

	got, err := NormalizeAddonExclusions([]string{"Questie*", "AI_VoiceOverData_Vanilla", "Questie*"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"AI_VoiceOverData_Vanilla", "Questie*"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NormalizeAddonExclusions() = %v, want %v", got, want)
	}
	for _, pattern := range []string{"", "../outside", "Interface/AddOns", "["} {
		if _, err := NormalizeAddonExclusions([]string{pattern}); err == nil {
			t.Errorf("NormalizeAddonExclusions(%q) returned nil error", pattern)
		}
	}
}

func TestLoadRejectsUnknownAndDuplicateKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "unknown",
			content: "wow_path = \"/wow\"\nbackup_directory = \"/backups\"\nautomatic_retention = 3\nextra = true\n",
			want:    "unknown key",
		},
		{
			name:    "duplicate",
			content: "wow_path = \"/wow\"\nwow_path = \"/other\"\nbackup_directory = \"/backups\"\nautomatic_retention = 3\n",
			want:    "duplicate key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(filePath, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(filePath)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateRejectsRelativePathsAndInvalidRetention(t *testing.T) {
	t.Parallel()

	tests := []Config{
		{WoWPath: "relative", BackupDirectory: "/backups", Retention: 3},
		{WoWPath: "/wow", BackupDirectory: "relative", Retention: 3},
		{WoWPath: "/wow", BackupDirectory: "/backups", Retention: 0},
	}
	for _, cfg := range tests {
		if err := cfg.Validate(); err == nil {
			t.Errorf("Config%#v.Validate() returned nil", cfg)
		}
	}
}

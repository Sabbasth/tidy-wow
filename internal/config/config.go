// Package config loads and saves tidy-wow's persistent configuration.
package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	fileName         = "config.toml"
	defaultRetention = 3
)

var defaultAddonExclusions = []string{"AI_VoiceOverData_Vanilla"}

// Config contains durable user choices.
type Config struct {
	WoWPath         string
	BackupDirectory string
	Retention       int
	AddonExclusions []string
}

// DefaultRetention is the number of automatic backups kept per flavor by default.
func DefaultRetention() int {
	return defaultRetention
}

// DefaultAddonExclusions returns the initial add-on exclusion patterns.
func DefaultAddonExclusions() []string {
	return append([]string(nil), defaultAddonExclusions...)
}

// DefaultPath returns the native per-user configuration path.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(dir, "tidy-wow", fileName), nil
}

// Validate checks that all required settings are present and independent from the working directory.
func (c Config) Validate() error {
	if c.WoWPath == "" {
		return errors.New("WoW installation path is required")
	}
	if !filepath.IsAbs(c.WoWPath) {
		return errors.New("WoW installation path must be absolute")
	}
	if c.BackupDirectory == "" {
		return errors.New("backup directory is required")
	}
	if !filepath.IsAbs(c.BackupDirectory) {
		return errors.New("backup directory must be absolute")
	}
	if c.Retention < 1 {
		return errors.New("automatic retention must be at least 1")
	}
	if _, err := NormalizeAddonExclusions(c.AddonExclusions); err != nil {
		return err
	}
	return nil
}

// NormalizeAddonExclusions validates, deduplicates, and sorts add-on patterns.
func NormalizeAddonExclusions(patterns []string) ([]string, error) {
	seen := make(map[string]bool, len(patterns))
	normalized := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || strings.ContainsAny(pattern, `/\\`) || pattern == "." || pattern == ".." {
			return nil, fmt.Errorf("invalid add-on exclusion pattern %q", pattern)
		}
		if _, err := filepath.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("invalid add-on exclusion pattern %q: %w", pattern, err)
		}
		if !seen[pattern] {
			seen[pattern] = true
			normalized = append(normalized, pattern)
		}
	}
	sort.Strings(normalized)
	return normalized, nil
}

// Load reads and validates a configuration file.
func Load(filePath string) (Config, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration %q: %w", filePath, err)
	}
	defer f.Close()

	cfg, err := parse(f)
	if err != nil {
		return Config{}, fmt.Errorf("parse configuration %q: %w", filePath, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate configuration %q: %w", filePath, err)
	}
	return cfg, nil
}

// Save writes a validated configuration atomically.
func Save(filePath string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create configuration directory %q: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure configuration directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	exclusions, err := NormalizeAddonExclusions(cfg.AddonExclusions)
	if err != nil {
		return fmt.Errorf("normalize add-on exclusions: %w", err)
	}
	encodedExclusions, err := json.Marshal(exclusions)
	if err != nil {
		return fmt.Errorf("encode add-on exclusions: %w", err)
	}
	content := fmt.Sprintf("wow_path = %s\nbackup_directory = %s\nautomatic_retention = %d\naddon_exclusions = %s\n",
		strconv.Quote(cfg.WoWPath), strconv.Quote(cfg.BackupDirectory), cfg.Retention, encodedExclusions)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure temporary configuration: %w", err)
	}
	if _, err := io.WriteString(tmp, content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary configuration: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary configuration: %w", err)
	}
	if err := os.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("replace configuration %q: %w", filePath, err)
	}
	return nil
}

func parse(r io.Reader) (Config, error) {
	cfg := Config{AddonExclusions: DefaultAddonExclusions()}
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(r)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("line %d: expected key = value", lineNumber)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if seen[key] {
			return Config{}, fmt.Errorf("line %d: duplicate key %q", lineNumber, key)
		}
		seen[key] = true

		switch key {
		case "wow_path":
			parsed, err := strconv.Unquote(value)
			if err != nil {
				return Config{}, fmt.Errorf("line %d: invalid wow_path: %w", lineNumber, err)
			}
			cfg.WoWPath = parsed
		case "backup_directory":
			parsed, err := strconv.Unquote(value)
			if err != nil {
				return Config{}, fmt.Errorf("line %d: invalid backup_directory: %w", lineNumber, err)
			}
			cfg.BackupDirectory = parsed
		case "automatic_retention":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Config{}, fmt.Errorf("line %d: invalid automatic_retention: %w", lineNumber, err)
			}
			cfg.Retention = parsed
		case "addon_exclusions":
			var patterns []string
			if err := json.Unmarshal([]byte(value), &patterns); err != nil {
				return Config{}, fmt.Errorf("line %d: invalid addon_exclusions: %w", lineNumber, err)
			}
			cfg.AddonExclusions = patterns
		default:
			return Config{}, fmt.Errorf("line %d: unknown key %q", lineNumber, key)
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	return cfg, nil
}

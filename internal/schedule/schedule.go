// Package schedule manages the per-user macOS LaunchAgent.
package schedule

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const label = "com.github.sabbasth.tidy-wow.backup"

// Calendar is a launchd calendar interval. Weekday is nil for a daily job.
type Calendar struct {
	Hour    int
	Minute  int
	Weekday *int
}

// ParseTime parses a 24-hour HH:MM value.
func ParseTime(value string) (hour, minute int, err error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, errors.New("time must use 24-hour HH:MM format")
	}
	hour, err = strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, errors.New("hour must be between 00 and 23")
	}
	minute, err = strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, errors.New("minute must be between 00 and 59")
	}
	return hour, minute, nil
}

// ParseWeekday converts an English weekday to launchd's Sunday=1 convention.
func ParseWeekday(value string) (int, error) {
	days := map[string]int{
		"sunday": 1, "monday": 2, "tuesday": 3, "wednesday": 4,
		"thursday": 5, "friday": 6, "saturday": 7,
	}
	day, ok := days[strings.ToLower(strings.TrimSpace(value))]
	if !ok {
		return 0, fmt.Errorf("invalid weekday %q", value)
	}
	return day, nil
}

// Paths contains native per-user LaunchAgent and log locations.
type Paths struct {
	Plist  string
	Stdout string
	Stderr string
}

// DefaultPaths returns the native paths for a user's home directory.
func DefaultPaths(home string) Paths {
	return Paths{
		Plist:  filepath.Join(home, "Library", "LaunchAgents", label+".plist"),
		Stdout: filepath.Join(home, "Library", "Logs", "tidy-wow", "backup.log"),
		Stderr: filepath.Join(home, "Library", "Logs", "tidy-wow", "backup-error.log"),
	}
}

// Runner executes launchctl directly without a shell.
type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

// ExecRunner executes operating-system commands.
type ExecRunner struct{}

// Run executes one command and returns its combined output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Manager installs and removes the tidy-wow LaunchAgent.
type Manager struct {
	Runner Runner
	UID    int
	Paths  Paths
}

// Install writes and loads the LaunchAgent idempotently.
func (m Manager) Install(ctx context.Context, executable, configPath string, calendar Calendar) error {
	if !filepath.IsAbs(executable) || !filepath.IsAbs(configPath) {
		return errors.New("executable and configuration paths must be absolute")
	}
	if m.Runner == nil {
		return errors.New("command runner is required")
	}
	if err := validateCalendar(calendar); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.Paths.Plist), 0o700); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.Paths.Stdout), 0o700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	content, err := marshalPlist(executable, configPath, m.Paths, calendar)
	if err != nil {
		return err
	}
	if err := atomicWrite(m.Paths.Plist, content, 0o600); err != nil {
		return fmt.Errorf("write LaunchAgent: %w", err)
	}

	domain := fmt.Sprintf("gui/%d", m.UID)
	service := domain + "/" + label
	if _, err := m.Runner.Run(ctx, "launchctl", "print", service); err == nil {
		if output, err := m.Runner.Run(ctx, "launchctl", "bootout", service); err != nil {
			return commandError("unload existing LaunchAgent", output, err)
		}
	}
	if output, err := m.Runner.Run(ctx, "launchctl", "bootstrap", domain, m.Paths.Plist); err != nil {
		return commandError("load LaunchAgent", output, err)
	}
	return nil
}

// Status returns launchctl's description of the loaded service.
func (m Manager) Status(ctx context.Context) (string, error) {
	if m.Runner == nil {
		return "", errors.New("command runner is required")
	}
	service := fmt.Sprintf("gui/%d/%s", m.UID, label)
	output, err := m.Runner.Run(ctx, "launchctl", "print", service)
	if err != nil {
		return "", commandError("query LaunchAgent", output, err)
	}
	return string(output), nil
}

// Remove unloads the service if present and removes its plist.
func (m Manager) Remove(ctx context.Context) error {
	if m.Runner == nil {
		return errors.New("command runner is required")
	}
	service := fmt.Sprintf("gui/%d/%s", m.UID, label)
	if _, err := m.Runner.Run(ctx, "launchctl", "print", service); err == nil {
		if output, err := m.Runner.Run(ctx, "launchctl", "bootout", service); err != nil {
			return commandError("unload LaunchAgent", output, err)
		}
	}
	if err := os.Remove(m.Paths.Plist); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove LaunchAgent %q: %w", m.Paths.Plist, err)
	}
	return nil
}

func validateCalendar(calendar Calendar) error {
	if calendar.Hour < 0 || calendar.Hour > 23 || calendar.Minute < 0 || calendar.Minute > 59 {
		return errors.New("invalid calendar time")
	}
	if calendar.Weekday != nil && (*calendar.Weekday < 1 || *calendar.Weekday > 7) {
		return errors.New("invalid calendar weekday")
	}
	return nil
}

func marshalPlist(executable, configPath string, paths Paths, calendar Calendar) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString(xml.Header)
	buffer.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	encoder := xml.NewEncoder(&buffer)
	encoder.Indent("", "\t")
	start := func(name string) error { return encoder.EncodeToken(xml.StartElement{Name: xml.Name{Local: name}}) }
	end := func(name string) error { return encoder.EncodeToken(xml.EndElement{Name: xml.Name{Local: name}}) }
	textElement := func(name, value string) error {
		if err := start(name); err != nil {
			return err
		}
		if err := encoder.EncodeToken(xml.CharData(value)); err != nil {
			return err
		}
		return end(name)
	}
	key := func(value string) error { return textElement("key", value) }

	if err := encoder.EncodeToken(xml.StartElement{Name: xml.Name{Local: "plist"}, Attr: []xml.Attr{{Name: xml.Name{Local: "version"}, Value: "1.0"}}}); err != nil {
		return nil, err
	}
	if err := start("dict"); err != nil {
		return nil, err
	}
	values := []struct{ key, value string }{
		{"Label", label},
	}
	for _, item := range values {
		if err := key(item.key); err != nil {
			return nil, err
		}
		if err := textElement("string", item.value); err != nil {
			return nil, err
		}
	}
	if err := key("ProgramArguments"); err != nil {
		return nil, err
	}
	if err := start("array"); err != nil {
		return nil, err
	}
	for _, argument := range []string{executable, "backup", "--all", "--automatic", "--config", configPath} {
		if err := textElement("string", argument); err != nil {
			return nil, err
		}
	}
	if err := end("array"); err != nil {
		return nil, err
	}
	if err := key("StartCalendarInterval"); err != nil {
		return nil, err
	}
	if err := start("dict"); err != nil {
		return nil, err
	}
	calendarValues := []struct {
		key   string
		value int
	}{{"Hour", calendar.Hour}, {"Minute", calendar.Minute}}
	if calendar.Weekday != nil {
		calendarValues = append(calendarValues, struct {
			key   string
			value int
		}{"Weekday", *calendar.Weekday})
	}
	for _, item := range calendarValues {
		if err := key(item.key); err != nil {
			return nil, err
		}
		if err := textElement("integer", strconv.Itoa(item.value)); err != nil {
			return nil, err
		}
	}
	if err := end("dict"); err != nil {
		return nil, err
	}
	for _, item := range []struct{ key, value string }{{"StandardOutPath", paths.Stdout}, {"StandardErrorPath", paths.Stderr}} {
		if err := key(item.key); err != nil {
			return nil, err
		}
		if err := textElement("string", item.value); err != nil {
			return nil, err
		}
	}
	if err := end("dict"); err != nil {
		return nil, err
	}
	if err := end("plist"); err != nil {
		return nil, err
	}
	if err := encoder.Flush(); err != nil {
		return nil, fmt.Errorf("encode LaunchAgent plist: %w", err)
	}
	buffer.WriteByte('\n')
	return buffer.Bytes(), nil
}

func atomicWrite(filePath string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(filePath), ".launchagent-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filePath)
}

func commandError(action string, output []byte, err error) error {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, detail)
}

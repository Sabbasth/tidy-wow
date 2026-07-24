package schedule

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseTimeAndWeekday(t *testing.T) {
	t.Parallel()

	hour, minute, err := ParseTime("03:07")
	if err != nil || hour != 3 || minute != 7 {
		t.Fatalf("ParseTime() = %d, %d, %v", hour, minute, err)
	}
	for _, value := range []string{"3:07", "24:00", "03:60", "noon"} {
		if _, _, err := ParseTime(value); err == nil {
			t.Errorf("ParseTime(%q) returned nil error", value)
		}
	}
	if day, err := ParseWeekday("Monday"); err != nil || day != 2 {
		t.Errorf("ParseWeekday(Monday) = %d, %v", day, err)
	}
	if _, err := ParseWeekday("Funday"); err == nil {
		t.Error("ParseWeekday(Funday) returned nil error")
	}
}

func TestMarshalPlistEscapesPathsAndCalendar(t *testing.T) {
	t.Parallel()

	weekday := 2
	paths := Paths{
		Plist:  "/Users/test/Library/LaunchAgents/test.plist",
		Stdout: "/Users/test/Library/Logs/tidy-wow/out & log",
		Stderr: "/Users/test/Library/Logs/tidy-wow/error.log",
	}
	content, err := marshalPlist("/Applications/Tidy & Wow/tidy-wow", "/Users/test/Config & Data/config.toml", paths, Calendar{Hour: 3, Minute: 5, Weekday: &weekday})
	if err != nil {
		t.Fatalf("marshalPlist() error = %v", err)
	}
	text := string(content)
	for _, want := range []string{
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`,
		"/Applications/Tidy &amp; Wow/tidy-wow",
		"/Users/test/Config &amp; Data/config.toml",
		"<key>Hour</key>", "<integer>3</integer>",
		"<key>Minute</key>", "<integer>5</integer>",
		"<key>Weekday</key>", "<integer>2</integer>",
		"<string>--automatic</string>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist lacks %q:\n%s", want, text)
		}
	}
}

func TestManagerInstallIsIdempotent(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths := DefaultPaths(home)
	runner := &fakeRunner{results: []runResult{
		{output: []byte("loaded")},
		{},
		{},
	}}
	manager := Manager{Runner: runner, UID: 501, Paths: paths}
	if err := manager.Install(context.Background(), "/usr/local/bin/tidy-wow", "/Users/test/config.toml", Calendar{Hour: 3}); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	wantCalls := [][]string{
		{"launchctl", "print", "gui/501/" + label},
		{"launchctl", "bootout", "gui/501/" + label},
		{"launchctl", "bootstrap", "gui/501", paths.Plist},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Errorf("runner calls = %#v, want %#v", runner.calls, wantCalls)
	}
	info, err := os.Stat(paths.Plist)
	if err != nil {
		t.Fatalf("Stat(plist) error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("plist mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Dir(paths.Stdout)); err != nil {
		t.Errorf("log directory missing: %v", err)
	}
}

func TestManagerRemoveAbsentServiceAndPlist(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{results: []runResult{{err: os.ErrNotExist}}}
	manager := Manager{Runner: runner, UID: 501, Paths: DefaultPaths(t.TempDir())}
	if err := manager.Remove(context.Background()); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0][1] != "print" {
		t.Errorf("runner calls = %#v", runner.calls)
	}
}

type runResult struct {
	output []byte
	err    error
}

type fakeRunner struct {
	calls   [][]string
	results []runResult
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	result := r.results[0]
	r.results = r.results[1:]
	return result.output, result.err
}

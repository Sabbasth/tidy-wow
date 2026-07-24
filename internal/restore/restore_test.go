package restore

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sabbasth/tidy-wow/internal/backup"
	"github.com/sabbasth/tidy-wow/internal/wow"
)

func TestRestoreReplacesOnlyManagedProfile(t *testing.T) {
	t.Parallel()

	flavor := wow.Flavor{ProductID: "wow_classic_era", Directory: "_classic_era_", Version: "1.2.3"}
	sourceRoot := t.TempDir()
	writeFile(t, filepath.Join(sourceRoot, flavor.Directory, "Interface", "AddOns", "ElvUI", "ElvUI.toc"), "new addon")
	writeFile(t, filepath.Join(sourceRoot, flavor.Directory, "WTF", "Account", "TËST", "SavedVariables", "ElvUI.lua"), "new settings")
	writeFile(t, filepath.Join(sourceRoot, flavor.Directory, "WTF", "Account", "TËST", "Realm", "Hero", "config-cache.wtf"), "new config")

	archiveDestination := t.TempDir()
	archiveCreator := backup.NewCreator("test", func() time.Time {
		return time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	})
	archivePath, err := archiveCreator.Create(context.Background(), backup.Request{
		InstallationPath: sourceRoot,
		Flavor:           flavor,
		Destination:      archiveDestination,
		Kind:             backup.Manual,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	targetRoot := t.TempDir()
	flavorPath := filepath.Join(targetRoot, flavor.Directory)
	writeFile(t, filepath.Join(flavorPath, "Interface", "AddOns", "Stale", "Stale.toc"), "stale addon")
	writeFile(t, filepath.Join(flavorPath, "WTF", "Account", "TËST", "SavedVariables", "Stale.lua"), "stale settings")
	writeFile(t, filepath.Join(flavorPath, "WTF", "Account", "TËST", "Realm", "Hero", "config-cache.wtf"), "old config")
	writeFile(t, filepath.Join(flavorPath, "WTF", "Config.wtf"), "machine setting")
	writeFile(t, filepath.Join(flavorPath, "WTF", "Account", "TËST", "Realm", "Hero", "layout-local.txt"), "local layout")

	safetyDestination := t.TempDir()
	safetyCreator := backup.NewCreator("test", func() time.Time {
		return time.Date(2026, 7, 23, 10, 1, 0, 0, time.UTC)
	})
	restorer := New(safetyCreator)
	result, err := restorer.Restore(context.Background(), archivePath, wow.Installation{
		Path:    targetRoot,
		Flavors: []wow.Flavor{flavor},
	}, safetyDestination)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if result.ProductID != flavor.ProductID {
		t.Errorf("Result.ProductID = %q", result.ProductID)
	}
	if _, err := os.Stat(result.SafetyArchive); err != nil {
		t.Errorf("safety archive does not exist: %v", err)
	}

	assertContent(t, filepath.Join(flavorPath, "Interface", "AddOns", "ElvUI", "ElvUI.toc"), "new addon")
	assertContent(t, filepath.Join(flavorPath, "WTF", "Account", "TËST", "SavedVariables", "ElvUI.lua"), "new settings")
	assertContent(t, filepath.Join(flavorPath, "WTF", "Account", "TËST", "Realm", "Hero", "config-cache.wtf"), "new config")
	assertContent(t, filepath.Join(flavorPath, "WTF", "Config.wtf"), "machine setting")
	assertContent(t, filepath.Join(flavorPath, "WTF", "Account", "TËST", "Realm", "Hero", "layout-local.txt"), "local layout")
	if _, err := os.Stat(filepath.Join(flavorPath, "Interface", "AddOns", "Stale", "Stale.toc")); !os.IsNotExist(err) {
		t.Errorf("stale add-on remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(flavorPath, "WTF", "Account", "TËST", "SavedVariables", "Stale.lua")); !os.IsNotExist(err) {
		t.Errorf("stale SavedVariables remains: %v", err)
	}
}

func TestValidateArchiveRejectsUnsafeAndUnmanagedPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		entryPath string
		want      string
	}{
		{name: "traversal", entryPath: "../escape", want: "unsafe ZIP entry"},
		{name: "absolute", entryPath: "/escape", want: "unsafe ZIP entry"},
		{name: "backslash", entryPath: `WTF\escape`, want: "unsafe ZIP entry"},
		{name: "unmanaged", entryPath: "World of Warcraft.app/Contents/MacOS/Wow", want: "outside the managed profile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archivePath := createArchive(t, []archiveEntry{{name: tt.entryPath, content: "payload"}}, true)
			_, err := validateArchive(context.Background(), archivePath)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateArchive() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateArchiveRejectsChecksumMismatchAndUndeclaredEntry(t *testing.T) {
	t.Parallel()

	t.Run("checksum", func(t *testing.T) {
		archivePath := createArchive(t, []archiveEntry{{name: "Interface/AddOns/Test/Test.lua", content: "actual", manifestContent: "different"}}, true)
		_, err := validateArchive(context.Background(), archivePath)
		if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("validateArchive() error = %v", err)
		}
	})
	t.Run("undeclared", func(t *testing.T) {
		archivePath := createArchive(t, []archiveEntry{
			{name: "Interface/AddOns/Test/Test.lua", content: "declared"},
			{name: "Interface/AddOns/Test/Extra.lua", content: "extra", omitManifest: true},
		}, true)
		_, err := validateArchive(context.Background(), archivePath)
		if err == nil || !strings.Contains(err.Error(), "not declared") {
			t.Fatalf("validateArchive() error = %v", err)
		}
	})
}

func TestValidateArchiveRejectsSymlink(t *testing.T) {
	t.Parallel()

	filePath := filepath.Join(t.TempDir(), "archive.zip")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	header := &zip.FileHeader{Name: "Interface/AddOns/Test/link"}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("/etc/passwd")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = validateArchive(context.Background(), filePath)
	if err == nil || !strings.Contains(err.Error(), "unsupported ZIP entry type") {
		t.Fatalf("validateArchive() error = %v", err)
	}
}

func TestRestoreRejectsSymlinkedDestinationAncestor(t *testing.T) {
	t.Parallel()

	flavor := wow.Flavor{ProductID: "wow_classic_era", Directory: "_classic_era_", Version: "1"}
	sourceRoot := t.TempDir()
	writeFile(t, filepath.Join(sourceRoot, flavor.Directory, "WTF", "Account", "ACCOUNT", "SavedVariables", "Test.lua"), "new")
	creator := backup.NewCreator("test", time.Now)
	archivePath, err := creator.Create(context.Background(), backup.Request{
		InstallationPath: sourceRoot, Flavor: flavor, Destination: t.TempDir(), Kind: backup.Manual,
	})
	if err != nil {
		t.Fatal(err)
	}

	targetRoot := t.TempDir()
	flavorPath := filepath.Join(targetRoot, flavor.Directory)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(flavorPath, "WTF"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(flavorPath, "WTF", "Account")); err != nil {
		t.Fatal(err)
	}
	_, err = New(creator).Restore(context.Background(), archivePath, wow.Installation{Path: targetRoot, Flavors: []wow.Flavor{flavor}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "symbolic link in restore destination") {
		t.Fatalf("Restore() error = %v, want destination symlink error", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("restore wrote outside flavor: %v", entries)
	}
}

func TestRestoreRollsBackAfterReplacementFailure(t *testing.T) {
	t.Parallel()

	flavor := wow.Flavor{ProductID: "wow_classic_era", Directory: "_classic_era_", Version: "1"}
	sourceRoot := t.TempDir()
	writeFile(t, filepath.Join(sourceRoot, flavor.Directory, "Interface", "AddOns", "New", "New.toc"), "new")
	creator := backup.NewCreator("test", time.Now)
	archivePath, err := creator.Create(context.Background(), backup.Request{
		InstallationPath: sourceRoot, Flavor: flavor, Destination: t.TempDir(), Kind: backup.Manual,
	})
	if err != nil {
		t.Fatal(err)
	}

	targetRoot := t.TempDir()
	oldPath := filepath.Join(targetRoot, flavor.Directory, "Interface", "AddOns", "Old", "Old.toc")
	writeFile(t, oldPath, "old")
	restorer := New(creator)
	replacements := 0
	restorer.replace = func(flavorPath, staging string) error {
		replacements++
		if replacements == 1 {
			if err := clearManaged(flavorPath); err != nil {
				return err
			}
			return os.ErrPermission
		}
		return replaceManaged(flavorPath, staging)
	}
	_, err = restorer.Restore(context.Background(), archivePath, wow.Installation{Path: targetRoot, Flavors: []wow.Flavor{flavor}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("Restore() error = %v, want rolled back error", err)
	}
	if replacements != 2 {
		t.Errorf("replacement calls = %d, want 2", replacements)
	}
	assertContent(t, oldPath, "old")
	if _, err := os.Stat(filepath.Join(targetRoot, flavor.Directory, "Interface", "AddOns", "New", "New.toc")); !os.IsNotExist(err) {
		t.Errorf("failed restore content remains: %v", err)
	}
}

type archiveEntry struct {
	name            string
	content         string
	manifestContent string
	omitManifest    bool
}

func createArchive(t *testing.T, entries []archiveEntry, includeManifest bool) string {
	t.Helper()
	filePath := filepath.Join(t.TempDir(), "archive.zip")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	manifest := backup.Manifest{SchemaVersion: 1, ProductID: "wow_classic_era", Kind: backup.Manual}
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.name, Method: zip.Store}
		header.SetMode(0o600)
		entry, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(item.content)); err != nil {
			t.Fatal(err)
		}
		if !item.omitManifest {
			content := item.content
			if item.manifestContent != "" {
				content = item.manifestContent
			}
			sum := sha256.Sum256([]byte(content))
			manifest.Files = append(manifest.Files, backup.File{
				Path: item.name, Size: int64(len(item.content)), SHA256: hex.EncodeToString(sum[:]),
			})
		}
	}
	if includeManifest {
		header := &zip.FileHeader{Name: "manifest.json", Method: zip.Store}
		header.SetMode(0o600)
		entry, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(entry).Encode(manifest); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return filePath
}

func writeFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertContent(t *testing.T, filePath, want string) {
	t.Helper()
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Errorf("read %q: %v", filePath, err)
		return
	}
	if string(content) != want {
		t.Errorf("content of %q = %q, want %q", filePath, content, want)
	}
}

package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sabbasth/tidy-wow/internal/wow"
)

func TestCreateArchivesOnlyPortableFiles(t *testing.T) {
	t.Parallel()

	installation := t.TempDir()
	flavor := wow.Flavor{ProductID: "wow_classic_era", Directory: "_classic_era_", Version: "1.2.3"}
	files := map[string]string{
		"Interface/AddOns/ElvUI/ElvUI.toc":                                    "## SavedVariables: ElvDB",
		"WTF/Account/ACCOUNT/SavedVariables/ElvUI.lua":                        "ElvDB = {}",
		"WTF/Account/ACCOUNT/Royaume/Personnage/SavedVariables/ElvUI.lua.bak": "ElvCharacterDB = {}",
		"WTF/Account/ACCOUNT/Royaume/Personnage/AddOns.txt":                   "ElvUI: enabled",
		"WTF/Account/ACCOUNT/Royaume/Personnage/config-cache.wtf":             "SET autoLootDefault \"1\"",
		"WTF/Account/ACCOUNT/Royaume/Personnage/chat-cache.txt":               "VERSION 8",
		"WTF/Account/ACCOUNT/Royaume/Personnage/bindings-cache.wtf":           "bind",
		"WTF/Account/ACCOUNT/macros-cache.txt":                                "MACRO 1",
		"WTF/Account/ACCOUNT/Royaume/Personnage/SavedVariables/Résumé.lua":    "unicode = true",
		"WTF/Config.wtf": "SET gxWindow \"1\"",
		"WTF/Account/ACCOUNT/Royaume/Personnage/layout-local.txt":        "X: 12",
		"WTF/Account/ACCOUNT/edit-mode-cache-account.txt":                "binary",
		"WTF/Account/ACCOUNT/Royaume/Personnage/cache.md5":               "hash",
		"WTF/Account/ACCOUNT/Royaume/Personnage/tts-cache-character.txt": "tts",
		"WTF/Account/ACCOUNT/Royaume/Personnage/config-cache.old":        "old",
		"WTF/Account/ACCOUNT/Royaume/Personnage/random.txt":              "random",
	}
	for relative, content := range files {
		writeFixture(t, filepath.Join(installation, flavor.Directory, filepath.FromSlash(relative)), content)
	}

	createdAt := time.Date(2026, time.July, 23, 12, 34, 56, 123, time.FixedZone("CEST", 2*60*60))
	destination := t.TempDir()
	creator := NewCreator("v0.1.0", func() time.Time { return createdAt })
	archivePath, err := creator.Create(context.Background(), Request{
		InstallationPath: installation,
		Flavor:           flavor,
		Destination:      destination,
		Kind:             Manual,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wantFilename := "tidy-wow-wow_classic_era-20260723T103456.000000123Z-manual.zip"
	if filepath.Base(archivePath) != wantFilename {
		t.Errorf("archive filename = %q, want %q", filepath.Base(archivePath), wantFilename)
	}

	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("zip.OpenReader() error = %v", err)
	}
	defer archive.Close()

	entries := make(map[string]*zip.File)
	for _, entry := range archive.File {
		entries[entry.Name] = entry
	}
	wantIncluded := []string{
		"Interface/AddOns/ElvUI/ElvUI.toc",
		"WTF/Account/ACCOUNT/SavedVariables/ElvUI.lua",
		"WTF/Account/ACCOUNT/Royaume/Personnage/SavedVariables/ElvUI.lua.bak",
		"WTF/Account/ACCOUNT/Royaume/Personnage/SavedVariables/Résumé.lua",
		"WTF/Account/ACCOUNT/Royaume/Personnage/AddOns.txt",
		"WTF/Account/ACCOUNT/Royaume/Personnage/config-cache.wtf",
		"WTF/Account/ACCOUNT/Royaume/Personnage/chat-cache.txt",
		"WTF/Account/ACCOUNT/Royaume/Personnage/bindings-cache.wtf",
		"WTF/Account/ACCOUNT/macros-cache.txt",
		manifestName,
	}
	for _, name := range wantIncluded {
		if entries[name] == nil {
			t.Errorf("archive lacks %q", name)
		}
	}
	wantExcluded := []string{
		"WTF/Config.wtf",
		"WTF/Account/ACCOUNT/Royaume/Personnage/layout-local.txt",
		"WTF/Account/ACCOUNT/edit-mode-cache-account.txt",
		"WTF/Account/ACCOUNT/Royaume/Personnage/cache.md5",
		"WTF/Account/ACCOUNT/Royaume/Personnage/tts-cache-character.txt",
		"WTF/Account/ACCOUNT/Royaume/Personnage/config-cache.old",
		"WTF/Account/ACCOUNT/Royaume/Personnage/random.txt",
	}
	for _, name := range wantExcluded {
		if entries[name] != nil {
			t.Errorf("archive unexpectedly contains %q", name)
		}
	}

	manifest := readManifest(t, entries[manifestName])
	if manifest.SchemaVersion != 1 || manifest.TidyWoWVersion != "v0.1.0" || manifest.Kind != Manual || manifest.ProductID != flavor.ProductID || manifest.WoWVersion != flavor.Version {
		t.Errorf("manifest metadata = %#v", manifest)
	}
	if len(manifest.Files) != len(wantIncluded)-1 {
		t.Fatalf("manifest has %d files, want %d", len(manifest.Files), len(wantIncluded)-1)
	}
	if !sort.SliceIsSorted(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path }) {
		t.Error("manifest files are not deterministically sorted")
	}
	for _, record := range manifest.Files {
		entry := entries[record.Path]
		if entry == nil {
			t.Errorf("manifest references missing entry %q", record.Path)
			continue
		}
		content := readEntry(t, entry)
		sum := sha256.Sum256(content)
		if record.Size != int64(len(content)) || record.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("invalid integrity record for %q: %#v", record.Path, record)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(destination, ".tidy-wow-*.zip.tmp")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Errorf("temporary archives remain: %v", matches)
	}
}

func TestCreateRefusesSymbolicLinks(t *testing.T) {
	t.Parallel()

	installation := t.TempDir()
	flavor := wow.Flavor{ProductID: "wow", Directory: "_retail_", Version: "1"}
	addOnPath := filepath.Join(installation, flavor.Directory, "Interface", "AddOns", "Unsafe")
	if err := os.MkdirAll(addOnPath, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "secret")
	writeFixture(t, target, "secret")
	if err := os.Symlink(target, filepath.Join(addOnPath, "data.lua")); err != nil {
		t.Fatal(err)
	}

	creator := NewCreator("test", time.Now)
	_, err := creator.Create(context.Background(), Request{
		InstallationPath: installation,
		Flavor:           flavor,
		Destination:      t.TempDir(),
		Kind:             Manual,
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Create() error = %v, want symbolic link error", err)
	}
}

func TestCreateExcludesMatchingAddonDirectories(t *testing.T) {
	t.Parallel()

	installation := t.TempDir()
	flavor := wow.Flavor{ProductID: "wow", Directory: "_retail_", Version: "1"}
	writeFixture(t, filepath.Join(installation, flavor.Directory, "Interface", "AddOns", "AI_VoiceOverData_Vanilla", "audio.mp3"), "large data")
	writeFixture(t, filepath.Join(installation, flavor.Directory, "Interface", "AddOns", "Questie", "Questie.lua"), "included")
	archivePath, err := NewCreator("test", time.Now).Create(context.Background(), Request{
		InstallationPath: installation,
		Flavor:           flavor,
		Destination:      t.TempDir(),
		Kind:             Manual,
		AddonExclusions:  []string{"AI_VoiceOverData_*"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if strings.HasPrefix(entry.Name, "Interface/AddOns/AI_VoiceOverData_Vanilla/") {
			t.Errorf("archive unexpectedly includes excluded entry %q", entry.Name)
		}
	}
}

func TestApplyRetentionOnlyDeletesTargetAutomaticArchives(t *testing.T) {
	t.Parallel()

	destination := t.TempDir()
	names := []string{
		"tidy-wow-wow_classic_era-20260720T000000.000000000Z-automatic.zip",
		"tidy-wow-wow_classic_era-20260721T000000.000000000Z-automatic.zip",
		"tidy-wow-wow_classic_era-20260722T000000.000000000Z-automatic.zip",
		"tidy-wow-wow_classic_era-20260719T000000.000000000Z-manual.zip",
		"tidy-wow-wow_classic_era-20260718T000000.000000000Z-pre-restore.zip",
		"tidy-wow-wow_anniversary-20260717T000000.000000000Z-automatic.zip",
		"unrelated.zip",
	}
	for _, name := range names {
		writeFixture(t, filepath.Join(destination, name), name)
	}
	if err := ApplyRetention(destination, "wow_classic_era", 2); err != nil {
		t.Fatalf("ApplyRetention() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, names[0])); !os.IsNotExist(err) {
		t.Errorf("oldest automatic archive was not removed: %v", err)
	}
	for _, name := range names[1:] {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Errorf("archive %q should remain: %v", name, err)
		}
	}
}

func TestCreateRejectsUnsafeFlavorDirectory(t *testing.T) {
	t.Parallel()

	creator := NewCreator("test", time.Now)
	_, err := creator.Create(context.Background(), Request{
		InstallationPath: t.TempDir(),
		Flavor:           wow.Flavor{ProductID: "wow", Directory: "../outside"},
		Destination:      t.TempDir(),
		Kind:             Manual,
	})
	if err == nil || !strings.Contains(err.Error(), "one path component") {
		t.Fatalf("Create() error = %v, want unsafe directory error", err)
	}
}

func readManifest(t *testing.T, entry *zip.File) Manifest {
	t.Helper()
	if entry == nil {
		t.Fatal("manifest entry is nil")
	}
	content := readEntry(t, entry)
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("json.Unmarshal(manifest) error = %v", err)
	}
	return manifest
}

func readEntry(t *testing.T, entry *zip.File) []byte {
	t.Helper()
	r, err := entry.Open()
	if err != nil {
		t.Fatalf("entry.Open(%q) error = %v", entry.Name, err)
	}
	defer r.Close()
	content, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read ZIP entry %q: %v", entry.Name, err)
	}
	return content
}

func writeFixture(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

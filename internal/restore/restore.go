// Package restore validates and restores tidy-wow archives.
package restore

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sabbasth/tidy-wow/internal/backup"
	"github.com/sabbasth/tidy-wow/internal/wow"
)

const maxManifestSize = 16 << 20

// Result describes a completed restore and its safety archive.
type Result struct {
	ProductID     string
	SafetyArchive string
}

// Restorer applies validated archives to an installation.
type Restorer struct {
	creator         *backup.Creator
	addonExclusions []string
	replace         func(string, string) error
}

// New constructs a restorer that uses creator for pre-restore archives.
func New(creator *backup.Creator) *Restorer {
	return NewWithExclusions(creator, nil)
}

// NewWithExclusions constructs a restorer that preserves excluded add-ons.
func NewWithExclusions(creator *backup.Creator, exclusions []string) *Restorer {
	cloned := append([]string(nil), exclusions...)
	return &Restorer{
		creator:         creator,
		addonExclusions: cloned,
		replace: func(flavorPath, staging string) error {
			return replaceManagedWithExclusions(flavorPath, staging, cloned)
		},
	}
}

// Restore validates an archive, creates a safety backup, and replaces managed paths.
func (r *Restorer) Restore(ctx context.Context, archivePath string, installation wow.Installation, safetyDestination string) (Result, error) {
	if r.creator == nil {
		return Result{}, errors.New("backup creator is required")
	}
	validated, err := validateArchive(ctx, archivePath)
	if err != nil {
		return Result{}, err
	}
	defer validated.Close()

	flavor, err := installation.ResolveFlavor(validated.manifest.ProductID)
	if err != nil {
		return Result{}, fmt.Errorf("resolve archive flavor: %w", err)
	}
	flavorPath := filepath.Join(installation.Path, flavor.Directory)
	if err := ensureSafeDestinations(flavorPath, validated.manifest.Files); err != nil {
		return Result{}, err
	}
	staging, err := extractToStaging(ctx, validated, flavorPath, r.addonExclusions)
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(staging)

	safetyArchive, err := r.creator.Create(ctx, backup.Request{
		InstallationPath: installation.Path,
		Flavor:           flavor,
		Destination:      safetyDestination,
		Kind:             backup.PreRestore,
		AddonExclusions:  r.addonExclusions,
	})
	if err != nil {
		return Result{}, fmt.Errorf("create pre-restore safety archive: %w", err)
	}

	if err := r.replace(flavorPath, staging); err != nil {
		rollbackErr := r.rollback(ctx, safetyArchive, flavorPath)
		if rollbackErr != nil {
			return Result{}, errors.Join(fmt.Errorf("replace managed profile: %w", err), fmt.Errorf("rollback failed: %w", rollbackErr))
		}
		return Result{}, fmt.Errorf("replace managed profile (rolled back): %w", err)
	}
	return Result{ProductID: flavor.ProductID, SafetyArchive: safetyArchive}, nil
}

func (r *Restorer) rollback(ctx context.Context, archivePath, flavorPath string) error {
	validated, err := validateArchive(ctx, archivePath)
	if err != nil {
		return err
	}
	defer validated.Close()
	staging, err := extractToStaging(ctx, validated, flavorPath, r.addonExclusions)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	return r.replace(flavorPath, staging)
}

func ensureSafeDestinations(flavorPath string, files []backup.File) error {
	for _, record := range files {
		current := flavorPath
		for _, component := range strings.Split(record.Path, "/") {
			current = filepath.Join(current, component)
			info, err := os.Lstat(current)
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return fmt.Errorf("inspect restore destination %q: %w", current, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refuse symbolic link in restore destination %q", current)
			}
		}
	}
	return nil
}

type validatedArchive struct {
	reader   *zip.ReadCloser
	manifest backup.Manifest
	entries  map[string]*zip.File
}

func (a *validatedArchive) Close() error {
	return a.reader.Close()
}

func validateArchive(ctx context.Context, archivePath string) (*validatedArchive, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open archive %q: %w", archivePath, err)
	}
	fail := func(err error) (*validatedArchive, error) {
		reader.Close()
		return nil, err
	}

	entries := make(map[string]*zip.File, len(reader.File))
	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := validateEntryName(entry.Name); err != nil {
			return fail(fmt.Errorf("unsafe ZIP entry %q: %w", entry.Name, err))
		}
		if entries[entry.Name] != nil {
			return fail(fmt.Errorf("duplicate ZIP entry %q", entry.Name))
		}
		if !entry.Mode().IsRegular() {
			return fail(fmt.Errorf("unsupported ZIP entry type %q", entry.Name))
		}
		entries[entry.Name] = entry
	}
	manifestEntry := entries["manifest.json"]
	if manifestEntry == nil {
		return fail(errors.New("archive has no manifest.json"))
	}
	if manifestEntry.UncompressedSize64 > maxManifestSize {
		return fail(errors.New("archive manifest is too large"))
	}
	manifestContent, err := readLimited(manifestEntry, int64(manifestEntry.UncompressedSize64))
	if err != nil {
		return fail(fmt.Errorf("read archive manifest: %w", err))
	}
	var manifest backup.Manifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestContent)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fail(fmt.Errorf("decode archive manifest: %w", err))
	}
	if manifest.SchemaVersion != 1 {
		return fail(fmt.Errorf("unsupported archive schema version %d", manifest.SchemaVersion))
	}
	if manifest.ProductID == "" {
		return fail(errors.New("archive manifest has no product ID"))
	}

	records := make(map[string]backup.File, len(manifest.Files))
	for _, record := range manifest.Files {
		if record.Path == "manifest.json" {
			return fail(errors.New("manifest must not list itself"))
		}
		if err := validateEntryName(record.Path); err != nil {
			return fail(fmt.Errorf("unsafe manifest path %q: %w", record.Path, err))
		}
		if !isManagedArchivePath(record.Path) {
			return fail(fmt.Errorf("manifest path %q is outside the managed profile", record.Path))
		}
		if records[record.Path].Path != "" {
			return fail(fmt.Errorf("duplicate manifest path %q", record.Path))
		}
		if record.Size < 0 || len(record.SHA256) != sha256.Size*2 {
			return fail(fmt.Errorf("invalid integrity metadata for %q", record.Path))
		}
		if _, err := hex.DecodeString(record.SHA256); err != nil {
			return fail(fmt.Errorf("invalid SHA-256 for %q", record.Path))
		}
		entry := entries[record.Path]
		if entry == nil {
			return fail(fmt.Errorf("manifest references missing entry %q", record.Path))
		}
		if entry.UncompressedSize64 != uint64(record.Size) {
			return fail(fmt.Errorf("size mismatch for %q", record.Path))
		}
		records[record.Path] = record
	}
	if len(entries) != len(records)+1 {
		return fail(errors.New("archive contains entries not declared by the manifest"))
	}
	for name, record := range records {
		actualChecksum, readErr := checksumEntry(entries[name], record.Size)
		if readErr != nil {
			return fail(fmt.Errorf("verify %q: %w", name, readErr))
		}
		if !strings.EqualFold(actualChecksum, record.SHA256) {
			return fail(fmt.Errorf("checksum mismatch for %q", name))
		}
	}
	return &validatedArchive{reader: reader, manifest: manifest, entries: entries}, nil
}

func isManagedArchivePath(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) >= 3 && parts[0] == "Interface" && parts[1] == "AddOns" {
		return true
	}
	return backup.IsPortableWTFPath(filepath.FromSlash(name))
}

func validateEntryName(name string) error {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return errors.New("path must be a non-empty relative slash path")
	}
	cleaned := path.Clean(name)
	if cleaned != name || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return errors.New("path is not clean or traverses outside the flavor")
	}
	return nil
}

func readLimited(entry *zip.File, expected int64) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, expected+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != expected {
		return nil, fmt.Errorf("expanded size is %d, expected %d", len(content), expected)
	}
	return content, nil
}

func checksumEntry(entry *zip.File, expected int64) (string, error) {
	reader, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, reader)
	if err != nil {
		return "", err
	}
	if written != expected {
		return "", fmt.Errorf("expanded size is %d, expected %d", written, expected)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractToStaging(ctx context.Context, archive *validatedArchive, flavorPath string, exclusions []string) (string, error) {
	staging, err := os.MkdirTemp(flavorPath, ".tidy-wow-restore-")
	if err != nil {
		return "", fmt.Errorf("create restore staging directory: %w", err)
	}
	succeeded := false
	defer func() {
		if !succeeded {
			os.RemoveAll(staging)
		}
	}()

	paths := make([]string, 0, len(archive.manifest.Files))
	for _, record := range archive.manifest.Files {
		if isExcludedAddonPath(record.Path, exclusions) {
			continue
		}
		paths = append(paths, record.Path)
	}
	sort.Strings(paths)
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		destination := filepath.Join(staging, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return "", fmt.Errorf("create staging directory for %q: %w", name, err)
		}
		source, err := archive.entries[name].Open()
		if err != nil {
			return "", fmt.Errorf("open archive entry %q: %w", name, err)
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			source.Close()
			return "", fmt.Errorf("create staged file %q: %w", name, err)
		}
		_, copyErr := io.Copy(file, source)
		closeErr := file.Close()
		sourceErr := source.Close()
		if err := errors.Join(copyErr, closeErr, sourceErr); err != nil {
			return "", fmt.Errorf("extract %q: %w", name, err)
		}
	}
	succeeded = true
	return staging, nil
}

func replaceManaged(flavorPath, staging string) error {
	return replaceManagedWithExclusions(flavorPath, staging, nil)
}

func replaceManagedWithExclusions(flavorPath, staging string, exclusions []string) error {
	if err := clearManagedWithExclusions(flavorPath, exclusions); err != nil {
		return err
	}
	return filepath.WalkDir(staging, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(staging, source)
		if err != nil || relative == "." {
			return err
		}
		destination := filepath.Join(flavorPath, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := os.Rename(source, destination); err != nil {
			return fmt.Errorf("install restored file %q: %w", relative, err)
		}
		return nil
	})
}

func isExcludedAddonPath(name string, exclusions []string) bool {
	parts := strings.Split(name, "/")
	if len(parts) < 3 || parts[0] != "Interface" || parts[1] != "AddOns" {
		return false
	}
	for _, pattern := range exclusions {
		matched, _ := filepath.Match(pattern, parts[2])
		if matched {
			return true
		}
	}
	return false
}

func clearManaged(flavorPath string) error {
	return clearManagedWithExclusions(flavorPath, nil)
}

func clearManagedWithExclusions(flavorPath string, exclusions []string) error {
	addOnsPath := filepath.Join(flavorPath, "Interface", "AddOns")
	entries, err := os.ReadDir(addOnsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing AddOns: %w", err)
	}
	for _, entry := range entries {
		if isExcludedAddonPath(filepath.ToSlash(filepath.Join("Interface", "AddOns", entry.Name())), exclusions) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(addOnsPath, entry.Name())); err != nil {
			return fmt.Errorf("remove existing add-on %q: %w", entry.Name(), err)
		}
	}
	wtfPath := filepath.Join(flavorPath, "WTF")
	var targets []string
	err = filepath.WalkDir(wtfPath, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		relative, err := filepath.Rel(flavorPath, current)
		if err != nil {
			return err
		}
		if entry.Name() == "SavedVariables" && (entry.IsDir() || entry.Type()&os.ModeSymlink != 0) {
			targets = append(targets, current)
			return filepath.SkipDir
		}
		if !entry.IsDir() && backup.IsPortableWTFPath(relative) {
			targets = append(targets, current)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("enumerate existing managed profile: %w", err)
	}
	for _, target := range targets {
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove managed path %q: %w", target, err)
		}
	}
	return nil
}

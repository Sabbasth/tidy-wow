// Package backup creates portable, verifiable WoW profile archives.
package backup

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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sabbasth/tidy-wow/internal/wow"
)

const manifestName = "manifest.json"

// Kind identifies why an archive was created.
type Kind string

const (
	// Manual is a user-requested backup.
	Manual Kind = "manual"
	// Automatic is a backup created by a scheduled job.
	Automatic Kind = "automatic"
	// PreRestore protects the current profile before a restore.
	PreRestore Kind = "pre-restore"
)

// File records one archived file and its integrity metadata.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest describes and authenticates the contents of an archive.
type Manifest struct {
	SchemaVersion  int    `json:"schema_version"`
	TidyWoWVersion string `json:"tidy_wow_version"`
	CreatedAt      string `json:"created_at"`
	Kind           Kind   `json:"kind"`
	ProductID      string `json:"product_id"`
	WoWVersion     string `json:"wow_version"`
	Files          []File `json:"files"`
}

// Request specifies one archive operation.
type Request struct {
	InstallationPath string
	Flavor           wow.Flavor
	Destination      string
	Kind             Kind
	AddonExclusions  []string
}

// Creator creates archives using explicit version and time dependencies.
type Creator struct {
	version string
	now     func() time.Time
}

// NewCreator constructs an archive creator.
func NewCreator(version string, now func() time.Time) *Creator {
	if now == nil {
		now = time.Now
	}
	return &Creator{version: version, now: now}
}

// Create writes and atomically publishes one ZIP archive.
func (c *Creator) Create(ctx context.Context, request Request) (archivePath string, err error) {
	if err := validateRequest(request); err != nil {
		return "", err
	}
	if err := os.MkdirAll(request.Destination, 0o700); err != nil {
		return "", fmt.Errorf("create backup destination %q: %w", request.Destination, err)
	}

	flavorPath := filepath.Join(request.InstallationPath, request.Flavor.Directory)
	files, err := collectFiles(ctx, flavorPath, request.AddonExclusions)
	if err != nil {
		return "", fmt.Errorf("select portable files for %s: %w", request.Flavor.ProductID, err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("flavor %s has no portable profile files", request.Flavor.ProductID)
	}

	createdAt := c.now().UTC()
	filename := archiveFilename(request.Flavor.ProductID, createdAt, request.Kind)
	archivePath = filepath.Join(request.Destination, filename)
	temporary, err := os.CreateTemp(request.Destination, ".tidy-wow-*.zip.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary archive: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		temporary.Close()
		os.Remove(temporaryPath)
	}()

	zw := zip.NewWriter(temporary)
	manifest := Manifest{
		SchemaVersion:  1,
		TidyWoWVersion: c.version,
		CreatedAt:      createdAt.Format(time.RFC3339Nano),
		Kind:           request.Kind,
		ProductID:      request.Flavor.ProductID,
		WoWVersion:     request.Flavor.Version,
		Files:          make([]File, 0, len(files)),
	}
	for _, selected := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		record, writeErr := writeFile(zw, flavorPath, selected)
		if writeErr != nil {
			return "", writeErr
		}
		manifest.Files = append(manifest.Files, record)
	}
	if err := writeManifest(zw, manifest); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("finalize archive: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("sync archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close archive: %w", err)
	}

	// A hard link publishes the completed file atomically without replacing a
	// same-named archive. Both paths are on the destination filesystem.
	if err := os.Link(temporaryPath, archivePath); err != nil {
		return "", fmt.Errorf("publish archive %q: %w", archivePath, err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return "", fmt.Errorf("remove temporary archive: %w", err)
	}
	return archivePath, nil
}

// ApplyRetention keeps the newest automatic archives for one flavor.
func ApplyRetention(destination, productID string, keep int) error {
	if keep < 1 {
		return errors.New("automatic retention must be at least 1")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return fmt.Errorf("read backup destination %q: %w", destination, err)
	}
	prefix := "tidy-wow-" + productID + "-"
	suffix := "-automatic.zip"
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), suffix) {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names[keep:] {
		if err := os.Remove(filepath.Join(destination, name)); err != nil {
			return fmt.Errorf("remove expired automatic backup %q: %w", name, err)
		}
	}
	return nil
}

func validateRequest(request Request) error {
	if !filepath.IsAbs(request.InstallationPath) {
		return errors.New("installation path must be absolute")
	}
	if !filepath.IsAbs(request.Destination) {
		return errors.New("backup destination must be absolute")
	}
	if request.Flavor.ProductID == "" || request.Flavor.Directory == "" {
		return errors.New("flavor product ID and directory are required")
	}
	if filepath.Base(request.Flavor.Directory) != request.Flavor.Directory || request.Flavor.Directory == "." {
		return errors.New("flavor directory must be one path component")
	}
	if !safeProductID(request.Flavor.ProductID) {
		return fmt.Errorf("unsafe flavor product ID %q", request.Flavor.ProductID)
	}
	for _, pattern := range request.AddonExclusions {
		if !safeAddonPattern(pattern) {
			return fmt.Errorf("unsafe add-on exclusion pattern %q", pattern)
		}
	}
	switch request.Kind {
	case Manual, Automatic, PreRestore:
		return nil
	default:
		return fmt.Errorf("unsupported backup kind %q", request.Kind)
	}
}

func safeAddonPattern(pattern string) bool {
	if pattern == "" || strings.ContainsAny(pattern, `/\\`) || pattern == "." || pattern == ".." {
		return false
	}
	_, err := filepath.Match(pattern, "")
	return err == nil
}

func safeProductID(productID string) bool {
	for _, r := range productID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return productID != ""
}

func collectFiles(ctx context.Context, flavorPath string, exclusions []string) ([]string, error) {
	var files []string
	addOnsPath := filepath.Join(flavorPath, "Interface", "AddOns")
	if info, err := os.Lstat(addOnsPath); err == nil && info.IsDir() {
		if err := walkAddOns(ctx, flavorPath, addOnsPath, exclusions, &files); err != nil {
			return nil, err
		}
	} else if err == nil {
		return nil, fmt.Errorf("refuse unsupported AddOns path %q", addOnsPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect AddOns directory: %w", err)
	}

	wtfPath := filepath.Join(flavorPath, "WTF")
	if info, err := os.Lstat(wtfPath); err == nil && info.IsDir() {
		if err := walkSelected(ctx, flavorPath, wtfPath, IsPortableWTFPath, &files); err != nil {
			return nil, err
		}
	} else if err == nil {
		return nil, fmt.Errorf("refuse unsupported WTF path %q", wtfPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect WTF directory: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func walkAddOns(ctx context.Context, flavorPath, root string, exclusions []string, files *[]string) error {
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(flavorPath, current)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) == 3 && parts[0] == "Interface" && parts[1] == "AddOns" && matchesAddon(parts[2], exclusions) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if relative == "." || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symbolic link %q", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %q: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse unsupported file type %q", relative)
		}
		*files = append(*files, relative)
		return nil
	})
}

func matchesAddon(name string, exclusions []string) bool {
	for _, pattern := range exclusions {
		matched, _ := filepath.Match(pattern, name)
		if matched {
			return true
		}
	}
	return false
}

func walkSelected(ctx context.Context, flavorPath, root string, selected func(string) bool, files *[]string) error {
	return filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(flavorPath, current)
		if err != nil {
			return err
		}
		if relative == "." || entry.IsDir() {
			return nil
		}
		if !selected(relative) {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symbolic link %q", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %q: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse unsupported file type %q", relative)
		}
		*files = append(*files, relative)
		return nil
	})
}

// IsPortableWTFPath reports whether a relative flavor path belongs to the
// explicitly managed portable subset of WTF.
func IsPortableWTFPath(relative string) bool {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 2 || parts[0] != "WTF" {
		return false
	}
	base := parts[len(parts)-1]
	if strings.HasSuffix(base, ".old") || base == "cache.md5" || strings.HasPrefix(base, "tts-cache-") || base == "layout-local.txt" || strings.HasPrefix(base, "edit-mode-cache-") {
		return false
	}
	for _, part := range parts[1 : len(parts)-1] {
		if part == "SavedVariables" {
			return true
		}
	}
	switch base {
	case "AddOns.txt", "macros-cache.txt", "bindings-cache.wtf", "chat-cache.txt", "config-cache.wtf":
		return true
	default:
		return false
	}
}

func writeFile(zw *zip.Writer, flavorPath, relative string) (File, error) {
	fullPath := filepath.Join(flavorPath, relative)
	info, err := os.Lstat(fullPath)
	if err != nil {
		return File{}, fmt.Errorf("inspect selected file %q: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return File{}, fmt.Errorf("selected file %q changed to an unsupported type", relative)
	}
	source, err := os.Open(fullPath)
	if err != nil {
		return File{}, fmt.Errorf("open selected file %q: %w", relative, err)
	}
	defer source.Close()

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return File{}, fmt.Errorf("create ZIP header for %q: %w", relative, err)
	}
	header.Name = filepath.ToSlash(relative)
	header.Method = zip.Deflate
	header.SetMode(0o600)
	destination, err := zw.CreateHeader(header)
	if err != nil {
		return File{}, fmt.Errorf("create ZIP entry %q: %w", relative, err)
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(destination, hash), source)
	if err != nil {
		return File{}, fmt.Errorf("archive %q: %w", relative, err)
	}
	return File{Path: header.Name, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func writeManifest(zw *zip.Writer, manifest Manifest) error {
	header := &zip.FileHeader{Name: manifestName, Method: zip.Deflate}
	header.SetMode(0o600)
	destination, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create manifest entry: %w", err)
	}
	encoder := json.NewEncoder(destination)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func archiveFilename(productID string, createdAt time.Time, kind Kind) string {
	timestamp := createdAt.Format("20060102T150405.000000000Z")
	return fmt.Sprintf("tidy-wow-%s-%s-%s.zip", productID, timestamp, kind)
}

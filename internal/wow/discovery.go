// Package wow discovers and models World of Warcraft installations.
package wow

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Flavor is an installed WoW product with portable profile data.
type Flavor struct {
	ProductID string
	Directory string
	Version   string
}

// Addon is an installed add-on directory and its recursive file size.
type Addon struct {
	Name string
	Size int64
}

// ScanAddons lists the installed add-on directories for a flavor.
func ScanAddons(ctx context.Context, installationPath string, flavor Flavor) ([]Addon, error) {
	root := filepath.Join(installationPath, flavor.Directory, "Interface", "AddOns")
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect AddOns directory %q: %w", root, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("AddOns path %q is not a directory", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read AddOns directory %q: %w", root, err)
	}
	addons := make([]Addon, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		size, err := directorySize(ctx, filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("scan add-on %q: %w", entry.Name(), err)
		}
		addons = append(addons, Addon{Name: entry.Name(), Size: size})
	}
	sort.Slice(addons, func(i, j int) bool { return addons[i].Name < addons[j].Name })
	return addons, nil
}

func directorySize(ctx context.Context, root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symbolic link %q", current)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse unsupported file type %q", current)
		}
		size += info.Size()
		return nil
	})
	return size, err
}

// Installation is a validated WoW installation and its eligible flavors.
type Installation struct {
	Path    string
	Flavors []Flavor
}

// DiscoverOptions defines installation hints and injectable platform boundaries.
type DiscoverOptions struct {
	ExplicitPath   string
	ConfiguredPath string
	HomeDirectory  string
	MetadataSearch func(context.Context) ([]string, error)
}

// Discover finds the first valid installation in deterministic priority order.
func Discover(ctx context.Context, options DiscoverOptions) (Installation, error) {
	home := options.HomeDirectory
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Installation{}, fmt.Errorf("locate home directory: %w", err)
		}
	}

	candidates := []string{options.ExplicitPath, options.ConfiguredPath}
	candidates = append(candidates, battleNetCandidates(home)...)
	candidates = append(candidates,
		"/Applications/World of Warcraft",
		filepath.Join(home, "Applications", "World of Warcraft"),
	)

	seen := make(map[string]bool)
	var validationErrors []error
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		candidate = filepath.Clean(candidate)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		installation, err := Inspect(candidate)
		if err == nil {
			return installation, nil
		}
		if candidate == filepath.Clean(options.ExplicitPath) && options.ExplicitPath != "" {
			return Installation{}, fmt.Errorf("invalid explicit WoW installation: %w", err)
		}
		validationErrors = append(validationErrors, err)
	}

	search := options.MetadataSearch
	if search == nil {
		search = spotlightBuildInfo
	}
	paths, err := search(ctx)
	if err == nil {
		sort.Strings(paths)
		for _, buildInfo := range paths {
			candidate := filepath.Dir(buildInfo)
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			installation, inspectErr := Inspect(candidate)
			if inspectErr == nil {
				return installation, nil
			}
			validationErrors = append(validationErrors, inspectErr)
		}
	}

	if len(validationErrors) > 0 {
		return Installation{}, fmt.Errorf("no valid WoW installation found: %w", errors.Join(validationErrors...))
	}
	return Installation{}, errors.New("no WoW installation found")
}

// Inspect validates a WoW root and discovers flavors with profile content.
func Inspect(root string) (Installation, error) {
	if !filepath.IsAbs(root) {
		return Installation{}, fmt.Errorf("installation path %q is not absolute", root)
	}
	builds, err := readBuildInfo(filepath.Join(root, ".build.info"))
	if err != nil {
		return Installation{}, err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return Installation{}, fmt.Errorf("read installation directory %q: %w", root, err)
	}
	var flavors []Flavor
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		productID, err := readFlavorInfo(filepath.Join(directory, ".flavor.info"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Installation{}, err
		}
		if _, ok := builds[productID]; !ok {
			continue
		}
		if !hasPortableContent(directory) {
			continue
		}
		flavors = append(flavors, Flavor{
			ProductID: productID,
			Directory: entry.Name(),
			Version:   builds[productID],
		})
	}
	sort.Slice(flavors, func(i, j int) bool { return flavors[i].ProductID < flavors[j].ProductID })
	if len(flavors) == 0 {
		return Installation{}, fmt.Errorf("installation %q has no flavors with portable profile data", root)
	}
	return Installation{Path: filepath.Clean(root), Flavors: flavors}, nil
}

// ResolveFlavor matches a product ID, directory name, or supported convenience alias.
func (i Installation) ResolveFlavor(name string) (Flavor, error) {
	wanted := strings.ToLower(strings.TrimSpace(name))
	aliases := map[string]string{
		"era":         "wow_classic_era",
		"anniversary": "wow_anniversary",
		"retail":      "wow",
	}
	if productID, ok := aliases[wanted]; ok {
		wanted = productID
	}
	for _, flavor := range i.Flavors {
		if strings.EqualFold(flavor.ProductID, wanted) || strings.EqualFold(flavor.Directory, wanted) {
			return flavor, nil
		}
	}
	return Flavor{}, fmt.Errorf("flavor %q is not installed or has no portable profile data", name)
}

func hasPortableContent(directory string) bool {
	for _, relative := range []string{filepath.Join("Interface", "AddOns"), "WTF"} {
		info, err := os.Stat(filepath.Join(directory, relative))
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func readFlavorInfo(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open flavor metadata %q: %w", filePath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() || !scanner.Scan() {
		return "", fmt.Errorf("flavor metadata %q is incomplete", filePath)
	}
	productID := strings.TrimSpace(scanner.Text())
	if productID == "" {
		return "", fmt.Errorf("flavor metadata %q has an empty product ID", filePath)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read flavor metadata %q: %w", filePath, err)
	}
	return productID, nil
}

func readBuildInfo(filePath string) (map[string]string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open build metadata %q: %w", filePath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return nil, fmt.Errorf("build metadata %q is empty", filePath)
	}
	headings := strings.Split(scanner.Text(), "|")
	productIndex, versionIndex := -1, -1
	for index, heading := range headings {
		name, _, _ := strings.Cut(heading, "!")
		switch name {
		case "Product":
			productIndex = index
		case "Version":
			versionIndex = index
		}
	}
	if productIndex < 0 || versionIndex < 0 {
		return nil, fmt.Errorf("build metadata %q lacks Product or Version columns", filePath)
	}

	builds := make(map[string]string)
	for scanner.Scan() {
		columns := strings.Split(scanner.Text(), "|")
		if productIndex >= len(columns) || versionIndex >= len(columns) {
			continue
		}
		productID := strings.TrimSpace(columns[productIndex])
		if productID != "" {
			builds[productID] = strings.TrimSpace(columns[versionIndex])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read build metadata %q: %w", filePath, err)
	}
	if len(builds) == 0 {
		return nil, fmt.Errorf("build metadata %q contains no products", filePath)
	}
	return builds, nil
}

func battleNetCandidates(home string) []string {
	filePath := filepath.Join(home, "Library", "Application Support", "Battle.net", "Battle.net.config")
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	var document struct {
		Client struct {
			Install struct {
				DefaultInstallPath string
			}
		}
	}
	if json.Unmarshal(content, &document) != nil || document.Client.Install.DefaultInstallPath == "" {
		return nil
	}
	return []string{filepath.Join(document.Client.Install.DefaultInstallPath, "World of Warcraft")}
}

func spotlightBuildInfo(ctx context.Context) ([]string, error) {
	output, err := exec.CommandContext(ctx, "mdfind", "kMDItemFSName == '.build.info'c").Output()
	if err != nil {
		return nil, fmt.Errorf("search macOS metadata: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) > 100 {
		lines = lines[:100]
	}
	return lines, nil
}

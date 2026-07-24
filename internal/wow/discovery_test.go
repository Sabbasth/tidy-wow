package wow

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInspectDiscoversEligibleFlavors(t *testing.T) {
	t.Parallel()

	root := newInstallation(t, map[string]fixtureFlavor{
		"_classic_era_": {productID: "wow_classic_era", profilePath: filepath.Join("WTF", "Account")},
		"_anniversary_": {productID: "wow_anniversary", profilePath: filepath.Join("Interface", "AddOns")},
		"_retail_":      {productID: "wow", profilePath: ""},
		"_ptr_":         {productID: "wowt", profilePath: "WTF"},
	})

	installation, err := Inspect(root)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if installation.Path != root {
		t.Errorf("Inspect().Path = %q, want %q", installation.Path, root)
	}
	if len(installation.Flavors) != 2 {
		t.Fatalf("Inspect() found %d flavors, want 2: %#v", len(installation.Flavors), installation.Flavors)
	}
	if installation.Flavors[0].ProductID != "wow_anniversary" || installation.Flavors[1].ProductID != "wow_classic_era" {
		t.Errorf("Inspect() flavors = %#v", installation.Flavors)
	}
}

func TestResolveFlavor(t *testing.T) {
	t.Parallel()

	installation := Installation{Flavors: []Flavor{
		{ProductID: "wow_classic_era", Directory: "_classic_era_"},
		{ProductID: "wow_anniversary", Directory: "_anniversary_"},
	}}
	tests := map[string]string{
		"era":             "wow_classic_era",
		"anniversary":     "wow_anniversary",
		"wow_classic_era": "wow_classic_era",
		"_anniversary_":   "wow_anniversary",
	}
	for input, want := range tests {
		flavor, err := installation.ResolveFlavor(input)
		if err != nil {
			t.Errorf("ResolveFlavor(%q) error = %v", input, err)
			continue
		}
		if flavor.ProductID != want {
			t.Errorf("ResolveFlavor(%q) = %q, want %q", input, flavor.ProductID, want)
		}
	}
	if _, err := installation.ResolveFlavor("retail"); err == nil {
		t.Error("ResolveFlavor(retail) returned nil error for absent flavor")
	}
}

func TestDiscoverExplicitPathTakesPriority(t *testing.T) {
	t.Parallel()

	root := newInstallation(t, map[string]fixtureFlavor{
		"_classic_era_": {productID: "wow_classic_era", profilePath: "WTF"},
	})
	installation, err := Discover(context.Background(), DiscoverOptions{
		ExplicitPath:  root,
		HomeDirectory: t.TempDir(),
		MetadataSearch: func(context.Context) ([]string, error) {
			t.Fatal("metadata search should not run for a valid explicit path")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if installation.Path != root {
		t.Errorf("Discover().Path = %q, want %q", installation.Path, root)
	}
}

func TestDiscoverRejectsInvalidExplicitPath(t *testing.T) {
	t.Parallel()

	_, err := Discover(context.Background(), DiscoverOptions{
		ExplicitPath:   filepath.Join(t.TempDir(), "missing"),
		HomeDirectory:  t.TempDir(),
		MetadataSearch: func(context.Context) ([]string, error) { return nil, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "invalid explicit WoW installation") {
		t.Fatalf("Discover() error = %v, want explicit path error", err)
	}
}

func TestInspectRejectsFlavorNotDeclaredByBuildMetadata(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".build.info"), buildHeader+"\neu|1|1.0|wow_classic_era\n")
	writeFile(t, filepath.Join(root, "_anniversary_", ".flavor.info"), "Product Flavor!STRING:0\nwow_anniversary\n")
	if err := os.MkdirAll(filepath.Join(root, "_anniversary_", "WTF"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := Inspect(root)
	if err == nil || !strings.Contains(err.Error(), "no flavors") {
		t.Fatalf("Inspect() error = %v, want no flavors error", err)
	}
}

func TestScanAddons(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	flavor := Flavor{ProductID: "wow_classic_era", Directory: "_classic_era_"}
	writeFile(t, filepath.Join(root, flavor.Directory, "Interface", "AddOns", "Questie", "data.lua"), "1234")
	writeFile(t, filepath.Join(root, flavor.Directory, "Interface", "AddOns", "ElvUI", "core.lua"), "12")
	addons, err := ScanAddons(context.Background(), root, flavor)
	if err != nil {
		t.Fatalf("ScanAddons() error = %v", err)
	}
	want := []Addon{{Name: "ElvUI", Size: 2}, {Name: "Questie", Size: 4}}
	if !reflect.DeepEqual(addons, want) {
		t.Errorf("ScanAddons() = %#v, want %#v", addons, want)
	}
}

const buildHeader = "Region!STRING:0|Active!DEC:1|Version!STRING:0|Product!STRING:0"

type fixtureFlavor struct {
	productID   string
	profilePath string
}

func newInstallation(t *testing.T, flavors map[string]fixtureFlavor) string {
	t.Helper()
	root := t.TempDir()
	buildInfo := buildHeader
	for _, flavor := range flavors {
		if flavor.productID == "wowt" {
			continue
		}
		buildInfo += "\neu|1|1.2.3|" + flavor.productID
	}
	buildInfo += "\n"
	writeFile(t, filepath.Join(root, ".build.info"), buildInfo)
	for directory, flavor := range flavors {
		writeFile(t, filepath.Join(root, directory, ".flavor.info"), "Product Flavor!STRING:0\n"+flavor.productID+"\n")
		if flavor.profilePath != "" {
			if err := os.MkdirAll(filepath.Join(root, directory, flavor.profilePath), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
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

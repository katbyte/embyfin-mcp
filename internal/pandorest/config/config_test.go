package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelect(t *testing.T) {
	t.Parallel()

	all, err := Select("")
	if err != nil || len(all) != len(Services) {
		t.Errorf("Select(\"\") = %d services, %v", len(all), err)
	}
	one, err := Select(" jellyfin ")
	if err != nil || len(one) != 1 || one[0].Package != "jf" {
		t.Errorf("Select(jellyfin) = %+v, %v", one, err)
	}
	if _, err := Select("emby,plex"); err == nil || !strings.Contains(err.Error(), `unknown service "plex" (have emby, jellyfin, tmdb)`) {
		t.Errorf("Select with an unknown service = %v", err)
	}
}

// A service's document and definitions are named by the document's version,
// and the highest version present is the one resolved: an older document
// stays beside it, a newer one takes over by being added.
func TestResolveTakesTheHighestVersion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	svc, ok := Find("emby")
	if !ok {
		t.Fatal("no emby service")
	}
	svc = svc.In(root)
	if _, err := svc.Resolve(); err == nil || !strings.Contains(err.Error(), "no document api-defs/emby-openapi-<version>.json") {
		t.Errorf("no document = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "api-defs"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"4.9.0.30", "4.10.0.40", "4.10.0.9"} {
		if err := os.WriteFile(filepath.Join(root, SpecPath("emby", v)), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := svc.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	// the paths stay repository-relative; they are recorded in the definitions
	if resolved.Version != "4.10.0.40" || resolved.Spec != filepath.Join("api-defs", "emby-openapi-4.10.0.40.json") || resolved.Definitions != filepath.Join("api-defs", "emby-4.10.0.40") {
		t.Errorf("Resolve = %+v", resolved)
	}
	if got := resolved.Path(resolved.Spec); got != filepath.Join(root, "api-defs", "emby-openapi-4.10.0.40.json") {
		t.Errorf("Path(Spec) = %q", got)
	}
	versions, err := resolved.Versions()
	if err != nil || strings.Join(versions, " ") != "4.9.0.30 4.10.0.9 4.10.0.40" {
		t.Errorf("Versions = %v, %v (want numeric order, lowest first)", versions, err)
	}
	if svc.Path(svc.Output) != filepath.Join(root, "lib", "emby") {
		t.Errorf("Path(Output) = %q", svc.Path(svc.Output))
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"4.10", "4.9", 1},
		{"4.9", "4.10", -1},
		{"12.0.0", "12.0.0", 0},
		{"3", "3.1", -1},
		{"4.10.0.40", "4.10.0.9", 1},
		{"beta", "alpha", 1},
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// Every configured service has a document checked in, so the tools that
// read the config find something to work on.
func TestEveryServiceResolvesInTheRepository(t *testing.T) {
	t.Parallel()

	for _, svc := range Services {
		resolved, err := svc.In("../../..").Resolve()
		if err != nil {
			t.Errorf("%s: %v", svc.Name, err)
			continue
		}
		if _, err := os.Stat(resolved.Path(resolved.Definitions)); err != nil {
			t.Errorf("%s: definitions for %s are not checked in: %v", svc.Name, resolved.Version, err)
		}
	}
}

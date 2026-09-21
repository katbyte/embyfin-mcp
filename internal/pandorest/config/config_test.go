package config

import (
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

func TestPaths(t *testing.T) {
	t.Parallel()

	svc, ok := Find("emby")
	if !ok {
		t.Fatal("no emby service")
	}
	rooted := svc.In("/repo")
	// the configured paths stay repository-relative; they are recorded in the definitions
	if rooted.Spec != svc.Spec || rooted.Path(rooted.Spec) != filepath.Join(string(filepath.Separator)+"repo", "docs", "emby-openapi.json") {
		t.Errorf("In(/repo) = %+v", rooted)
	}
	if svc.Path(svc.Output) != filepath.Join("lib", "emby") {
		t.Errorf("Path without a root = %q", svc.Path(svc.Output))
	}
}

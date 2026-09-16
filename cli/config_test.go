package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// viper is global, so none of these can run in parallel and each resets it.
const (
	testServer = "http://nas:8096"
	testTool   = "item_get"
)

// load drives the real flag wiring against a temporary home and working
// directory.
func load(t *testing.T, home, wd string) *FlagData {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("HOME", home)
	t.Chdir(wd)

	if err := configureFlags(&cobra.Command{Use: "embyfin-mcp"}); err != nil {
		t.Fatalf("configureFlags: %v", err)
	}

	return GetFlags()
}

func write(t *testing.T, dir, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, ".embyfin-mcp"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// on is how a boolean is switched on from the environment.
const on = "true"

// A .embyfin-mcp in the working directory is documented as per-project
// settings, which it only is if it is found before the one in $HOME. viper
// reads the first file it finds.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestConfigFilePrecedence(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()

	t.Run("project wins over home", func(t *testing.T) {
		write(t, home, "SERVER=http://from-home\n")
		write(t, project, "SERVER=http://from-project\n")
		if got := load(t, home, project).Server; got != "http://from-project" {
			t.Errorf("server = %q, want the project config", got)
		}
	})

	t.Run("home is used when there is no project config", func(t *testing.T) {
		empty := t.TempDir()
		if got := load(t, home, empty).Server; got != "http://from-home" {
			t.Errorf("server = %q, want the home config", got)
		}
	})

	t.Run("no config at all is not an error", func(t *testing.T) {
		if got := load(t, t.TempDir(), t.TempDir()).Server; got != "" {
			t.Errorf("server = %q, want empty", got)
		}
	})
}

// The environment beats a config file, so a container can override what is
// baked into an image.
func TestEnvironmentOverridesConfigFile(t *testing.T) {
	home := t.TempDir()
	write(t, home, "SERVER=http://from-home\nTOKEN=from-home\n")

	t.Setenv("EMBYFIN_SERVER", "http://from-env")
	f := load(t, home, t.TempDir())

	if f.Server != "http://from-env" {
		t.Errorf("server = %q, want the environment", f.Server)
	}
	// and a key the environment did not set still comes from the file
	if f.Token != "from-home" {
		t.Errorf("token = %q, want the config file", f.Token)
	}
}

// Every documented EMBYFIN_* variable has to actually reach its field; a
// typo in the binding map is invisible until someone sets the variable and
// nothing happens.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestEnvironmentBindings(t *testing.T) {
	dir := t.TempDir()
	for _, env := range []struct {
		key, value string
		check      func(*FlagData) bool
	}{
		{"EMBYFIN_BACKEND", "jellyfin", func(f *FlagData) bool { return f.Backend == "jellyfin" }},
		{"EMBYFIN_SERVER", testServer, func(f *FlagData) bool { return f.Server == testServer }},
		{"EMBYFIN_TOKEN", "tok", func(f *FlagData) bool { return f.Token == "tok" }},
		{"EMBYFIN_READ_ONLY", on, func(f *FlagData) bool { return f.ReadOnly }},
		{"EMBYFIN_ENABLE_DELETE", on, func(f *FlagData) bool { return f.EnableDelete }},
		{"EMBYFIN_LISTEN", ":8080", func(f *FlagData) bool { return f.Listen == ":8080" }},
		{"EMBYFIN_AUTH_TOKEN", "bearer", func(f *FlagData) bool { return f.AuthToken == "bearer" }},
		{"EMBYFIN_ALLOW_NO_AUTH", on, func(f *FlagData) bool { return f.AllowNoAuth }},
		{"EMBYFIN_TMDB_KEY", "tmdb", func(f *FlagData) bool { return f.TMDBKey == "tmdb" }},
		{"EMBYFIN_TOOLSETS", "curation", func(f *FlagData) bool { return len(f.Toolsets) == 1 && f.Toolsets[0] == "curation" }},
		{"EMBYFIN_ALLOW_TOOLS", testTool, func(f *FlagData) bool { return len(f.AllowTools) == 1 && f.AllowTools[0] == testTool }},
		{"EMBYFIN_DENY_TOOLS", "*_delete", func(f *FlagData) bool { return len(f.DenyTools) == 1 && f.DenyTools[0] == "*_delete" }},
	} {
		t.Setenv(env.key, env.value)
		if f := load(t, dir, dir); !env.check(f) {
			t.Errorf("%s=%s did not reach its field: %+v", env.key, env.value, f)
		}
		_ = os.Unsetenv(env.key)
	}
}

// NewClient is where a missing server, token or a bad backend is caught,
// before anything tries to talk to the media server.
func TestNewClientValidation(t *testing.T) {
	t.Parallel()

	if _, err := (&FlagData{Backend: "emby"}).NewClient(); err == nil {
		t.Error("a client with no server should be refused")
	}
	if _, err := (&FlagData{Backend: "emby", Server: testServer}).NewClient(); err == nil {
		t.Error("a client with no token should be refused")
	}
	if _, err := (&FlagData{Backend: "plex", Server: testServer, Token: "t"}).NewClient(); err == nil {
		t.Error("an unknown backend should be refused")
	}
	if _, err := (&FlagData{Backend: "Jellyfin", Server: testServer, Token: "t"}).NewClient(); err != nil {
		t.Errorf("a complete configuration was refused: %v", err)
	}
}

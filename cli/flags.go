package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/tools"
	"github.com/katbyte/go-kt/clog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type FlagData struct {
	Backend      string   `mapstructure:"backend"`
	Server       string   `mapstructure:"server"`
	Token        string   `mapstructure:"token"`
	ReadOnly     bool     `mapstructure:"read-only"`
	EnableDelete bool     `mapstructure:"enable-delete"`
	Toolsets     []string `mapstructure:"toolsets"`
	AllowTools   []string `mapstructure:"allow-tools"`
	DenyTools    []string `mapstructure:"deny-tools"`
	Listen       string   `mapstructure:"listen"`
	AuthToken    string   `mapstructure:"auth-token"`
	AllowNoAuth  bool     `mapstructure:"allow-no-auth"`
	TMDBKey      string   `mapstructure:"tmdb-key"`
}

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.StringP("backend", "b", "emby", "media server backend: emby or jellyfin")
	pflags.StringP("server", "s", "", "the media server's url, e.g. http://nas:8096")
	pflags.StringP("token", "t", "", "the media server API key (consider exporting to EMBYFIN_TOKEN instead)")
	pflags.Bool("read-only", false, "register only tools that never change server state")
	pflags.Bool("enable-delete", false, "register the tools that delete media files and libraries")
	pflags.StringSlice("toolsets", nil, "groups of tools to register: all, core (default), curation, watching, organise, remote, admin, or a resource family like item (core is always included)")
	pflags.StringSlice("allow-tools", nil, "only register these tools: names, prefix globs like library_*, or the essential preset")
	pflags.StringSlice("deny-tools", nil, "never register these tools: names or prefix globs like *_delete")
	pflags.String("listen", "", "serve MCP over HTTP on this address (e.g. :8080) instead of stdio")
	pflags.String("auth-token", "", "bearer token required on the HTTP endpoint (consider exporting to EMBYFIN_AUTH_TOKEN instead)")
	pflags.Bool("allow-no-auth", false, "serve HTTP with no bearer token: anyone who can reach the port can use every tool")
	pflags.String("tmdb-key", "", "TMDB API key or read access token, enables provider-backed audits (consider exporting to EMBYFIN_TMDB_KEY instead)")

	// binding map for viper/pflag -> env
	m := map[string]string{ //nolint:gosec // G101: these are env var names, not credentials
		"backend":       "EMBYFIN_BACKEND",
		"server":        "EMBYFIN_SERVER",
		"token":         "EMBYFIN_TOKEN",
		"read-only":     "EMBYFIN_READ_ONLY",
		"enable-delete": "EMBYFIN_ENABLE_DELETE",
		"toolsets":      "EMBYFIN_TOOLSETS",
		"allow-tools":   "EMBYFIN_ALLOW_TOOLS",
		"deny-tools":    "EMBYFIN_DENY_TOOLS",
		"listen":        "EMBYFIN_LISTEN",
		"auth-token":    "EMBYFIN_AUTH_TOKEN",
		"allow-no-auth": "EMBYFIN_ALLOW_NO_AUTH",
		"tmdb-key":      "EMBYFIN_TMDB_KEY",
	}

	for name, env := range m {
		if err := viper.BindPFlag(name, pflags.Lookup(name)); err != nil {
			return fmt.Errorf("error binding '%s' flag: %w", name, err)
		}

		if env != "" {
			if err := viper.BindEnv(name, env); err != nil {
				return fmt.Errorf("error binding '%s' to env '%s' : %w", name, env, err)
			}
		}
	}

	viper.SetConfigName(".embyfin-mcp")
	viper.SetConfigType("env")
	// viper reads the first file it finds, so the working directory comes
	// first: a per-project .embyfin-mcp overrides the one in $HOME
	viper.AddConfigPath(".")
	if home, err := os.UserHomeDir(); err == nil {
		viper.AddConfigPath(home)
	}

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := errors.AsType[viper.ConfigFileNotFoundError](err); !ok {
			clog.Log.Errorf("Error reading config file: %v", err)
		}
	}

	return nil
}

// GetFlags returns the fully populated FlagData.
// We must unmarshal from Viper instead of using globally bound pflags variables
// because pflags only parses command-line arguments. Viper merges environment
// variables (and config files) on top of the CLI flags.
func GetFlags() *FlagData {
	var f FlagData
	if err := viper.Unmarshal(&f); err != nil {
		clog.Log.Fatalf("failed to unmarshal configuration: %v", err)
	}

	return &f
}

func (f *FlagData) NewClient() (*embyfin.Client, error) {
	return embyfin.New(embyfin.Backend(strings.ToLower(f.Backend)), f.Server, f.Token)
}

// DefaultToolsets is what the binary registers when --toolsets is not given:
// enough to find things and read them, and nothing that writes. The whole
// surface is thousands of tokens of tool definitions before a question is
// asked, which is a poor thing to spend a client's context on by default.
// Ask for more with --toolsets, or --toolsets all for everything.
var DefaultToolsets = []string{"core"}

// ToolOptions maps the flags onto the tool registration options.
func (f *FlagData) ToolOptions() tools.Options {
	sets := f.Toolsets
	if len(sets) == 0 {
		sets = DefaultToolsets
	}

	return tools.Options{
		ReadOnly:     f.ReadOnly,
		EnableDelete: f.EnableDelete,
		Toolsets:     sets,
		Allow:        f.AllowTools,
		Deny:         f.DenyTools,
		TMDBKey:      f.TMDBKey,
	}
}

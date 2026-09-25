package cli

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/tools"
	"github.com/katbyte/go-kt/clog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	TMDBToken    string   `mapstructure:"tmdb-token"`
	TMDBKey      string   `mapstructure:"tmdb-key"`
	AnimeList    string   `mapstructure:"anime-list"`
}

// envNames binds each flag to its environment variable.
var envNames = map[string]string{ //nolint:gosec // G101: these are env var names, not credentials
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
	"tmdb-token":    "EMBYFIN_TMDB_TOKEN",
	"tmdb-key":      "EMBYFIN_TMDB_KEY",
	"anime-list":    "EMBYFIN_ANIME_LIST",
}

// persistent is the flag set configureFlags bound, for the settings that
// have to be read source by source rather than through viper.
var persistent *pflag.FlagSet

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.StringP("backend", "b", "emby", "media server backend: emby or jellyfin")
	pflags.StringP("server", "s", "", "the media server's url, e.g. http://nas:8096")
	pflags.StringP("token", "t", "", "the media server API key (consider exporting to EMBYFIN_TOKEN instead)")
	pflags.Bool("read-only", false, "register only tools that never change server state")
	pflags.Bool("enable-delete", false, "register the tools that delete: media files, libraries, what a removed library left, playlists and collections")
	pflags.StringSlice("toolsets", nil, "groups of tools to register: all, core (default), curation, watching, organise, remote, admin, or a resource family like item (core is always included)")
	pflags.StringSlice("allow-tools", nil, "only register these tools: names, prefix globs like library_*, or the essential preset; from every tool, or from the --toolsets given")
	pflags.StringSlice("deny-tools", nil, "never register these tools: names or prefix globs like *_delete")
	pflags.String("listen", "", "serve MCP over HTTP on this address (e.g. :8080) instead of stdio")
	pflags.String("auth-token", "", "bearer token required on the HTTP endpoint (consider exporting to EMBYFIN_AUTH_TOKEN instead)")
	pflags.Bool("allow-no-auth", false, "serve HTTP with no bearer token: anyone who can reach the port can use every tool")
	pflags.String("tmdb-token", "", "TMDB API Read Access Token, or the older API Key, enables the provider-backed audits and show_missing's fallback (consider exporting to EMBYFIN_TMDB_TOKEN instead)")
	pflags.String("tmdb-key", "", "the same as --tmdb-token, by its older name")
	pflags.String("anime-list", "", "where audit_anime_ids reads the Anime-Lists mapping from: a URL or a file (default the list on GitHub)")

	persistent = pflags
	m := envNames

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

	// a config file spells a setting the way the environment does, less
	// the prefix - TMDB_TOKEN for --tmdb-token - and viper only matches a key
	// spelled as the flag is, so every two-word setting was ignored. Carried
	// across as defaults, they still lose to a flag or the environment.
	for name := range m {
		if alt := strings.ReplaceAll(name, "-", "_"); alt != name && viper.InConfig(alt) {
			viper.SetDefault(name, viper.Get(alt))
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
	f.TMDBToken = tmdbCredential()

	return &f
}

// tmdbCredential is the TMDB token by either of its names, from the highest
// source that sets one: a flag, then the environment, then the config file.
// viper settles each name on its own, so left to it a TMDB_TOKEN in the file
// would beat a --tmdb-key on the command line.
func tmdbCredential() string {
	names := []string{"tmdb-token", "tmdb-key"}
	sources := []func(name string) string{
		func(name string) string {
			if persistent != nil {
				if fl := persistent.Lookup(name); fl != nil && fl.Changed {
					return fl.Value.String()
				}
			}
			return ""
		},
		func(name string) string { return os.Getenv(envNames[name]) },
		func(name string) string { return viper.GetString(strings.ReplaceAll(name, "-", "_")) },
	}
	for _, source := range sources {
		for _, name := range names {
			if v := source(name); v != "" {
				return v
			}
		}
	}

	return ""
}

func (f *FlagData) NewClient() (*embyfin.Client, error) {
	return embyfin.New(embyfin.Backend(strings.ToLower(f.Backend)), f.Server, f.Token)
}

// DefaultToolsets is what the binary registers when neither --toolsets nor
// --allow-tools is given: enough to find things and read them, and nothing
// that writes. The whole surface is thousands of tokens of tool definitions
// before a question is asked, which is a poor thing to spend a client's
// context on by default. Ask for more with --toolsets, or --toolsets all for
// everything.
var DefaultToolsets = []string{"core"}

// ToolOptions maps the flags onto the tool registration options. An allow
// list with no --toolsets chooses from every tool: it already says which to
// load, and narrowed by the default set too, --allow-tools essential loaded
// three of its five tools.
func (f *FlagData) ToolOptions() tools.Options {
	sets := f.Toolsets
	if len(sets) == 0 && len(f.AllowTools) == 0 {
		sets = DefaultToolsets
	}

	return tools.Options{
		ReadOnly:     f.ReadOnly,
		EnableDelete: f.EnableDelete,
		Toolsets:     sets,
		Allow:        f.AllowTools,
		Deny:         f.DenyTools,
		TMDBKey:      cmp.Or(f.TMDBToken, f.TMDBKey),
		AnimeList:    f.AnimeList,
	}
}

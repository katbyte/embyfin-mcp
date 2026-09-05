package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/clog"
	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type FlagData struct {
	Backend      string `mapstructure:"backend"`
	Server       string `mapstructure:"server"`
	Token        string `mapstructure:"token"`
	EnableDelete bool   `mapstructure:"enable-delete"`
}

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.StringP("backend", "b", "emby", "media server backend: emby or jellyfin")
	pflags.StringP("server", "s", "", "the media server's url, e.g. http://nas:8096")
	pflags.StringP("token", "t", "", "the media server API key (consider exporting to EMBYFIN_TOKEN instead)")
	pflags.Bool("enable-delete", false, "register the item_delete tool, which permanently removes media files")

	// binding map for viper/pflag -> env
	m := map[string]string{ //nolint:gosec // G101: these are env var names, not credentials
		"backend":       "EMBYFIN_BACKEND",
		"server":        "EMBYFIN_SERVER",
		"token":         "EMBYFIN_TOKEN",
		"enable-delete": "EMBYFIN_ENABLE_DELETE",
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
	if home, err := os.UserHomeDir(); err == nil {
		viper.AddConfigPath(home)
	}
	viper.AddConfigPath(".")

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

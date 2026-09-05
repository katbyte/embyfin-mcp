// Package cli implements the embyfin-mcp command line interface: the cobra commands, flag and
// config handling, and the MCP server exposing library curation tools to AI clients.
package cli

import (
	"errors"
	"fmt"

	"github.com/katbyte/embyfin-mcp/lib/version"
	"github.com/katbyte/embyfin-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func ValidateParams(params []string) func(cmd *cobra.Command, args []string) error {
	return func(_ *cobra.Command, _ []string) error {
		for _, p := range params {
			if viper.GetString(p) != "" {
				continue
			}
			return errors.New(p + " parameter can't be empty")
		}

		return nil
	}
}

func Make() (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   "embyfin-mcp [command]",
		Short: "embyfin-mcp is an MCP server and CLI for curating Emby and Jellyfin media libraries",
		Long: `An MCP server (and CLI) for curating Emby and Jellyfin media libraries: search,
inspect, audit, and fix metadata matches from an AI client such as Claude Code.
Complete documentation is available at https://github.com/katbyte/embyfin-mcp`,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println("Run \"embyfin-mcp help\" for more information about available embyfin-mcp commands.")
			return nil
		},
	}

	root.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "Print the version number of embyfin-mcp",
		Long:          `Print the version number of embyfin-mcp`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println("embyfin-mcp " + version.Version)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:           "info",
		Short:         "Check connectivity and print media server info",
		Long:          `Connects to the configured media server and prints its name, version, and operating system.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"server", "token"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			client, err := GetFlags().NewClient()
			if err != nil {
				return err
			}

			i, err := client.SystemInfo(cmd.Context())
			if err != nil {
				return err
			}

			fmt.Printf("%s %s (%s) on %s — id %s\n", client.Backend(), i.ServerName, i.Version, i.OperatingSystem, i.ID)
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:           "serve",
		Short:         "Run the MCP server over stdio",
		Long:          `Runs the MCP server over stdio for AI clients such as Claude Code. Register it in .mcp.json with the EMBYFIN_* environment variables set.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams([]string{"server", "token"}),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			f := GetFlags()
			client, err := f.NewClient()
			if err != nil {
				return err
			}

			server := mcp.NewServer(&mcp.Implementation{
				Name:    "embyfin",
				Title:   "Emby/Jellyfin Library Curator",
				Version: version.Version,
			}, nil)

			tools.RegisterAll(server, client, tools.Options{EnableDelete: f.EnableDelete})

			return server.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	})

	if err := configureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}

	return root, nil
}

// Package main implements embyfin-mcp, an MCP server and CLI for curating Emby and Jellyfin media libraries.
package main

import (
	"os"

	c "github.com/gookit/color"
	"github.com/katbyte/embyfin-mcp/cli"
	"github.com/katbyte/go-kt/clog"
)

func main() {
	// the log level comes from EMBYFIN_LOG; read it once here, before anything logs
	clog.SetLevelFromEnv("EMBYFIN_LOG")

	cmd, err := cli.Make()
	if err != nil {
		clog.Log.Error(c.Sprintf("<red>embyfin-mcp: building cmd</> %v", err))

		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(c.Sprintf("<red>embyfin-mcp:</> %v", err))

		os.Exit(1)
	}

	os.Exit(0)
}

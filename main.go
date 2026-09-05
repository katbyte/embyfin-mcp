// Package main implements embyfin-mcp, an MCP server and CLI for curating Emby and Jellyfin media libraries.
package main

import (
	"os"

	c "github.com/gookit/color"
	"github.com/katbyte/embyfin-mcp/cli"
	"github.com/katbyte/embyfin-mcp/lib/clog"
)

func main() {
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

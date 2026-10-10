package tools

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pluginRunning is Jellyfin's word for a plugin that is loaded and working.
const pluginRunning = "Active"

func registerPluginsTool(r *registry) {
	client := r.client

	type pluginRow struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description,omitempty"`
		Status      string `json:"status,omitempty"      jsonschema:"Jellyfin: Active, Restart (waiting for the server to be restarted), Disabled, NotSupported, Malfunctioned, Superseded or Deleted. Emby says nothing of the kind"`
		Bundled     bool   `json:"bundled,omitempty"     jsonschema:"Jellyfin: the plugin came with the server and cannot be taken out. Emby does not say which of its plugins did"`
	}
	type pluginsOut struct {
		Backend string      `json:"backend"`
		Count   int         `json:"count"`
		Plugins []pluginRow `json:"plugins"`
		Note    string      `json:"note,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_plugins",
		Description: "What is installed into the server that is not part of it: each plugin's name, version and what it says it does, and on Jellyfin whether it is running and whether it came with the server. A plugin is code inside the server: one that has failed, or changed version, is a first thing to rule out when the server misbehaves, and server_info's pending_restart says whether one is waiting for a restart.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, pluginsOut, error) {
		plugins, err := client.Plugins(ctx)
		if err != nil {
			return nil, pluginsOut{}, err
		}
		slices.SortStableFunc(plugins, func(a, b embyfin.Plugin) int {
			return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})

		out := pluginsOut{Backend: string(client.Backend()), Count: len(plugins), Plugins: make([]pluginRow, 0, len(plugins))}
		var notRunning []string
		for _, p := range plugins {
			out.Plugins = append(out.Plugins, pluginRow{Name: p.Name, Version: p.Version, Description: p.Description, Status: p.Status, Bundled: p.Bundled})
			if p.Status != "" && p.Status != pluginRunning {
				notRunning = append(notRunning, fmt.Sprintf("%s (%s)", p.Name, p.Status))
			}
		}
		if len(notRunning) > 0 {
			out.Note = "not running: " + strings.Join(notRunning, ", ")
		}

		return nil, out, nil
	})
}

package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/tools"
)

// The binary registers core and nothing else unless asked, so a client that
// just points embyfin-mcp at a server does not spend thousands of tokens of
// context on tools it will not call.
func TestDefaultToolsetsIsCore(t *testing.T) {
	t.Parallel()

	var f FlagData
	opts := f.ToolOptions()
	if !slices.Equal(opts.Toolsets, []string{"core"}) {
		t.Errorf("default toolsets = %v, want [core]", opts.Toolsets)
	}

	got, err := tools.Describe(opts)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got))
	for _, ti := range got {
		names = append(names, ti.Name)
		if ti.Kind != "read" {
			t.Errorf("%s is %s; the default set should never change server state", ti.Name, ti.Kind)
		}
	}
	want := slices.Clone(tools.Toolsets["core"])
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("default registered %v, want %v", names, want)
	}
}

// An explicit --toolsets replaces the default rather than adding to it, and
// the other flags reach the options.
func TestExplicitToolsetsOverrideTheDefault(t *testing.T) {
	t.Parallel()

	f := FlagData{Toolsets: []string{"all"}, EnableDelete: true, ReadOnly: true, TMDBKey: "k", AllowTools: []string{"a"}, DenyTools: []string{"b"}}
	opts := f.ToolOptions()
	if !opts.EnableDelete || !opts.ReadOnly || opts.TMDBKey != "k" || len(opts.Allow) != 1 || len(opts.Deny) != 1 {
		t.Errorf("options lost a flag: %+v", opts)
	}
	got, err := tools.Describe(tools.Options{Toolsets: opts.Toolsets})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) <= len(tools.Toolsets["core"]) {
		t.Errorf("--toolsets all registered %d tools, want the whole surface", len(got))
	}
}

// An allow list with no --toolsets chooses from every tool: it says which to
// load. Narrowed by the default core as well, --allow-tools essential loaded
// three of its five tools, and the README's library_*,item_get,user_* none of
// the user_ tools, all in silence.
func TestAnAllowListChoosesFromEveryTool(t *testing.T) {
	t.Parallel()

	names := func(f FlagData) []string {
		t.Helper()
		got, err := tools.Describe(f.ToolOptions())
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(got))
		for _, ti := range got {
			out = append(out, ti.Name)
		}

		return out
	}
	essential, organise := []string{"essential"}, []string{"organise"}

	if opts := (&FlagData{AllowTools: essential}).ToolOptions(); len(opts.Toolsets) != 0 {
		t.Errorf("an allow list kept the default toolsets: %v", opts.Toolsets)
	}
	want := slices.Clone(tools.EssentialTools)
	slices.Sort(want)
	if got := names(FlagData{AllowTools: essential}); !slices.Equal(got, want) {
		t.Errorf("--allow-tools essential = %v, want %v", got, want)
	}
	got := names(FlagData{AllowTools: []string{"library_*,item_get,user_*"}})
	for _, name := range []string{"library_list", "library_episodes", "library_scan", "item_get", "user_next_up", "user_list", "user_stats"} {
		if !slices.Contains(got, name) {
			t.Errorf("library_*,item_get,user_* lacks %s: %v", name, got)
		}
	}
	for _, name := range got {
		if !strings.HasPrefix(name, "library_") && !strings.HasPrefix(name, "user_") && name != "item_get" {
			t.Errorf("library_*,item_get,user_* registered %s", name)
		}
	}
	// beside --toolsets it narrows them, and one naming what they do not
	// hold is refused, saying which set to add
	if got := names(FlagData{Toolsets: []string{"watching"}, AllowTools: essential}); !slices.Equal(got, want) {
		t.Errorf("--toolsets watching --allow-tools essential = %v, want %v", got, want)
	}
	if got := names(FlagData{Toolsets: organise, AllowTools: []string{"playlist_*"}}); len(got) == 0 || slices.ContainsFunc(got, func(n string) bool { return !strings.HasPrefix(n, "playlist_") }) {
		t.Errorf("--toolsets organise --allow-tools playlist_* = %v", got)
	}
	_, err := tools.Describe((&FlagData{Toolsets: organise, AllowTools: essential}).ToolOptions())
	if err == nil || !strings.Contains(err.Error(), "user_next_up, item_set_state") || !strings.Contains(err.Error(), "add watching to --toolsets") {
		t.Errorf("--toolsets organise --allow-tools essential = %v", err)
	}
	// with neither, the default is still core
	if opts := (&FlagData{}).ToolOptions(); !slices.Equal(opts.Toolsets, DefaultToolsets) {
		t.Errorf("no flags = %v", opts.Toolsets)
	}
}

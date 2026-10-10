package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// configGroups sorts a server's settings into groups by their names, first
// match first: by rule and not by list, so that both servers' settings fall
// into them and one a server adds lands somewhere sensible without anyone
// naming it here.
var configGroups = []struct {
	name  string
	holds func(setting string) bool
}{
	{"trickplay", func(s string) bool { return strings.HasPrefix(s, "TrickplayOptions") }},
	{"paths", func(s string) bool {
		return strings.HasSuffix(s, "Path") || strings.HasSuffix(s, "Paths") || s == "PathSubstitutions"
	}},
	{"logs", func(s string) bool { return containsAny(s, "Log", "SlowResponse") }},
	{"database", func(s string) bool { return containsAny(s, "Database", "DbConnections", "SqLite") }},
	{"network", func(s string) bool {
		return containsAny(s, "Http", "Port", "Remote", "Network", "Proxy", "UPnP", "WanDdns", "Cors", "Certificate", "IPv6", "Authorization", "Password")
	}},
	{"scanning", func(s string) bool {
		return containsAny(s, "Scan", "Refresh", "LibraryMonitor", "Concurrency", "Chapter", "ImageExtraction", "ImageEncoding", "LibraryUpdate")
	}},
	{"playback", func(s string) bool { return containsAny(s, "Resume", "Bitrate", "StreamLimit", "Session") }},
	{"metadata", func(s string) bool {
		return containsAny(s, "Metadata", "ImageSaving", "Sort", "People", "Grouping", "DisplaySpecials", "FolderView", "TrackTitles", "Culture", "ContentTypes")
	}},
	{"other", func(string) bool { return true }},
}

func containsAny(s string, parts ...string) bool {
	return slices.ContainsFunc(parts, func(p string) bool { return strings.Contains(s, p) })
}

// configGroupOf is the group a setting is listed in.
func configGroupOf(setting string) string {
	for _, g := range configGroups {
		if g.holds(setting) {
			return g.name
		}
	}

	return "other"
}

// configSecret says whether a setting holds a credential, by its name: its
// value is never given.
func configSecret(setting string) bool {
	return containsAny(setting, "Password", "Secret", "Token", "ApiKey")
}

// configFlat lays a settings document out one setting a name: a group of
// settings inside it becomes "Group.Setting", a list of words or numbers
// stays a list, and a list of documents - what fetches metadata for each
// kind of item, where plugins come from - is given whole only when full is
// set, and otherwise as how many it holds. A credential is blanked.
func configFlat(doc map[string]any, full bool) map[string]any {
	out := map[string]any{}
	var lay func(prefix string, v any)
	lay = func(name string, v any) {
		switch val := v.(type) {
		case map[string]any:
			for k, inner := range val {
				lay(name+"."+k, inner)
			}
		case []any:
			if !full && slices.ContainsFunc(val, func(e any) bool { _, deep := e.(map[string]any); return deep }) {
				out[name] = fmt.Sprintf("%d entries, each a group of settings: ask with full to see them", len(val))

				return
			}
			out[name] = val
		case string:
			if configSecret(name) && val != "" {
				val = "***"
			}
			out[name] = val
		default:
			out[name] = v
		}
	}
	for k, v := range doc {
		lay(k, v)
	}

	return out
}

// configRule says what one editable setting may be set to.
type configRule struct {
	// kind is "switch", "number" or "numbers"
	kind string
	// least and most bound a number, or each of a list of them; most 0 is
	// no upper bound
	least, most int64
	// about says what the setting does, for the refusal that names it
	about string
}

// jellyfinEditable are the settings server_config_edit changes on Jellyfin:
// the ones that tune how hard and how the server works at making trickplay
// images and at scanning, where a wrong value costs speed or a failed run
// and no data. The bounds are the ones Jellyfin's own dashboard puts on each
// field. Nothing else in the document is changed through this tool: it also
// holds where the server keeps its data, where it fetches plugins from, and
// who may reach it.
var jellyfinEditable = map[string]configRule{
	"TrickplayOptions.EnableHwAcceleration":         {kind: "switch", about: "decode on hardware when making trickplay images"},
	"TrickplayOptions.EnableHwEncoding":             {kind: "switch", about: "encode the images on hardware"},
	"TrickplayOptions.EnableKeyFrameOnlyExtraction": {kind: "switch", about: "take images from key frames only: much faster, less evenly spaced"},
	"TrickplayOptions.ProcessThreads":               {kind: "number", least: 0, about: "threads ffmpeg is given, 0 for its own choice"},
	"TrickplayOptions.Interval":                     {kind: "number", least: 1, about: "milliseconds between images"},
	"TrickplayOptions.TileWidth":                    {kind: "number", least: 1, about: "images across one tile sheet"},
	"TrickplayOptions.TileHeight":                   {kind: "number", least: 1, about: "images down one tile sheet"},
	"TrickplayOptions.Qscale":                       {kind: "number", least: 2, most: 31, about: "ffmpeg's quality scale for the images, 2 the best"},
	"TrickplayOptions.JpegQuality":                  {kind: "number", least: 1, most: 100, about: "JPEG quality of the tile sheets"},
	"TrickplayOptions.WidthResolutions":             {kind: "numbers", least: 1, about: "the widths, in pixels, images are made at"},
	"LibraryScanFanoutConcurrency":                  {kind: "number", least: 0, about: "how many folders a scan reads at once, 0 for the server's own choice"},
	"LibraryMetadataRefreshConcurrency":             {kind: "number", least: 0, about: "how many items a scan refreshes at once, 0 for the server's own choice"},
}

// whole reads a JSON number as a whole number.
func whole(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()

		return i, err == nil
	case float64:
		if n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return 0, false
		}

		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}

// check reads a value given for a setting as what the rule allows, in the
// form it is sent to the server in.
func (r configRule) check(name string, v any) (any, error) {
	bounded := func(n int64) error {
		if n < r.least || (r.most > 0 && n > r.most) {
			if r.most > 0 {
				return fmt.Errorf("%s (%s) takes %d to %d, not %d", name, r.about, r.least, r.most, n)
			}

			return fmt.Errorf("%s (%s) takes %d or more, not %d", name, r.about, r.least, n)
		}

		return nil
	}
	switch r.kind {
	case "switch":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s (%s) is on or off: give true or false, not %v", name, r.about, v)
		}

		return b, nil
	case "number":
		n, ok := whole(v)
		if !ok {
			return nil, fmt.Errorf("%s (%s) takes a whole number, not %v", name, r.about, v)
		}

		return n, bounded(n)
	default:
		list, ok := v.([]any)
		if !ok || len(list) == 0 {
			return nil, fmt.Errorf("%s (%s) takes a list of whole numbers with at least one in it, not %v", name, r.about, v)
		}
		out := make([]any, 0, len(list))
		for _, e := range list {
			n, ok := whole(e)
			if !ok {
				return nil, fmt.Errorf("%s (%s) takes a list of whole numbers, and %v is not one", name, r.about, e)
			}
			if err := bounded(n); err != nil {
				return nil, err
			}
			out = append(out, n)
		}

		return out, nil
	}
}

// configAt finds a setting in a document by its flat name, with the group
// that holds it, so that it can be read and set in place.
func configAt(doc map[string]any, name string) (holder map[string]any, key string, found bool) {
	holder = doc
	parts := strings.Split(name, ".")
	for _, p := range parts[:len(parts)-1] {
		inner, ok := holder[p].(map[string]any)
		if !ok {
			return nil, "", false
		}
		holder = inner
	}
	key = parts[len(parts)-1]
	_, found = holder[key]

	return holder, key, found
}

// sameSetting holds two values of a setting against each other as the
// server's JSON would spell them.
func sameSetting(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)

	return err1 == nil && err2 == nil && slices.Equal(x, y)
}

func registerConfigTools(r *registry) {
	client := r.client

	type configIn struct {
		Groups []string `json:"groups,omitempty" jsonschema:"the groups to give: trickplay, paths, logs, database, network, scanning, playback, metadata, other. Empty gives every group"`
		Full   bool     `json:"full,omitempty"   jsonschema:"give the settings that are lists of groups whole - what fetches metadata for each kind of item, where plugins come from - where by default only how many each holds is said"`
	}
	type configOut struct {
		Backend  string                    `json:"backend"`
		Groups   map[string]map[string]any `json:"groups"         jsonschema:"the settings by group, each by the server's own name for it; one inside a group of settings is named Group.Setting. A credential is given as ***"`
		Editable []string                  `json:"editable"       jsonschema:"the settings server_config_edit changes on this server; empty on a server it changes none on"`
		Note     string                    `json:"note,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_config",
		Description: "The server's own settings, in groups, each by the server's name for it: how it makes trickplay images, how hard it scans, what it logs and for how long, where it keeps things, who may reach it. These are the server-wide settings; a library's own are library_get's. editable names the ones server_config_edit can change. A setting that is a credential is never given.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in configIn) (*mcp.CallToolResult, configOut, error) {
		known := make([]string, 0, len(configGroups))
		for _, g := range configGroups {
			known = append(known, g.name)
		}
		want := map[string]bool{}
		for _, g := range in.Groups {
			g = strings.ToLower(strings.TrimSpace(g))
			if !slices.Contains(known, g) {
				return nil, configOut{}, fmt.Errorf("no group %q: groups are %s", g, strings.Join(known, ", "))
			}
			want[g] = true
		}
		doc, err := client.ServerConfig(ctx)
		if err != nil {
			return nil, configOut{}, err
		}

		out := configOut{Backend: string(client.Backend()), Groups: map[string]map[string]any{}, Editable: []string{}}
		for name, value := range configFlat(doc, in.Full) {
			group := configGroupOf(name)
			if len(want) > 0 && !want[group] {
				continue
			}
			if out.Groups[group] == nil {
				out.Groups[group] = map[string]any{}
			}
			out.Groups[group][name] = value
		}
		if client.Backend() == embyfin.Jellyfin {
			out.Editable = slices.Sorted(maps.Keys(jellyfinEditable))
		} else {
			out.Note = "none of Emby's server settings is changed by server_config_edit so far; what it keeps for making preview thumbnails is each library's own setting"
		}

		return nil, out, nil
	})

	type configEditIn struct {
		Set map[string]any `json:"set" jsonschema:"the settings to change and what to: each by its name as server_config's editable lists it, a switch as true or false, a number as a whole number, a list of numbers as a list. Settings not named here are left exactly as they are"`
	}
	type configChange struct {
		Setting string `json:"setting"`
		Before  any    `json:"before"`
		After   any    `json:"after"   jsonschema:"as the server gives it after the change"`
	}
	type configEditOut struct {
		Changed        []configChange `json:"changed"`
		Unchanged      []string       `json:"unchanged"       jsonschema:"settings that already had the value asked for"`
		PendingRestart bool           `json:"pending_restart" jsonschema:"the server says a change is waiting for it to be restarted"`
		Note           string         `json:"note,omitempty"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "server_config_edit",
		Description: "Change the server-wide settings that tune how the server works, and no others, on Jellyfin: how it makes trickplay images (on hardware or not, key frames only, threads, interval, tile size, quality, widths) and how many folders and items a scan handles at once. server_config's editable lists them. " +
			"Each value is checked against the bounds Jellyfin's own dashboard puts on it before anything is sent, every other setting is sent back exactly as the server gave it, and the answer gives each changed setting before and after, read back from the server. " +
			"A wrong value here costs speed or a failed run of a task, not data: the settings that say where the server keeps its data, where it fetches plugins from and who may reach it are not on the list, and are refused by name. The trickplay settings take effect for images made from then on; images already made are not made again.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in configEditIn) (*mcp.CallToolResult, configEditOut, error) {
		if client.Backend() != embyfin.Jellyfin {
			return nil, configEditOut{}, errors.New("none of Emby's server settings is changed by this tool so far: it changes Jellyfin's trickplay and scan settings. Nothing was changed")
		}
		if len(in.Set) == 0 {
			return nil, configEditOut{}, fmt.Errorf("set is what to change: name at least one of %s", strings.Join(slices.Sorted(maps.Keys(jellyfinEditable)), ", "))
		}
		// every value is checked before anything is read or sent
		values := map[string]any{}
		for _, name := range slices.Sorted(maps.Keys(in.Set)) {
			rule, ok := jellyfinEditable[name]
			if !ok {
				return nil, configEditOut{}, fmt.Errorf("%s is not a setting this tool changes: it changes %s. Nothing was changed", name, strings.Join(slices.Sorted(maps.Keys(jellyfinEditable)), ", "))
			}
			value, err := rule.check(name, in.Set[name])
			if err != nil {
				return nil, configEditOut{}, fmt.Errorf("%w. Nothing was changed", err)
			}
			values[name] = value
		}

		before, after, err := client.EditServerConfig(ctx, func(doc map[string]any) error {
			changed := false
			for _, name := range slices.Sorted(maps.Keys(values)) {
				holder, key, found := configAt(doc, name)
				if !found {
					return fmt.Errorf("this server's settings hold no %s, so it was not set and nothing was changed: its version may not have it (server_config lists what it has)", name)
				}
				if !sameSetting(holder[key], values[name]) {
					holder[key], changed = values[name], true
				}
			}
			// nothing to change is nothing to send: the server is not
			// asked to save what it already has
			if !changed {
				return embyfin.ErrConfigUnchanged
			}

			return nil
		})
		if err != nil {
			return nil, configEditOut{}, err
		}

		out := configEditOut{Changed: []configChange{}, Unchanged: []string{}}
		for _, name := range slices.Sorted(maps.Keys(values)) {
			wasIn, wasKey, _ := configAt(before, name)
			nowIn, nowKey, _ := configAt(after, name)
			was, now := wasIn[wasKey], nowIn[nowKey]
			if !sameSetting(now, values[name]) {
				return nil, configEditOut{}, fmt.Errorf("the server took the change and gives %s as %v, not the %v asked for", name, now, values[name])
			}
			if sameSetting(was, now) {
				out.Unchanged = append(out.Unchanged, name)

				continue
			}
			out.Changed = append(out.Changed, configChange{Setting: name, Before: was, After: now})
		}
		if info, err := client.SystemInfo(ctx); err == nil {
			out.PendingRestart = info.HasPendingRestart
		}
		if len(out.Changed) == 0 {
			out.Note = "every setting named already had the value asked for"
		}

		return nil, out, nil
	})
}

package tools

import (
	"net/http"
	"strings"
	"testing"
)

// The plugins are listed by name with what each says of itself; Jellyfin
// also says which are running and which came with it, and the ones that are
// not running are named in the note. Emby says neither.
func TestServerPlugins(t *testing.T) {
	t.Parallel()

	jf := newFakeServer(t)
	jf.jellyfin = true
	jf.mux.HandleFunc("GET /Plugins", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{
			{"Name": "zzyzx Lyrics", "Version": "3.0.0.0", "Description": "Fetches lyrics.", "Id": "p3", "CanUninstall": true, "Status": "Malfunctioned"},
			{"Name": "Quux Metadata", "Version": "12.2.0.0", "Description": "Fetches metadata.", "Id": "p1", "CanUninstall": false, "Status": "Active"},
			{"Name": "Plugh Sync", "Version": "1.4.0.0", "Id": "p2", "CanUninstall": true, "Status": "Restart"},
		})
	})
	out := mustCall(t, session(t, jf, Options{}), "server_plugins", map[string]any{})
	rows := objects(t, out["plugins"], "plugins")
	if out["backend"] != "jellyfin" || number(t, out["count"], "count") != 3 || len(rows) != 3 {
		t.Fatalf("server_plugins = %v", out)
	}
	if rows[0]["name"] != "Plugh Sync" || rows[0]["status"] != "Restart" || rows[0]["bundled"] != nil || rows[0]["description"] != nil {
		t.Errorf("the first by name = %v", rows[0])
	}
	if rows[1]["name"] != "Quux Metadata" || rows[1]["version"] != "12.2.0.0" || rows[1]["description"] != "Fetches metadata." || rows[1]["status"] != "Active" || !boolean(t, rows[1]["bundled"], "bundled") {
		t.Errorf("one that came with the server = %v", rows[1])
	}
	if note := text(out["note"]); note != "not running: Plugh Sync (Restart), zzyzx Lyrics (Malfunctioned)" {
		t.Errorf("note = %q", note)
	}

	emby := newFakeServer(t)
	emby.mux.HandleFunc("GET /Plugins", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{{"Name": "Zzyzx Backup", "Version": "1.8.6.0", "Description": "Backs the server up", "Id": "e1", "ImageTag": "1"}})
	})
	out = mustCall(t, session(t, emby, Options{}), "server_plugins", map[string]any{})
	rows = objects(t, out["plugins"], "plugins")
	if out["backend"] != "emby" || len(rows) != 1 || rows[0]["name"] != "Zzyzx Backup" || rows[0]["version"] != "1.8.6.0" || rows[0]["status"] != nil || rows[0]["bundled"] != nil || out["note"] != nil {
		t.Errorf("on Emby = %v", out)
	}
	if strings.Contains(string(mustJSON(t, out)), "e1") {
		t.Errorf("a plugin's id is not something a tool takes, and is given: %v", out)
	}
}

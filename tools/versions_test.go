package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// adminView answers what an Emby tool reads in the first administrator's
// view from the fake's own library: the account, u1, an administrator who
// sees every library; the list in its view, which is the library's own list
// with no version merged away; and the single read of an item, which is that
// list's answer for its id.
func adminView(t *testing.T, f *fakeServer) {
	t.Helper()

	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []any{map[string]any{"Id": "u1", "Name": "Quux", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}}, "TotalRecordCount": 1})
	})
	f.mux.HandleFunc("GET /Users/u1/Items", func(w http.ResponseWriter, r *http.Request) {
		view := r.Clone(r.Context())
		view.URL.Path = "/Items"
		f.mux.ServeHTTP(w, view)
	})
	f.mux.HandleFunc("GET /Users/u1/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		list := r.Clone(r.Context())
		list.URL.Path, list.URL.RawQuery = "/Items", url.Values{"Ids": {r.PathValue("id")}}.Encode()
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, list)
		var page struct{ Items []map[string]any }
		_ = json.Unmarshal(rec.Body.Bytes(), &page)
		for _, it := range page.Items {
			if it["Id"] == r.PathValue("id") {
				writeJSON(t, w, it)

				return
			}
		}
		http.NotFound(w, r)
	})
}

// Emby merges two files of one film into one item with two versions, but
// only in a user's view: a sweep of /Items holds each file as an item of its
// own, and a list in a user's view hides one of them without listing the
// other's versions. Only the single item read in a user's view names both
// (seen live on Emby 4.10). So on Emby the versions audit reads what Emby
// shows people, and the duplicates audit leaves two entries Emby shows as one
// item's versions out of its groups.
func TestEmbyVersionsAsEmbyShowsThem(t *testing.T) {
	t.Parallel()

	source := func(label string) map[string]any {
		return map[string]any{"Path": "/zz/films/Zzyzx (2001)/Zzyzx (2001) - " + label + ".mkv", "Name": label}
	}
	entry := func(id, label string) map[string]any {
		return map[string]any{
			"Id": id, "Name": "Zzyzx", "Type": "Movie", "ProductionYear": 2001, "ProviderIds": map[string]any{"Tmdb": "78"},
			"Path": "/zz/films/Zzyzx (2001)/Zzyzx (2001) - " + label + ".mkv", "MediaSources": []map[string]any{source(label)},
		}
	}
	other := map[string]any{
		"Id": "32", "Name": "Plugh", "Type": "Movie", "ProductionYear": 2016, "ProviderIds": map[string]any{"Tmdb": "329865"},
		"Path": "/zz/films/Plugh (2016)/Plugh (2016).mkv", "MediaSources": []map[string]any{{"Path": "/zz/films/Plugh (2016)/Plugh (2016).mkv"}},
	}

	f, _ := zzyzxServer(t)
	// every file, one item each
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(entry("29", "1080p"), entry("30", "2160p"), other))
	})
	// the administrator's view: the 2160p file is one of the 1080p item's
	// versions, and not listed
	f.mux.HandleFunc("GET /Users/u1/Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(entry("29", "1080p"), other))
	})
	// the hidden one read on its own names every version
	f.mux.HandleFunc("GET /Users/u1/Items/30", func(w http.ResponseWriter, _ *http.Request) {
		it := entry("30", "2160p")
		it["MediaSources"] = []map[string]any{source("2160p"), source("1080p")}
		writeJSON(t, w, it)
	})
	cs := session(t, f, Options{})

	out, msg := callTool(t, cs, "audit_multiple_versions", map[string]any{"library": "Zzyzx Films"})
	if msg != "" {
		t.Fatal(msg)
	}
	found := objects(t, out["findings"], "findings")
	if len(found) != 1 || found[0]["id"] != "29" || !strings.HasPrefix(text(found[0]["detail"]), "2 versions: Zzyzx (2001) - 2160p.mkv, Zzyzx (2001) - 1080p.mkv") {
		t.Errorf("audit_multiple_versions = %v, want the one item Emby shows with both versions", found)
	}
	if n := number(t, out["items_scanned"], "items_scanned"); n != 2 {
		t.Errorf("items_scanned = %d, want the 2 items Emby shows", n)
	}

	out, msg = callTool(t, cs, "audit_duplicates", map[string]any{"library": "Zzyzx Films", "types": "Movie"})
	if msg != "" {
		t.Fatal(msg)
	}
	if n := number(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("audit_duplicates = %v, want the two versions of one item left out", out["groups"])
	}
	if n := number(t, out["items_scanned"], "items_scanned"); n != 2 {
		t.Errorf("audit_duplicates items_scanned = %d, want 2", n)
	}
}

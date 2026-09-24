package tools

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// What counts as a piece of a disc rather than a film.
func TestDiscRoot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ path, root, kind string }{
		// a Blu-ray flattened into the film's folder
		{"/m/Iron Gate (1968)/Iron Gate (1968)/00000.m2ts", "/m/Iron Gate (1968)/Iron Gate (1968)", "flattened blu-ray"},
		{"/m/Iron Gate (1968)/Iron Gate (1968)/00003.M2TS", "/m/Iron Gate (1968)/Iron Gate (1968)", "flattened blu-ray"},
		// a DVD flattened the same way
		{"/m/Doc (1999)/VTS_01_1.VOB", "/m/Doc (1999)", "flattened dvd"},
		// a disc that kept its structure: the folder holding it is the disc,
		// and an item inside it is the server reaching past the disc
		{"/m/Film (2001)/BDMV/STREAM/00000.m2ts", "/m/Film (2001)", "inside a disc structure"},
		{"/m/Film (2001)/VIDEO_TS/VTS_01_1.VOB", "/m/Film (2001)", "inside a disc structure"},
		{`D:\Films\Film (2001)\BDMV\STREAM\00000.m2ts`, "D:/Films/Film (2001)", "inside a disc structure"},
		// ordinary files, whatever they are named
		{"/m/Film (1999)/Film (1999).mkv", "", ""},
		{"/m/Film (1999)/12345.mkv", "", ""},
		{"/m/Film (1999)/Film 00000.m2ts", "", ""},
	} {
		root, kind, isDisc := discRoot(tc.path)
		if isDisc != (tc.kind != "") || root != tc.root || kind != tc.kind {
			t.Errorf("discRoot(%q) = %q, %q, %v; want %q, %q", tc.path, root, kind, isDisc, tc.root, tc.kind)
		}
	}
}

// discItem is an item as the server lists one, as much as this audit reads.
type discItem struct {
	ID          string            `json:"Id"`
	Name        string            `json:"Name"`
	Type        string            `json:"Type"`
	Path        string            `json:"Path"`
	RunTimeTick int64             `json:"RunTimeTicks,omitempty"`
	ProviderIDs map[string]string `json:"ProviderIds,omitempty"`
	MediaSource []struct {
		Size int64 `json:"Size"`
	} `json:"MediaSources,omitempty"`
}

// One disc, several films. The shape a real library had: a Blu-ray's streams
// left in the film's folder, each matched on its own, so a four-minute clip
// sits in the library under another film's name.
func TestAuditDiscFolders(t *testing.T) {
	t.Parallel()

	rows := []discItem{
		{ID: "a", Name: "00000", Type: "Movie", Path: "/m/s/Iron Gate (1968)/Iron Gate (1968)/00000.m2ts", ProviderIDs: map[string]string{"Tmdb": "770001"}},
		{ID: "b", Name: "A Different Film", Type: "Movie", Path: "/m/s/Iron Gate (1968)/Iron Gate (1968)/00001.m2ts", RunTimeTick: 60 * 10_000_000, ProviderIDs: map[string]string{"Tmdb": "770002"}},
		{ID: "c", Name: "Another Film", Type: "Movie", Path: "/m/s/Iron Gate (1968)/Iron Gate (1968)/00003.m2ts", RunTimeTick: 240 * 10_000_000, ProviderIDs: map[string]string{"Imdb": "tt7700003"}},
		// a DVD in another folder, all of it matched to the one title
		{ID: "d", Name: "VTS_01_1", Type: "Video", Path: "/m/d/Doc (1999)/VTS_01_1.VOB", ProviderIDs: map[string]string{"Tmdb": "42"}},
		{ID: "e", Name: "VTS_01_2", Type: "Video", Path: "/m/d/Doc (1999)/VTS_01_2.VOB", ProviderIDs: map[string]string{"Tmdb": "42"}},
		// a disc the server holds whole: nothing to report
		{ID: "f", Name: "Kept Whole", Type: "Movie", Path: "/m/k/Kept (2001)"},
		// and an ordinary film
		{ID: "g", Name: "Ordinary", Type: "Movie", Path: "/m/o/Ordinary (2010)/Ordinary (2010).mkv"},
	}
	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		page := rows[min(start, len(rows)):]
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": page, "TotalRecordCount": len(rows)})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_disc_folders", map[string]any{})
	if number(t, out["items_scanned"], "items_scanned") != len(rows) || number(t, out["total_findings"], "total_findings") != 2 {
		t.Fatalf("out = %v", out)
	}
	folders := objects(t, out["folders"], "folders")
	if len(folders) != 2 {
		t.Fatalf("folders = %v", folders)
	}

	// the disc with the most pieces first
	disc := folders[0]
	if text(disc["folder"]) != "/m/s/Iron Gate (1968)/Iron Gate (1968)" || text(disc["kind"]) != "flattened blu-ray" || number(t, disc["items"], "items") != 3 {
		t.Errorf("the flattened blu-ray = %v", disc)
	}
	if !strings.Contains(text(disc["note"]), "3 different titles") {
		t.Errorf("the pieces were matched to three titles, and the note says %q", text(disc["note"]))
	}
	entries := objects(t, disc["entries"], "entries")
	if len(entries) != 3 || text(entries[0]["file"]) != "00000.m2ts" || text(entries[2]["file"]) != "00003.m2ts" {
		t.Errorf("entries = %v", entries)
	}
	if text(entries[2]["matched_to"]) != "imdb:tt7700003" || number(t, entries[2]["runtime_s"], "runtime_s") != 240 {
		t.Errorf("a four-minute clip under another film's name = %v", entries[2])
	}

	// one title across a whole DVD is the disc read correctly, so no note
	dvd := folders[1]
	if text(dvd["folder"]) != "/m/d/Doc (1999)" || text(dvd["kind"]) != "flattened dvd" || dvd["note"] != nil {
		t.Errorf("the dvd = %v", dvd)
	}

	// the sweep reads in the cheap order and in big pages: this one reads
	// every episode on a server, where the default order costs half an hour
	for _, req := range f.requests("/Items") {
		if !strings.Contains(req.Query, "SortBy=DateCreated%2CSortName") || !strings.Contains(req.Query, "Limit=10000") {
			t.Errorf("the sweep reads in the slow order or small pages: %s", req.Query)
		}
	}

	// a disc held whole, and an ordinary film, are not findings
	for _, folder := range folders {
		if strings.Contains(text(folder["folder"]), "Kept") || strings.Contains(text(folder["folder"]), "Ordinary") {
			t.Errorf("reported something that is not a disc: %v", folder)
		}
	}
}

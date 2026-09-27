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
		// a camcorder's footage is numbered the same way and even keeps a
		// BDMV folder, under PRIVATE/AVCHD, but its clips are .MTS and they
		// are home videos, not a disc's menu and trailers
		{"/home/Zzyzx Holiday/PRIVATE/AVCHD/BDMV/STREAM/00000.MTS", "", ""},
		{`D:\Home\Zzyzx Holiday\PRIVATE\AVCHD\BDMV\STREAM\00001.MTS`, "", ""},
		{"/home/Zzyzx Holiday/00002.MTS", "", ""},
		{"/home/Zzyzx Holiday/00003.mts", "", ""},
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

	// the sweep reads in the cheap order and in big pages (and the overlap
	// each request re-reads): this one reads every episode on a server,
	// where the default order costs half an hour
	for _, req := range f.requests("/Items") {
		if !strings.Contains(req.Query, "SortBy=DateCreated%2CSortName") || !strings.Contains(req.Query, "Limit=10100") {
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

// Pieces of one disc sharing any id are one title. The feature matched by
// its TMDB and IMDb ids and a trailer matched by the TMDB id alone were
// counted as two titles, so a disc read right was reported as matched to the
// wrong film. A piece matched to an id nothing else carries is still another
// title, and a film's TMDB number is not an episode's.
func TestAuditDiscFoldersCountsTitlesByAnySharedID(t *testing.T) {
	t.Parallel()

	rows := []discItem{
		{ID: "a", Name: "Iron Gate", Type: "Movie", Path: "/m/Iron Gate (1968)/00000.m2ts", ProviderIDs: map[string]string{"Tmdb": "770001", "Imdb": "tt7700001"}},
		{ID: "b", Name: "Iron Gate", Type: "Movie", Path: "/m/Iron Gate (1968)/00001.m2ts", ProviderIDs: map[string]string{"Tmdb": "770001"}},
		{ID: "c", Name: "Iron Gate", Type: "Movie", Path: "/m/Iron Gate (1968)/00002.m2ts", ProviderIDs: map[string]string{"Imdb": "tt7700001"}},
		{ID: "d", Name: "00003", Type: "Movie", Path: "/m/Iron Gate (1968)/00003.m2ts"},
		// a second disc: two pieces matched to the feature, one to another
		// film, and one an episode carrying the feature's TMDB number, which
		// as an episode's is another title altogether
		{ID: "e", Name: "Doc", Type: "Movie", Path: "/m/Doc (1999)/VTS_01_1.VOB", ProviderIDs: map[string]string{"Tmdb": "42", "Imdb": "tt0000042"}},
		{ID: "f", Name: "Doc", Type: "Movie", Path: "/m/Doc (1999)/VTS_01_2.VOB", ProviderIDs: map[string]string{"Imdb": "tt0000042"}},
		{ID: "g", Name: "Another Film", Type: "Movie", Path: "/m/Doc (1999)/VTS_02_1.VOB", ProviderIDs: map[string]string{"Tmdb": "43"}},
		{ID: "h", Name: "An Episode", Type: "Episode", Path: "/m/Doc (1999)/VTS_03_1.VOB", ProviderIDs: map[string]string{"Tmdb": "42"}},
		// a third disc: two films of one TMDB collection, sharing its id and
		// a placeholder, are two titles
		{ID: "i", Name: "Zzyzx One", Type: "Movie", Path: "/m/Saga (2001)/00000.m2ts", ProviderIDs: map[string]string{"Tmdb": "501", "TmdbCollection": "900", "Imdb": "0"}},
		{ID: "j", Name: "Zzyzx Two", Type: "Movie", Path: "/m/Saga (2001)/00001.m2ts", ProviderIDs: map[string]string{"Tmdb": "502", "TmdbCollection": "900", "Imdb": "0"}},
	}
	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, page()) })
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		start := startIndex(t, r.URL.Query())
		writeJSON(t, w, map[string]any{"Items": rows[min(start, len(rows)):], "TotalRecordCount": len(rows)})
	})

	out := mustCall(t, session(t, f, Options{}), "audit_disc_folders", map[string]any{})
	notes := map[string]any{}
	for _, folder := range objects(t, out["folders"], "folders") {
		notes[text(folder["folder"])] = folder["note"]
	}
	if len(notes) != 3 {
		t.Fatalf("folders = %v", out["folders"])
	}
	if note := text(notes["/m/Saga (2001)"]); !strings.Contains(note, "matched to 2 different titles") {
		t.Errorf("two films of one collection = %q, want 2 titles", note)
	}
	if note, noted := notes["/m/Iron Gate (1968)"]; !noted || note != nil {
		t.Errorf("one film matched three ways, and a piece matched to nothing, noted as several titles: %v", note)
	}
	if note := text(notes["/m/Doc (1999)"]); !strings.Contains(note, "matched to 3 different titles, so at least 2") {
		t.Errorf("the feature, another film and an episode = %q, want 3 titles", note)
	}
}

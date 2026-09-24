package tools

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// A DVD rip's frame says nothing about its shape; the ratio the file states
// does, and every quality row carries it with the width it is shown at.
func TestQualityRowsCarryTheStatedShape(t *testing.T) {
	t.Parallel()

	dvd := &embyfin.Item{MediaSources: []embyfin.MediaSource{{Container: "mkv", Size: 1, MediaStreams: []embyfin.MediaStream{
		{Type: "Video", Codec: "mpeg2video", Width: 720, Height: 480, AspectRatio: "16:9"},
	}}}}
	q := qualityOf(dvd)
	if q.AspectRatio != "16:9" || q.DisplayWidth != 853 {
		t.Errorf("an anamorphic DVD = %+v, want aspect 16:9 shown at 853 wide", q)
	}
	hd := &embyfin.Item{MediaSources: []embyfin.MediaSource{{Container: "mkv", Size: 1, MediaStreams: []embyfin.MediaStream{
		{Type: "Video", Codec: "h264", Width: 1920, Height: 1080, AspectRatio: "16:9"},
	}}}}
	if hq := qualityOf(hd); hq.AspectRatio != "16:9" || hq.DisplayWidth != 0 {
		t.Errorf("a frame already 16:9 = %+v, want no display width", hq)
	}
	if uq := qualityOf(&embyfin.Item{MediaSources: []embyfin.MediaSource{{Size: 1, MediaStreams: []embyfin.MediaStream{{Type: "Video", Width: 720, Height: 480}}}}}); uq.AspectRatio != "" || uq.DisplayWidth != 0 {
		t.Errorf("a file stating no ratio = %+v, want the shape left unknown", uq)
	}
	// and the facts can be asked for by name, and dropped
	keep, err := keptFacts([]string{"aspect_ratio", "display_width"})
	if err != nil {
		t.Fatal(err)
	}
	q = qualityOf(dvd)
	q.keepOnly(keep)
	if q.AspectRatio != "16:9" || q.DisplayWidth != 853 || q.Width != 0 {
		t.Errorf("narrowed = %+v", q)
	}
}

// quality_compare compares the shape the file states when it has one: a
// 720x480 anamorphic 16:9 DVD against a 1280x720 file is the same shape,
// not "different frame shapes"; the same frame stating nothing is compared
// by its frame, and the answer says which it used.
func TestQualityCompareUsesTheStatedShape(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})
	out := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 720, "height": 480, "aspect_ratio": "16:9", "video_codec": "mpeg2video", "bitrate": 6000000},
		"b": map[string]any{"width": 1280, "height": 720, "video_codec": "h264", "bitrate": 4000000},
	})
	if strings.Contains(strings.Join(texts(out["caveats"]), " "), "shapes") {
		t.Errorf("an anamorphic 16:9 DVD against a 16:9 file was called a different shape: %v", out)
	}
	a := object(t, out["a"], "a")
	if aspect, ok := a["aspect"].(float64); !ok || text(a["aspect_from"]) != "stated" || aspect != 1.78 {
		t.Errorf("a = %v, want the stated 16:9", a)
	}
	b := object(t, out["b"], "b")
	if text(b["aspect_from"]) != "frame" {
		t.Errorf("b = %v, want the frame's shape", b)
	}

	unknown := mustCall(t, cs, "quality_compare", map[string]any{
		"a": map[string]any{"width": 720, "height": 480, "video_codec": "mpeg2video", "bitrate": 6000000},
		"b": map[string]any{"width": 1280, "height": 720, "video_codec": "h264", "bitrate": 4000000},
	})
	if !strings.Contains(strings.Join(texts(unknown["caveats"]), " "), "shapes") {
		t.Errorf("a DVD frame stating no ratio was not remarked on against a 16:9 file: %v", unknown)
	}
}

// user_get carries the account's playback preferences, which decide whether
// a file whose first audio track is in another language plays right for it.
func TestUserGetPlaybackPreferences(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Id":"admin","Name":"root","Policy":{"IsAdministrator":true},"Configuration":{"AudioLanguagePreference":"eng","SubtitleLanguagePreference":"eng","SubtitleMode":"OnlyForced","PlayDefaultAudioTrack":true}}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
	})
	f.mux.HandleFunc("GET /Users/admin/Items", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
	})
	out := mustCall(t, session(t, f, Options{}), "user_get", map[string]any{"user": "root"})
	if text(out["audio_language"]) != "eng" || text(out["subtitle_language"]) != "eng" || text(out["subtitle_mode"]) != "OnlyForced" || !boolean(t, out["play_default_audio_track"], "play_default_audio_track") {
		t.Errorf("user_get = %v", out)
	}
}

// library_export writes the library to a file, one row a line, and touches
// nothing else: it refuses a file that exists, narrows the rows to the facts
// asked for, and leaves no half a file behind.
func TestLibraryExport(t *testing.T) {
	t.Parallel()

	s := severance()
	cs := session(t, tvServer(t, s), Options{})
	path := filepath.Join(t.TempDir(), "out", "shows.jsonl")
	out := mustCall(t, cs, "library_export", map[string]any{"path": path, "library": "Shows"})
	withFile := 0
	for _, e := range s.episodes {
		if e.path != "" && !e.missing {
			withFile++
		}
	}
	if number(t, out["rows"], "rows") != withFile || text(out["shape"]) != "episode row" || number(t, out["bytes"], "bytes") <= 0 {
		t.Errorf("library_export = %v, want %d episode rows", out, withFile)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a path this test chose
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for sc := bufio.NewScanner(strings.NewReader(string(raw))); sc.Scan(); lines++ {
		var row map[string]any
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("line %d is not JSON: %v", lines+1, err)
		}
		if lines == 0 && (row["series"] == nil || row["width"] == nil || row["path"] == nil) {
			t.Errorf("first row = %v, want a full episode row", row)
		}
	}
	if lines != withFile {
		t.Errorf("the file holds %d lines, want %d", lines, withFile)
	}

	// a file that exists is refused, and there is no flag to write over it
	if msg := mustRefuse(t, cs, "library_export", map[string]any{"path": path, "library": "Shows"}); !strings.Contains(msg, "exists") {
		t.Errorf("writing over a file = %q", msg)
	}
	mustRefuse(t, cs, "library_export", map[string]any{"path": path, "library": "Shows", "overwrite": true})
	path = filepath.Join(filepath.Dir(path), "narrowed.jsonl")
	narrowed := mustCall(t, cs, "library_export", map[string]any{"path": path, "library": "Shows", "fields": []any{"path"}})
	raw, _ = os.ReadFile(path) //nolint:gosec // a path this test chose
	if number(t, narrowed["rows"], "rows") != withFile || strings.Contains(string(raw), `"width"`) || !strings.Contains(string(raw), `"path"`) {
		t.Errorf("narrowed export = %v: %s", narrowed, raw)
	}
	if msg := mustRefuse(t, cs, "library_export", map[string]any{"library": "Shows"}); !strings.Contains(msg, "path") {
		t.Errorf("no path = %q", msg)
	}
}

// audit_quality lists the files it cannot trust: a file the server never
// probed answers every quality question with nothing, and on Emby a file
// written after the server first saw it may still carry the earlier file's
// facts.
func TestAuditQualityListsUnprobedAndReplaced(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Films","ItemId":"lib","CollectionType":"movies","Locations":["/m"]}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"TotalRecordCount": 4, "Items": []map[string]any{
			{
				"Id": "1", "Name": "Zzyzx Probed", "Type": "Movie", "Path": "/m/a.mkv", "LocationType": "FileSystem", "DateCreated": "2026-09-01T10:00:00Z", "DateModified": "2026-09-01T10:00:00Z",
				"MediaSources": []map[string]any{{"Size": 5, "MediaStreams": []map[string]any{{"Type": "Video", "Width": 1920, "Height": 1080}}}},
			},
			{
				"Id": "2", "Name": "Zzyzx Unprobed", "Type": "Movie", "Path": "/m/b.mkv", "LocationType": "FileSystem", "DateCreated": "2026-09-01T10:00:00Z", "DateModified": "2026-09-01T10:00:00Z",
				"MediaSources": []map[string]any{{"Size": 0}},
			},
			{
				"Id": "3", "Name": "Zzyzx Replaced", "Type": "Movie", "Path": "/m/c.mkv", "LocationType": "FileSystem", "DateCreated": "2026-09-01T10:00:00Z", "DateModified": "2026-09-15T10:00:00Z",
				"MediaSources": []map[string]any{{"Size": 7, "MediaStreams": []map[string]any{{"Type": "Video", "Width": 1280, "Height": 720}}}},
			},
			{"Id": "4", "Name": "Zzyzx Virtual", "Type": "Movie", "Path": "/m/d.mkv", "LocationType": "Virtual"},
		}})
	})
	out := mustCall(t, session(t, f, Options{}), "audit_quality", map[string]any{"library": "Films"})
	if number(t, out["items_scanned"], "items_scanned") != 4 {
		t.Errorf("scanned = %v, want every item", out["items_scanned"])
	}
	unprobed := objects(t, out["unprobed"], "unprobed")
	if len(unprobed) != 1 || text(unprobed[0]["name"]) != "Zzyzx Unprobed" || !strings.Contains(text(unprobed[0]["detail"]), "never probed") {
		t.Errorf("unprobed = %v", unprobed)
	}
	replaced := objects(t, out["replaced"], "replaced")
	if len(replaced) != 1 || text(replaced[0]["name"]) != "Zzyzx Replaced" || number(t, replaced[0]["size"], "size") != 7 || !strings.Contains(text(replaced[0]["detail"]), "2026-09-15") {
		t.Errorf("replaced = %v", replaced)
	}
	if number(t, out["total_unprobed"], "total_unprobed") != 1 || number(t, out["total_replaced"], "total_replaced") != 1 || out["note"] != nil {
		t.Errorf("totals = %v", out)
	}
}

// With a TMDB token, audit_file_path says where TMDB puts the file's
// title: another number is a file numbered in another provider's order,
// the same number a reworded title, none a title TMDB never heard of.
func TestAuditTitleMismatchDiagnosesByTMDB(t *testing.T) {
	t.Parallel()

	s := severance()
	s.ids = map[string]string{"Tmdb": guideTMDBID}
	s.episodes = []ep{
		{season: 1, number: 1, name: "Good News About Hell", path: "/s/Severance S01E01 - Good News About Hell.mkv"},
		{season: 1, number: 2, name: "Half Loop", path: "/s/Severance S01E02 - Good News About Hell.mkv"},    // numbered the other order
		{season: 1, number: 3, name: "In Perpetuity", path: "/s/Severance S01E03 - Nowhere To Be Found.mkv"}, // a title TMDB never heard of
	}
	run := map[int][]string{1: {"Good News About Hell", "Half Loop", "In Perpetuity"}}
	cs := session(t, tvServer(t, s), Options{TMDBKey: "k", ProviderTransport: guideServer(t, run, aired2022)})
	out := mustCall(t, cs, "audit_file_path", map[string]any{"library": "Shows"})
	rows := objects(t, out["findings"], "findings")
	if len(rows) != 2 {
		t.Fatalf("findings = %v", rows)
	}
	byEpisode := map[int]map[string]any{}
	for _, r := range rows {
		byEpisode[number(t, r["episode"], "episode")] = r
	}
	if r := byEpisode[2]; text(r["tmdb_episode"]) != "S01E01" || !strings.Contains(text(r["diagnosis"]), "another order") {
		t.Errorf("a file numbered the other way = %v", r)
	}
	if r := byEpisode[3]; r["tmdb_episode"] != nil || !strings.Contains(text(r["diagnosis"]), "no TMDB episode") {
		t.Errorf("a title TMDB never heard of = %v", r)
	}

	// without a token, nothing is said
	plain := mustCall(t, session(t, tvServer(t, s), Options{}), "audit_file_path", map[string]any{"library": "Shows"})
	if r := objects(t, plain["findings"], "findings")[0]; r["diagnosis"] != nil {
		t.Errorf("a diagnosis with no TMDB token: %v", r)
	}
}

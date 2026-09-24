package tools

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// With provider true, audit_missing_episodes reads each series' run from
// TMDB: what the run lists past the files is reported, a series no
// provider knows is listed as unknown with why, and the series are paged
// by lookup.
func TestAuditMissingEpisodesAsksTheProvider(t *testing.T) {
	t.Parallel()

	s := severance()
	s.ids = map[string]string{"Tmdb": guideTMDBID}
	s.episodes = []ep{
		{season: 1, number: 1, name: "one", path: "/m/s01e01.mkv"},
		{season: 1, number: 2, name: "two", path: "/m/s01e02.mkv"},
	}
	nobody := &fakeSeries{id: "s9", name: "Zzyzx Unidentified", episodes: []ep{
		{season: 1, number: 1, name: "pilot", path: "/m/z01.mkv"},
	}}
	run := map[int][]string{1: {"one", "two", "three", "four"}}
	cs := session(t, tvServer(t, s, nobody), Options{TMDBKey: "k", ProviderTransport: guideServer(t, run, aired2022)})

	// without the provider, neither series has a gap between its files
	plain := mustCall(t, cs, "audit_missing_episodes", map[string]any{"library": "Shows"})
	if number(t, plain["total_findings"], "total_findings") != 0 || boolean(t, plain["runs_known"], "runs_known") {
		t.Errorf("without the provider = %v", plain)
	}

	out := mustCall(t, cs, "audit_missing_episodes", map[string]any{"library": "Shows", "provider": true})
	if !boolean(t, out["runs_known"], "runs_known") || out["note"] != nil || out["next_offset"] != nil {
		t.Errorf("with the provider = %v", out)
	}
	rows := objects(t, out["findings"], "findings")
	if len(rows) != 1 || text(rows[0]["name"]) != s.name || !strings.Contains(text(rows[0]["detail"]), "listed by TMDB without a file: S01E03, S01E04") {
		t.Errorf("findings = %v", rows)
	}
	unknown := objects(t, out["unknown"], "unknown")
	if len(unknown) != 1 || text(unknown[0]["name"]) != "Zzyzx Unidentified" || !strings.Contains(text(unknown[0]["reason"]), "carries no tmdb, tvdb or imdb id") || number(t, out["total_unknown"], "total_unknown") != 1 {
		t.Errorf("unknown = %v", unknown)
	}

	// one series a call: Severance first by name, then the other
	first := mustCall(t, cs, "audit_missing_episodes", map[string]any{"library": "Shows", "provider": true, "max_lookups": 1})
	if number(t, first["next_offset"], "next_offset") != 1 || number(t, first["total_findings"], "total_findings") != 1 || first["total_unknown"] != nil {
		t.Errorf("first page = %v", first)
	}
	rest := mustCall(t, cs, "audit_missing_episodes", map[string]any{"library": "Shows", "provider": true, "offset": 1})
	if rest["next_offset"] != nil || number(t, rest["total_findings"], "total_findings") != 0 || number(t, rest["total_unknown"], "total_unknown") != 1 {
		t.Errorf("the rest = %v", rest)
	}

	// no token, no provider: said plainly
	if msg := mustRefuse(t, session(t, tvServer(t, s), Options{}), "audit_missing_episodes", map[string]any{"provider": true}); !strings.Contains(msg, "EMBYFIN_TMDB_TOKEN") {
		t.Errorf("without a token = %q", msg)
	}
}

// Two series of one name are asked about once each when paged: sorted by
// name alone, in the order a map gave them, they could swap across a
// next_offset boundary, so one was asked twice and the other never.
func TestAuditMissingEpisodesPagesSeriesOfOneNameOnce(t *testing.T) {
	t.Parallel()

	twins := []*fakeSeries{
		{id: "t1", name: "Zzyzx Twin", episodes: []ep{{season: 1, number: 1, name: "one", path: "/m/t1.mkv"}}},
		{id: "t2", name: "Zzyzx Twin", episodes: []ep{{season: 1, number: 1, name: "one", path: "/m/t2.mkv"}}},
	}
	cs := session(t, tvServer(t, twins...), Options{TMDBKey: "k", ProviderTransport: guideServer(t, map[int][]string{1: {"one"}}, aired2022)})

	asked := func(offset int) string {
		out := mustCall(t, cs, "audit_missing_episodes", map[string]any{"provider": true, "max_lookups": 1, "offset": offset})
		unknown := objects(t, out["unknown"], "unknown")
		if len(unknown) != 1 {
			t.Fatalf("offset %d: unknown = %v", offset, unknown)
		}

		return text(unknown[0]["id"])
	}
	for range 20 {
		if first, second := asked(0), asked(1); first != "t1" || second != "t2" {
			t.Fatalf("the two pages asked about %s and %s, want t1 then t2", first, second)
		}
	}
}

// recordsServer is a canned Emby holding one TV library whose episodes are
// the rows given, as the server lists them.
func recordsServer(t *testing.T, rows []map[string]any) *fakeServer {
	t.Helper()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []map[string]any{{"Name": "Shows", "CollectionType": "tvshows", "ItemId": "lib"}}, "TotalRecordCount": 1})
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": rows, "TotalRecordCount": len(rows)})
	})

	return f
}

// The server's records of episodes it has no file for are read by the rule
// the provider's run is: an episode that has not aired yet, or has no date
// at all, is announced rather than missing.
func TestAuditMissingEpisodesLeavesOutUnairedRecords(t *testing.T) {
	t.Parallel()

	future := time.Now().AddDate(0, 1, 0).UTC().Format("2006-01-02") + "T00:00:00.0000000Z"
	episode := func(id string, number int, premiere string, file bool) map[string]any {
		row := map[string]any{
			"Id": id, "Name": id, "Type": "Episode", "SeriesId": "z", "SeriesName": "Zzyzx Show",
			"ParentIndexNumber": 1, "IndexNumber": number, "LocationType": "Virtual", "PremiereDate": premiere,
		}
		if file {
			row["LocationType"], row["Path"] = "FileSystem", "/tv/z/"+id+".mkv"
		}

		return row
	}
	cs := session(t, recordsServer(t, []map[string]any{
		episode("e1", 1, "2020-01-01T00:00:00.0000000Z", true),
		episode("e2", 2, "2020-01-08T00:00:00.0000000Z", false),
		episode("e3", 3, future, false),
		episode("e4", 4, "", false),
	}), Options{})

	out := mustCall(t, cs, "audit_missing_episodes", map[string]any{})
	rows := objects(t, out["findings"], "findings")
	if len(rows) != 1 || !strings.HasSuffix(text(rows[0]["detail"]), "listed by the server's own records without a file: S01E02") {
		t.Errorf("findings = %v, want only the aired S01E02", rows)
	}
	if !boolean(t, out["runs_known"], "runs_known") {
		t.Errorf("runs_known = false for a server that keeps records: %v", out)
	}

	// a server whose only records are of episodes still to come keeps a run
	// all the same: nothing is missing, and that is known
	upcoming := mustCall(t, session(t, recordsServer(t, []map[string]any{
		episode("e1", 1, "2020-01-01T00:00:00.0000000Z", true),
		episode("e2", 2, future, false),
	}), Options{}), "audit_missing_episodes", map[string]any{})
	if number(t, upcoming["total_findings"], "total_findings") != 0 || !boolean(t, upcoming["runs_known"], "runs_known") || upcoming["note"] != nil {
		t.Errorf("only upcoming records = %v", upcoming)
	}
}

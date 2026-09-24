package tools

import (
	"strings"
	"testing"
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

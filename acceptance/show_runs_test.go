//go:build integration

package acceptance

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The 2025 series Asterix & Obelix: The Big Fight, its tvshow.nfo carrying
// the ids of the 1989 film Asterix and the Big Fight: TMDB 11625, which TMDB
// numbers as a film, and the film's IMDb id. Read as a series' number, 11625
// is Common Law, and its run came back as what the series was missing -
// two episodes of a 1996 sitcom, confidently. The IMDb id says what the ids
// are, and the run is not read.
func TestAShowHoldingAFilmsIDs(t *testing.T) {
	needsTMDBRecording(t, "GET api.themoviedb.org/3/find/tt0096842?external_source=imdb_id")
	asterix := findItem(t, "Messy Shows", "Series", "Asterix & Obelix: The Big Fight")

	out := call(t, "show_missing", map[string]any{"series_id": asterix})
	if out["supported"] != false || out["missing"] != nil || str(out["source"]) != "none" {
		t.Errorf("show_missing = supported %v, source %v, missing %v: want the run unknown", out["supported"], out["source"], out["missing"])
	}
	for _, want := range []string{"the series carries a film's ids", "its IMDb id tt0096842 is Asterix and the Big Fight (TMDB film 11625), not a series", "its TMDB id 11625 is that film's number", "item_identify"} {
		if !strings.Contains(str(out["reason"]), want) {
			t.Errorf("reason = %q, want it saying %q", out["reason"], want)
		}
	}
	// the files it holds are no gap
	if out["gaps_on_disk"] != nil || out["season_gaps_on_disk"] != nil {
		t.Errorf("gaps on disk = %v, %v", out["gaps_on_disk"], out["season_gaps_on_disk"])
	}

	// the sweep: unknown with the same reason, and no finding
	sweep := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "provider": true})
	for _, f := range rows(t, sweep["findings"], "findings") {
		if str(f["id"]) == asterix {
			t.Errorf("audit_missing_episodes finds %v", f)
		}
	}
	var reason string
	for _, u := range rows(t, sweep["unknown"], "unknown") {
		if str(u["id"]) == asterix {
			reason = str(u["reason"])
		}
	}
	if !strings.Contains(reason, "the series carries a film's ids") {
		t.Errorf("the series' unknown row = %q, want the film's ids named", reason)
	}

	// the other audits have nothing to say that is wrong: it is matched, its
	// path names it, and audit_provider reads films alone
	missing := call(t, "audit_missing_metadata_provider", map[string]any{"library": "Messy Shows", "missing": "tvdb"})
	var detail string
	for _, f := range rows(t, missing["findings"], "findings") {
		if str(f["id"]) == asterix {
			detail = str(f["detail"])
		}
	}
	if detail != "no tvdb id; has tmdb:11625 imdb:tt0096842" {
		t.Errorf("missing tvdb = %q, want the two ids it does hold", detail)
	}
	for _, f := range rows(t, call(t, "audit_file_path", map[string]any{"library": "Messy Shows"})["findings"], "findings") {
		if str(f["series"]) == "Asterix & Obelix: The Big Fight" || str(f["id"]) == asterix {
			t.Errorf("audit_file_path finds %v", f)
		}
	}
	if msg := callErr(t, "audit_provider", map[string]any{"library": "Messy Shows", "types": "Series"}); !strings.Contains(msg, "types must be Movie") {
		t.Errorf("audit_provider over series = %s", msg)
	}
	// and the film's TMDB number finds the series, the one thing holding it
	found := rows(t, call(t, "item_find_by_metadata_id", map[string]any{"metadata_provider": "tmdb", "id": "11625"})["items"], "items")
	if len(found) != 1 || str(found[0]["id"]) != asterix || str(found[0]["type"]) != "Series" {
		t.Errorf("tmdb 11625 = %v, want the series holding the film's number", found)
	}
}

// Red Dwarf, held from its third season on: its files are S03E01 to E03, and
// its tvshow.nfo carries its ids. Nothing on disk says seasons one and two
// are missing - there is no gap between files of one season - so without a
// provider the run is unknown and nothing is claimed; with one, the whole run
// before the third season is listed.
func TestAShowHeldFromALaterSeason(t *testing.T) {
	dwarf := findItem(t, "Messy Shows", "Series", "Red Dwarf")
	var seasons []int
	for _, s := range rows(t, call(t, "show_seasons", map[string]any{"series_id": dwarf})["seasons"], "seasons") {
		seasons = append(seasons, num(t, s["season"], "season"))
	}
	if !slices.Equal(seasons, []int{3}) {
		t.Errorf("seasons = %v, want the third alone", seasons)
	}

	plain := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows"})
	if plain["runs_known"] != false || slices.Contains(findings(t, plain), "Red Dwarf") {
		t.Errorf("from the files alone: runs_known %v, findings %v: want nothing said of Red Dwarf", plain["runs_known"], findings(t, plain))
	}

	needsTMDBRecording(t, "GET api.themoviedb.org/3/tv/326/season/12")
	out := call(t, "show_missing", map[string]any{"series_id": dwarf})
	if out["supported"] != true || str(out["source"]) != "tmdb" || out["gaps_on_disk"] != nil || out["season_gaps_on_disk"] != nil {
		t.Fatalf("show_missing = supported %v source %v, gaps %v %v", out["supported"], out["source"], out["gaps_on_disk"], out["season_gaps_on_disk"])
	}
	got := missingKeys(t, out)
	var first14 []string
	for season := 1; season <= 2; season++ {
		for e := 1; e <= 6; e++ {
			first14 = append(first14, fmt.Sprintf("S%02dE%02d", season, e))
		}
	}
	first14 = append(first14, "S03E04", "S03E05")
	if len(got) < len(first14) || !slices.Equal(got[:len(first14)], first14) || slices.Contains(got, "S03E01") {
		t.Errorf("missing = %v, want the first two seasons whole, then the third from E04", got)
	}
	if m := rows(t, out["missing"], "missing")[0]; str(m["name"]) != "The End" || str(m["air_date"]) != "1988-02-15" {
		t.Errorf("the first missing = %v, want TMDB's S01E01 The End, aired 1988-02-15", m)
	}

	sweep := call(t, "audit_missing_episodes", map[string]any{"library": "Messy Shows", "provider": true})
	var detail string
	for _, f := range rows(t, sweep["findings"], "findings") {
		if str(f["id"]) == dwarf {
			detail = str(f["detail"])
		}
	}
	if want := "listed by TMDB without a file: S01E01, S01E02, S01E03, S01E04, S01E05, S01E06, S02E01, S02E02, S02E03, S02E04, S02E05, S02E06 and "; !strings.HasPrefix(detail, want) || sweep["runs_known"] != true {
		t.Errorf("Red Dwarf = %q (runs known %v), want %q and a count of the rest", detail, sweep["runs_known"], want)
	}
	if n := len(got) - 12; !strings.HasSuffix(detail, fmt.Sprintf(" and %d more", n)) {
		t.Errorf("Red Dwarf = %q, want the rest counted: %d more", detail, n)
	}
}

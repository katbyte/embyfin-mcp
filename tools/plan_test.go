package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
)

// C18: the call to make before writing. A bulk import that does not read the
// destinations can overwrite one series' episodes with files from another of
// the same name, and nothing afterwards can show it: an overwritten path
// keeps the item's id and its date_created.
func TestPlanCheck(t *testing.T) {
	t.Parallel()

	old := &fakeSeries{
		id: "ex94", name: "Example High", year: 1994,
		path: "/media/shows/Example High (1994)",
		episodes: []ep{
			{season: 3, number: 8, name: "Episode 60", path: "/media/shows/Example High (1994)/Season 03/Example High - 03x08 - Episode 60.mkv", minutes: 45},
		},
	}
	other := &fakeSeries{
		id: "sev", name: "Severance", year: 2022, path: "/media/shows/Severance",
		episodes: []ep{{season: 1, number: 1, name: "One", path: "/media/shows/Severance/S01E01.mkv"}},
	}
	f := tvServer(t, old, other)
	(&fakeDisk{files: map[string]bool{old.episodes[0].path: true, other.episodes[0].path: true}}).serve(t, f)
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
		// straight onto an existing file, from a different show entirely
		{
			"path": "/media/shows/Example High (1994)/Season 03/Example High - 03x08 - Episode 60.mkv",
			"size": 7_000_000_000, "series": "Example High 2022", "season": 3, "episode": 8,
		},
		// a free path inside a series folder
		{"path": "/media/shows/Example High (1994)/Season 03/Example High - 03x09 - Episode 61.mkv", "series": "Example High"},
		// two entries that would land on one file
		{"path": "/media/shows/Severance/S01E07.mkv", "series": "Severance", "season": 1, "episode": 7},
		{"path": "/media/shows/Severance/S01E07.mkv", "series": "Severance", "season": 1, "episode": 7},
		// nowhere the server knows
		{"path": "/staging/incoming/Some Show S01E01.mkv"},
	}})

	rows := objects(t, out["entries"], "entries")
	if len(rows) != 5 {
		t.Fatalf("entries = %v", rows)
	}

	// the destination that already holds a file says so, with what is there
	overwrite := rows[0]
	if !boolean(t, overwrite["exists"], "exists") {
		t.Fatalf("an occupied destination read as free: %v", overwrite)
	}
	current := object(t, overwrite["current"], "current")
	if text(current["item_id"]) != "ex94-3-8" || number(t, current["runtime_s"], "runtime_s") != 45*60 {
		t.Errorf("current = %v", current)
	}
	// the incoming file is bigger, and the ratio says so rather than leaving
	// the caller to divide
	if ratio, ok := current["size_ratio"].(float64); !ok || ratio <= 1 {
		t.Errorf("size_ratio = %v", current["size_ratio"])
	}
	if number(t, out["existing"], "existing") != 1 {
		t.Errorf("existing = %v, want 1", out["existing"])
	}

	// and the folder it would join is the 1994 series, not the 2022 one the
	// caller named: that gap is the whole problem
	join := object(t, overwrite["would_join"], "would_join")
	if text(join["series_id"]) != "ex94" || number(t, join["series_year"], "series_year") != 1994 {
		t.Errorf("would_join = %v", join)
	}
	if score := decimal(t, join["claim_similarity"], "claim_similarity"); score >= seriesConfident {
		t.Errorf("claiming the 2022 series scored %v against the 1994 folder", score)
	}

	// a free path inside a known series: no file, and the series is named
	free := rows[1]
	if boolean(t, free["exists"], "exists") || free["current"] != nil {
		t.Errorf("a free path = %v", free)
	}
	if text(object(t, free["would_join"], "would_join")["series_id"]) != "ex94" {
		t.Errorf("a free path did not name its series: %v", free)
	}

	// two entries onto one path name each other
	for _, i := range []int{2, 3} {
		if dups := texts(rows[i]["duplicate_of"]); len(dups) != 1 || !strings.Contains(dups[0], "S01E07") {
			t.Errorf("entry %d does not name the entry it collides with: %v", i, rows[i])
		}
	}
	if number(t, out["duplicates"], "duplicates") != 2 {
		t.Errorf("duplicates = %v, want 2", out["duplicates"])
	}

	// a path under no series folder says so rather than reading as free
	outside := rows[4]
	if outside["would_join"] != nil || !strings.Contains(text(outside["note"]), "no series folder") {
		t.Errorf("a path outside the library = %v", outside)
	}
	if number(t, out["unplaced"], "unplaced") != 1 {
		t.Errorf("unplaced = %v, want 1", out["unplaced"])
	}

	// and the batch is bounded
	big := make([]map[string]any, 501)
	for i := range big {
		big[i] = map[string]any{"path": "/media/shows/Severance/x.mkv"}
	}
	if msg := mustRefuse(t, cs, "plan_check", map[string]any{"entries": big}); !strings.Contains(msg, "500") {
		t.Errorf("an oversized batch: %s", msg)
	}
	if msg := mustRefuse(t, cs, "plan_check", map[string]any{"entries": []map[string]any{}}); !strings.Contains(msg, "at least one") {
		t.Errorf("an empty batch: %s", msg)
	}
}

// A server that finds two files of one episode in a folder merges them into
// one item, and only the first is the item's own path. The second was read as
// free - a write there would have replaced it silently - and what was said to
// be at a path was the tallest version, not the file there.
func TestPlanCheckKnowsEveryVersionOfAnEpisode(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		s := severance()
		s.episodes[0].alt = "/media/shows/Severance/Season 01/S01E01 - 720p.mkv"
		cs := session(t, tvServerFor(t, jellyfin, s), Options{})

		out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
			{"path": "/media/shows/Severance/Season 01/S01E01 - 720p.mkv", "size": 700 << 20},
		}})
		row := objects(t, out["entries"], "entries")[0]
		if !boolean(t, row["checked"], "checked") || !boolean(t, row["exists"], "exists") {
			t.Fatalf("jellyfin %v: the second version's path read as free: %v", jellyfin, row)
		}
		// the 720p file at that path, not the 1080p one beside it
		current := object(t, row["current"], "current")
		if number(t, current["height"], "height") != 720 || number(t, current["size"], "size") != 350<<20 {
			t.Errorf("jellyfin %v: current describes another version: %v", jellyfin, current)
		}
		if ratio := decimal(t, current["size_ratio"], "size_ratio"); ratio != 2 {
			t.Errorf("jellyfin %v: size_ratio = %v, want 2 against the file at the path", jellyfin, ratio)
		}
	}
}

// Jellyfin cannot be asked what is at a path, so a path under no series folder
// - a film - read exists: false on every one, with nothing to say it had not
// been checked. It is asked by the title the path names instead, and where
// that cannot settle it the row says so rather than answering false.
func TestPlanCheckOnJellyfinSaysWhatItCouldNotCheck(t *testing.T) {
	t.Parallel()

	film := &fakeSeries{id: "alien", name: "Alien", year: 1979, film: true, path: "/media/films/Alien (1979)/Alien (1979).mkv"}
	for _, jellyfin := range []bool{false, true} {
		f := tvServerFor(t, jellyfin, severance(), film)
		(&fakeDisk{jellyfin: jellyfin, files: map[string]bool{film.path: true}}).serve(t, f)
		cs := session(t, f, Options{})

		out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
			{"path": "/media/films/Alien (1979)/Alien (1979).mkv"},
			{"path": "/media/films/Aliens (1986)/Aliens (1986).mkv"},
			{"path": "/staging/incoming/Some Film (2020).mkv"},
		}})
		rows := objects(t, out["entries"], "entries")

		// the film is found, on both servers, and it has no season
		held := rows[0]
		if !boolean(t, held["checked"], "checked") || !boolean(t, held["exists"], "exists") {
			t.Errorf("jellyfin %v: a film's own path = %v", jellyfin, held)
		} else if current := object(t, held["current"], "current"); text(current["item_id"]) != "alien" || current["season"] != nil {
			t.Errorf("jellyfin %v: current = %v", jellyfin, current)
		}

		// outside every library folder, nothing can be there on either
		outside := rows[2]
		if !boolean(t, outside["checked"], "checked") || boolean(t, outside["exists"], "exists") {
			t.Errorf("jellyfin %v: a path outside the library = %v", jellyfin, outside)
		}

		// inside the library, where only a path lookup could settle it
		free := rows[1]
		if !jellyfin {
			// Emby was asked by the path itself, and it is free
			if !boolean(t, free["checked"], "checked") || boolean(t, free["exists"], "exists") || number(t, out["unchecked"], "unchecked") != 0 {
				t.Errorf("Emby: a free path = %v, unchecked %v", free, out["unchecked"])
			}

			continue
		}
		// the library could not be asked, and says so; the disk has no file
		// here, but an item whose file is gone may still be at the path, so
		// whether writing here replaces anything is not known either
		if boolean(t, free["checked"], "checked") || free["in_library"] != nil || free["exists"] != nil || boolean(t, free["on_disk"], "on_disk") {
			t.Errorf("Jellyfin: a path it could not look up answered as though it had: %v", free)
		}
		if note := text(free["note"]); !strings.Contains(note, "not known") || !strings.Contains(note, "Jellyfin") || !strings.Contains(note, "exists is true only when the server's disk has a file here, and null otherwise") {
			t.Errorf("Jellyfin: the note does not say it could not tell: %q", note)
		}
		if number(t, out["unchecked"], "unchecked") != 1 {
			t.Errorf("Jellyfin: unchecked = %v, want 1", out["unchecked"])
		}
	}
}

// The claim is scored the way a name is resolved: its title and its year
// against the series' own. Scoring bare titles called "Severance (2022)" a
// different show from Severance under its own folder, and put a claim of
// plain "Doctor Who" under a series the library names "Doctor Who (1963)"
// no higher than a guess.
func TestPlanCheckScoresAClaimByTitleAndYear(t *testing.T) {
	t.Parallel()

	sev := &fakeSeries{id: "sev", name: "Severance", year: 2022, path: "/media/shows/Severance (2022)"}
	who := &fakeSeries{id: "who63", name: "Doctor Who (1963)", year: 1963, path: "/media/shows/Doctor Who (1963)"}
	cs := session(t, tvServer(t, sev, who), Options{})

	out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
		{"path": "/media/shows/Severance (2022)/Season 01/S01E01.mkv", "series": "Severance (2022)"},
		{"path": "/media/shows/Doctor Who (1963)/Season 01/S01E01.mkv", "series": "Doctor Who"},
		{"path": "/media/shows/Doctor Who (1963)/Season 01/S01E02.mkv", "series": "Doctor Who (2005)"},
	}})
	rows := objects(t, out["entries"], "entries")
	claim := func(i int) float64 {
		return decimal(t, object(t, rows[i]["would_join"], "would_join")["claim_similarity"], "claim_similarity")
	}
	if got := claim(0); got < 0.95 {
		t.Errorf("Severance (2022) under Severance's own folder scored %v", got)
	}
	if got := claim(1); got < 0.95 {
		t.Errorf("Doctor Who under Doctor Who (1963) scored %v", got)
	}
	if got := claim(2); got >= seriesConfident {
		t.Errorf("Doctor Who (2005) under the 1963 series scored %v: the year is what says it is the wrong show", got)
	}
}

// A special is season 0, and says so: an omitted 0 left the one season a
// caller most needs to tell apart with no number at all.
func TestPlanCheckSaysASpecialIsSeasonZero(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = append(s.episodes, ep{season: 0, number: 1, name: "Lumon Orientation", path: "/media/shows/Severance/Specials/S00E01.mkv"})
	cs := session(t, tvServer(t, s), Options{})

	out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{{"path": "/media/shows/Severance/Specials/S00E01.mkv"}}})
	current := object(t, objects(t, out["entries"], "entries")[0]["current"], "current")
	if season, ok := current["season"]; !ok || number(t, season, "season") != 0 {
		t.Errorf("a special's current = %v, want season 0", current)
	}
}

// A zero is an answer, and the most important one: an empty incoming file
// over a whole one is a size_ratio of 0, and a claim of another show
// altogether a claim_similarity of 0. Both were left out of the answer as if
// no size or series had been given.
func TestPlanCheckGivesAZero(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes[0].alt = "/media/shows/Severance/Season 01/S01E01 - 720p.mkv"
	cs := session(t, tvServer(t, s), Options{})

	out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
		{"path": "/media/shows/Severance/Season 01/S01E01 - 720p.mkv", "size": 0, "series": "Zzyzx Qwerty"},
		{"path": "/media/shows/Severance/Season 01/S01E01 - 720p.mkv"},
	}})
	rows := objects(t, out["entries"], "entries")
	current := object(t, rows[0]["current"], "current")
	if ratio, ok := current["size_ratio"]; !ok || decimal(t, ratio, "size_ratio") != 0 {
		t.Errorf("an empty file over a whole one: current = %v, want size_ratio 0", current)
	}
	join := object(t, rows[0]["would_join"], "would_join")
	if score, ok := join["claim_similarity"]; !ok || decimal(t, score, "claim_similarity") != 0 {
		t.Errorf("another show claimed: would_join = %v, want claim_similarity 0", join)
	}
	// and with no size or series given there is nothing to compare, and
	// nothing is said
	if _, ok := object(t, rows[1]["current"], "current")["size_ratio"]; ok {
		t.Errorf("no size given, yet size_ratio: %v", rows[1]["current"])
	}
	if _, ok := object(t, rows[1]["would_join"], "would_join")["claim_similarity"]; ok {
		t.Errorf("no series claimed, yet claim_similarity: %v", rows[1]["would_join"])
	}
}

// fakeDisk is the server's own disk as a canned server shows it: the folder
// listing and the path check, each server's way, over the files given. A
// disk that ignores case finds a path under any spelling. A hidden folder is
// one the server's process cannot read: its parent lists it, but listing it
// fails and the path check finds nothing in it, as .NET's Directory.Exists
// answers for a folder it may not enter. A blank folder is one listed as
// empty whatever it holds.
type fakeDisk struct {
	mu            sync.Mutex
	files         map[string]bool
	hidden, blank map[string]bool
	ignoreCase    bool
	jellyfin      bool
}

// unreadable says whether a path is a hidden folder or inside one, or inside
// a blank one: what the path check cannot find.
func (d *fakeDisk) unreadable(p string) bool {
	for dir := p; dir != ""; dir = mediapath.Dir(dir) {
		if d.hidden[dir] || dir != p && d.blank[dir] {
			return true
		}
	}

	return false
}

func (d *fakeDisk) serve(t *testing.T, f *fakeServer) {
	t.Helper()

	dirs := func() map[string]bool {
		out := map[string]bool{}
		for p := range d.files {
			for dir := mediapath.Dir(p); dir != ""; dir = mediapath.Dir(dir) {
				out[dir] = true
			}
		}

		return out
	}
	f.mux.HandleFunc("GET /Environment/DirectoryContents", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		folder := param(r.URL.Query(), "Path")
		if !dirs()[folder] || d.unreadable(folder) {
			http.Error(w, "not found", http.StatusNotFound)

			return
		}
		entries := []map[string]any{}
		if d.blank[folder] {
			writeJSON(t, w, entries)

			return
		}
		for p := range d.files {
			if mediapath.Dir(p) == folder {
				entries = append(entries, map[string]any{"Name": mediapath.Base(p), "Path": p, "Type": "File"})
			}
		}
		for p := range dirs() {
			if mediapath.Dir(p) == folder {
				entries = append(entries, map[string]any{"Name": mediapath.Base(p), "Path": p, "Type": "Directory"})
			}
		}
		writeJSON(t, w, entries)
	})
	f.mux.HandleFunc("POST /Environment/ValidatePath", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path   string
			IsFile bool
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("ValidatePath body: %v", err)
		}
		path := body.Path
		if !d.jellyfin {
			path = r.URL.Query().Get("Path")
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		held := d.dirsOrFiles(dirs(), body.IsFile)
		for p := range held {
			if (p == path || d.ignoreCase && strings.EqualFold(p, path)) && !d.unreadable(p) {
				w.WriteHeader(http.StatusNoContent)

				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
}

func (d *fakeDisk) dirsOrFiles(dirs map[string]bool, files bool) map[string]bool {
	if files {
		return d.files
	}

	return dirs
}

// plan_check read the library alone, so a file written by a previous batch
// and not yet scanned read as a free path, and the next write replaced it.
// The server's disk is read too, on both servers: a file no item holds is
// there all the same.
func TestPlanCheckReadsTheDiskAsWellAsTheLibrary(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		s := severance()
		film := &fakeSeries{id: "alien", name: "Alien", year: 1979, film: true, path: "/media/films/Alien (1979)/Alien (1979).mkv"}
		f := tvServerFor(t, jellyfin, s, film)
		disk := &fakeDisk{jellyfin: jellyfin, files: map[string]bool{
			"/media/shows/Severance/Season 01/S01E01.mkv": true,
			// written by the last batch, not scanned yet
			"/media/shows/Severance/Season 01/S01E03.mkv":    true,
			"/media/films/Aliens (1986)/Aliens (1986).mkv":   true,
			"/media/shows/Severance/Season 01/s01e04.mkv":    true,
			"/media/films/Alien (1979)/Alien (1979).mkv":     true,
			"/media/shows/Severance/Season 01/Other.nfo":     true,
			"/media/shows/Severance/Season 01/S01E09 folder": true,
		}}
		disk.serve(t, f)
		cs := session(t, f, Options{})

		out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
			{"path": "/media/shows/Severance/Season 01/S01E03.mkv"},
			{"path": "/media/films/Aliens (1986)/Aliens (1986).mkv"},
			{"path": "/media/shows/Severance/Season 01/S01E05.mkv"},
			{"path": "/media/shows/Severance/Season 01/S01E04.mkv"},
			{"path": "/media/shows/Severance/Season 01/S01E02.mkv"},
		}})
		rows := objects(t, out["entries"], "entries")

		// in a series folder: on the disk, in no item, and replaced all the same
		unscanned := rows[0]
		if !boolean(t, unscanned["exists"], "exists") || boolean(t, unscanned["in_library"], "in_library") || !boolean(t, unscanned["on_disk"], "on_disk") ||
			!strings.Contains(text(unscanned["note"]), "no item holds") || unscanned["current"] != nil {
			t.Errorf("jellyfin %v: an unscanned file = %v", jellyfin, unscanned)
		}
		// a film's folder: Emby asks the library by path and it holds nothing;
		// Jellyfin cannot ask, and the disk alone says a file is there
		loose := rows[1]
		if !boolean(t, loose["exists"], "exists") || !boolean(t, loose["on_disk"], "on_disk") {
			t.Errorf("jellyfin %v: an unscanned film = %v", jellyfin, loose)
		}
		if jellyfin && loose["in_library"] != nil || !jellyfin && boolean(t, loose["in_library"], "in_library") {
			t.Errorf("jellyfin %v: an unscanned film's in_library = %v", jellyfin, loose["in_library"])
		}
		// nothing anywhere is free
		if free := rows[2]; boolean(t, free["exists"], "exists") || boolean(t, free["on_disk"], "on_disk") || boolean(t, free["in_library"], "in_library") {
			t.Errorf("jellyfin %v: a free path = %v", jellyfin, free)
		}
		// a name differing only in case: a different file on this disk
		if cased := rows[3]; boolean(t, cased["exists"], "exists") || !strings.Contains(text(cased["note"]), `"s01e04.mkv" is beside this path`) {
			t.Errorf("jellyfin %v: a file differing in case on a disk that tells case apart = %v", jellyfin, cased)
		}
		// held by the library, its file gone from the disk: a file written
		// there becomes that item, which is what exists warns of
		if gone := rows[4]; !boolean(t, gone["exists"], "exists") || !boolean(t, gone["in_library"], "in_library") || boolean(t, gone["on_disk"], "on_disk") || !strings.Contains(text(gone["note"]), "the item's file is gone") {
			t.Errorf("jellyfin %v: an item whose file is gone = %v", jellyfin, gone)
		}
		// the film is in no item Emby could find by its path; Jellyfin could
		// not ask, so only the season's file is known to be unscanned there
		notScanned := 2
		if jellyfin {
			notScanned = 1
		}
		if number(t, out["existing"], "existing") != 3 || number(t, out["not_scanned"], "not_scanned") != notScanned {
			t.Errorf("jellyfin %v: existing %v, not_scanned %v", jellyfin, out["existing"], out["not_scanned"])
		}

		// on a disk that ignores case, the same name is the same file
		disk.mu.Lock()
		disk.ignoreCase = true
		disk.mu.Unlock()
		out = mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
			{"path": "/media/shows/Severance/Season 01/S01E04.mkv"},
			{"path": "/media/shows/Severance/Season 01/s01e01.mkv"},
		}})
		rows = objects(t, out["entries"], "entries")
		if cased := rows[0]; !boolean(t, cased["exists"], "exists") || !strings.Contains(text(cased["note"]), "ignores case") {
			t.Errorf("jellyfin %v: a file differing in case on a disk that ignores it = %v", jellyfin, cased)
		}
		// and a file the library holds, spelled otherwise, is that item's
		if cased := rows[1]; !boolean(t, cased["exists"], "exists") || !boolean(t, cased["in_library"], "in_library") || text(object(t, cased["current"], "current")["item_id"]) != "sev-1-1" {
			t.Errorf("jellyfin %v: the library's file spelled otherwise on a disk that ignores case = %v", jellyfin, cased)
		}
	}

	// a path that is not a full one is refused rather than answered free
	cs := session(t, tvServer(t, severance()), Options{})
	if msg := mustRefuse(t, cs, "plan_check", map[string]any{"entries": []map[string]any{{"path": "Severance/S01E01.mkv"}}}); !strings.Contains(msg, "not a full path") {
		t.Errorf("a relative path = %q", msg)
	}
}

// A folder the server could not list read as holding nothing, and so did a
// folder it listed as empty - which is how a folder its process cannot read
// can answer - so a path in either read free. The nearest folder above that
// the server can list settles a folder that is not there; one it lists but
// cannot read, or lists as empty, leaves the path not known. And a film the
// library holds under another spelling of the path, on a disk that ignores
// case, is that film, not a file no item holds.
func TestPlanCheckSaysWhatTheDiskCannotTell(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		film := &fakeSeries{id: "alien", name: "Alien", year: 1979, film: true, path: "/media/films/Alien (1979)/Alien (1979).mkv"}
		f := tvServerFor(t, jellyfin, severance(), film)
		disk := &fakeDisk{jellyfin: jellyfin, files: map[string]bool{
			"/media/shows/Severance/Season 01/S01E01.mkv": true,
			"/media/shows/Severance/Season 01/S01E02.mkv": true,
			film.path: true,
			"/media/shows/Severance/Season 03/S03E01.mkv": true,
			"/media/shows/Severance/Season 04/S04E01.mkv": true,
		}, hidden: map[string]bool{"/media/shows/Severance/Season 03": true}, blank: map[string]bool{"/media/shows/Severance/Season 04": true}}
		disk.serve(t, f)
		cs := session(t, f, Options{})

		out := mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{
			{"path": "/media/shows/Severance/Season 02/S02E01.mkv"},
			{"path": "/media/shows/Severance/Season 03/S03E01.mkv"},
			{"path": "/media/shows/Severance/Season 04/S04E01.mkv"},
			{"path": "/media/shows/Zzyzx Qwerty/Season 01/S01E01.mkv"},
			{"path": "/media/films/Aliens (1986)/Aliens (1986).mkv"},
		}})
		rows := objects(t, out["entries"], "entries")

		// a season folder that is not there: its show's folder says so
		if r := rows[0]; boolean(t, r["exists"], "exists") || boolean(t, r["on_disk"], "on_disk") ||
			!strings.Contains(text(r["note"]), "the folder /media/shows/Severance/Season 02 is not on the server's disk (the nearest folder there is /media/shows/Severance), so the disk has nothing at this path") {
			t.Errorf("jellyfin %v: a season folder not there = %v", jellyfin, r)
		}
		// a folder its show's folder lists, which the server cannot read
		if r := rows[1]; r["exists"] != nil || r["on_disk"] != nil ||
			!strings.Contains(text(r["note"]), "the server's disk lists the folder /media/shows/Severance/Season 03, but the server could not read it: whether a file is at this path is not known") {
			t.Errorf("jellyfin %v: a folder the server cannot read = %v", jellyfin, r)
		}
		// a folder it lists as empty
		if r := rows[2]; r["exists"] != nil || r["on_disk"] != nil ||
			!strings.Contains(text(r["note"]), "the server lists nothing in /media/shows/Severance/Season 04: an empty folder, or one its process cannot read, so whether a file is at this path is not known") {
			t.Errorf("jellyfin %v: a folder listed as empty = %v", jellyfin, r)
		}
		// two folders down that are not there: the nearest is the library's
		// shows folder; Jellyfin cannot ask the library about a path under
		// no series folder, so whether an item is still at it is not known
		r := rows[3]
		if boolean(t, r["on_disk"], "on_disk") || !strings.Contains(text(r["note"]), "the folder /media/shows/Zzyzx Qwerty is not on the server's disk (the nearest folder there is /media/shows), so the disk has nothing at this path") {
			t.Errorf("jellyfin %v: a show's folder not there = %v", jellyfin, r)
		}
		if jellyfin && r["exists"] != nil || !jellyfin && boolean(t, r["exists"], "exists") {
			t.Errorf("jellyfin %v: a show's folder not there, exists = %v", jellyfin, r["exists"])
		}
		// a film's folder not there: Emby asked the library by the path
		if r := rows[4]; boolean(t, r["on_disk"], "on_disk") || jellyfin && r["exists"] != nil || !jellyfin && boolean(t, r["exists"], "exists") {
			t.Errorf("jellyfin %v: a film's folder not there = %v", jellyfin, r)
		}
		if n := number(t, out["disk_unknown"], "disk_unknown"); n != 2 {
			t.Errorf("jellyfin %v: disk_unknown = %d, want the two folders it could not read", jellyfin, n)
		}

		// the film the library holds, asked after by another spelling on a
		// disk that ignores case
		disk.mu.Lock()
		disk.ignoreCase = true
		disk.mu.Unlock()
		out = mustCall(t, cs, "plan_check", map[string]any{"entries": []map[string]any{{"path": "/media/films/Alien (1979)/alien (1979).mkv"}}})
		cased := objects(t, out["entries"], "entries")[0]
		if !boolean(t, cased["checked"], "checked") || !boolean(t, cased["exists"], "exists") || !boolean(t, cased["in_library"], "in_library") ||
			text(object(t, cased["current"], "current")["item_id"]) != "alien" || strings.Contains(text(cased["note"]), "no item holds") || number(t, out["not_scanned"], "not_scanned") != 0 {
			t.Errorf("jellyfin %v: the library's film spelled otherwise on a disk that ignores case = %v, not_scanned %v", jellyfin, cased, out["not_scanned"])
		}
	}
}

// The walk up to a folder the server can list took an empty listing as
// proof, where an empty listing is how a share gone offline answers: an
// unscanned file there read free and a held one read as gone. And a folder
// the library holds items under, which the disk does not list, is the
// server not seeing its own library folder, not a folder that is not there.
func TestPlanCheckDoesNotTakeAnUnseenShareForAnEmptyOne(t *testing.T) {
	t.Parallel()

	for _, jellyfin := range []bool{false, true} {
		plan := func(disk *fakeDisk, paths ...string) (map[string]any, []map[string]any) {
			t.Helper()
			f := tvServerFor(t, jellyfin, severance())
			disk.jellyfin = jellyfin
			disk.serve(t, f)
			entries := make([]map[string]any, 0, len(paths))
			for _, p := range paths {
				entries = append(entries, map[string]any{"path": p})
			}
			out := mustCall(t, session(t, f, Options{}), "plan_check", map[string]any{"entries": entries})

			return out, objects(t, out["entries"], "entries")
		}
		const unscanned, held = "/media/shows/Severance/Season 01/S01E05.mkv", "/media/shows/Severance/Season 01/S01E01.mkv"

		// the shows share lists empty
		offline := &fakeDisk{files: map[string]bool{held: true, "/media/films/Zzyzx (2001)/Zzyzx (2001).mkv": true}, blank: map[string]bool{"/media/shows": true}}
		out, rows := plan(offline, unscanned, held)
		if r := rows[0]; r["exists"] != nil || r["on_disk"] != nil || !strings.Contains(text(r["note"]), "the server lists nothing in /media/shows") {
			t.Errorf("jellyfin %v: an unscanned file on a share that lists empty = %v", jellyfin, r)
		}
		if r := rows[1]; !boolean(t, r["exists"], "exists") || !boolean(t, r["in_library"], "in_library") || r["on_disk"] != nil || strings.Contains(text(r["note"]), "file is gone") {
			t.Errorf("jellyfin %v: a held file on a share that lists empty = %v", jellyfin, r)
		}
		if n := number(t, out["disk_unknown"], "disk_unknown"); n != 2 {
			t.Errorf("jellyfin %v: disk_unknown = %d, want both", jellyfin, n)
		}

		// the shows folder lists another show, and not the one the library
		// holds under it
		unseen := &fakeDisk{files: map[string]bool{"/media/shows/Zzyzx Other/Season 01/S01E01.mkv": true}}
		out, rows = plan(unseen, unscanned)
		if r := rows[0]; r["exists"] != nil || r["on_disk"] != nil ||
			!strings.Contains(text(r["note"]), "the server's disk does not list /media/shows/Severance, yet the library holds items there: the server cannot see its own library folder") {
			t.Errorf("jellyfin %v: a path under a library folder the disk does not list = %v", jellyfin, r)
		}
		if n := number(t, out["disk_unknown"], "disk_unknown"); n != 1 {
			t.Errorf("jellyfin %v: disk_unknown = %d, want 1", jellyfin, n)
		}
	}
}

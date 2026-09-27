package tools

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"testing"
)

// Which groups survive the limit is the same on every call: two groups of
// one series and one title in different seasons used to swap places with
// the order the sweep's map happened to give them, so a capped worklist
// named a different group each time it was asked.
func TestAuditDuplicateEpisodesKeepsItsOrderAcrossCalls(t *testing.T) {
	t.Parallel()

	s := &fakeSeries{id: "z", name: "Zzyzx Show", episodes: []ep{
		{season: 1, number: 1, name: "Pilot", path: "/m/s1e1.mkv", minutes: 45},
		{season: 1, number: 2, name: "Pilot", path: "/m/s1e2.mkv", minutes: 45},
		{season: 2, number: 1, name: "Pilot", path: "/m/s2e1.mkv", minutes: 45},
		{season: 2, number: 2, name: "Pilot", path: "/m/s2e2.mkv", minutes: 45},
	}}
	f := tvServer(t, s)
	adminView(t, f)
	cs := session(t, f, Options{})

	for range 20 {
		out := mustCall(t, cs, "audit_duplicate_episodes", map[string]any{"limit": 1})
		groups := objects(t, out["groups"], "groups")
		if len(groups) != 1 || number(t, out["total_findings"], "total_findings") != 2 {
			t.Fatalf("out = %v", out)
		}
		if season := number(t, groups[0]["season"], "season"); season != 1 {
			t.Fatalf("the group kept under the limit is season %d, want season 1 every time", season)
		}
	}
}

// dupEpisode is one episode as a canned Jellyfin lists it: its runtime in
// seconds, its file's size, and whether the server probed the file - an
// unprobed one carries the metadata's runtime and no streams.
type dupEpisode struct {
	season, number int
	title          string
	seconds        float64
	size           int64
	unprobed       bool
	ext            string // the file's extension, .mkv when not given
}

func dupEpisodesServer(t *testing.T, eps []dupEpisode) *fakeServer {
	t.Helper()

	rows := make([]map[string]any, 0, len(eps))
	for _, e := range eps {
		ticks := int64(e.seconds * ticksPerSecond)
		path := "/zz/Zzyzx Show/Season " + strconv.Itoa(e.season) + "/Zzyzx Show S" + strconv.Itoa(e.season) + "E" + strconv.Itoa(e.number) + cmp.Or(e.ext, ".mkv")
		source := map[string]any{
			"Path": path, "Container": "mkv", "Size": e.size, "RunTimeTicks": ticks,
			"MediaStreams": []map[string]any{{"Type": "Video", "Codec": "h264", "Width": 1920, "Height": 1080}},
		}
		if e.unprobed {
			source = map[string]any{"Path": path, "Size": e.size}
		}
		rows = append(rows, map[string]any{
			"Id": "e" + strconv.Itoa(e.season) + "-" + strconv.Itoa(e.number), "Type": "Episode", "Name": e.title, "Path": path,
			"SeriesName": "Zzyzx Show", "SeriesId": "z", "ParentIndexNumber": e.season, "IndexNumber": e.number,
			"LocationType": "FileSystem", "RunTimeTicks": ticks, "MediaSources": []map[string]any{source},
		})
	}
	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		start := startIndex(t, r.URL.Query())
		writeJSON(t, w, map[string]any{"Items": rows[min(start, len(rows)):], "TotalRecordCount": len(rows)})
	})

	return f
}

// Near certain only on what the files say. In a show whose episodes all run
// within a few percent of each other, a title a season carries twice on
// purpose ran as close as a copy and was called near certain: now two
// entries are near certain when they run the same to the second, when one
// runs the other's length at PAL speed, or when they are the same size to
// the byte - each shared by no episode of the season under another title -
// and a lead otherwise. A runtime the server did not read off the file is no
// sign, nor a size merely close, nor one every episode of a season of
// placeholders or links shares, nor a runtime in a season with nothing under
// another title to say the show is not cut to one length. A title whose
// entries run more than 15% apart is said as far_apart, every entry listed: a
// copy cut short, two episodes in one file, or two episodes that share a
// title.
//
// A length match counts only when the season shows its lengths vary: two or
// more episodes under other titles running lengths that differ. An airing
// anime cut to its slot runs every episode to the frame, and a double first
// episode beside three still titled TBA made the three near certain copies.
// Nor does it count for a placeholder title, which episodes not yet named
// share whatever they hold.
//
// A runtime another episode runs within a few seconds of is shared: a
// premiere of 2840 seconds, a two-parter "The Battle" at 1420.0 and 1420.3,
// and "Aftermath" at 1421.5 made the two-parter near certain copies - the
// lengths vary, and nothing ran the pair's to the second.
//
// Entries of the one title sharing a match are more copies: three copies of
// one file read as "the same runtime, which E07 runs too" against each pair,
// and a lead. And a season made from two sources - its first episodes from a
// film-rate disc, the rest from a PAL one, each cut to one length - holds a
// PAL-speed match between any two episodes either side of the join: two of
// them sharing a title were called the same content.
func TestAuditDuplicateEpisodesConfidence(t *testing.T) {
	t.Parallel()

	const half = 22 * 60 // a sitcom's episode
	eps := []dupEpisode{
		// a two-parter without its (1) and (2): a second and a half apart,
		// which a fixed-length show's episodes are, and different files
		{1, 1, "Two Parts", half, 350_000_000, false, ""},
		{1, 2, "Two Parts", half + 1.5, 351_000_000, false, ""},
		// the same encode twice under two numbers, and an episode of
		// another length
		{2, 1, "Held Twice", half, 350_000_000, false, ""},
		{2, 5, "Held Twice", half + 0.4, 360_000_000, false, ""},
		{2, 2, "Between", half + 30, 355_000_000, false, ""},
		{2, 3, "Between Again", half + 60, 356_000_000, false, ""},
		// the same content from a PAL source: 25 frames a second against
		// 23.976, so about 4% shorter
		{3, 1, "From PAL", half, 350_000_000, false, ""},
		{3, 2, "From PAL", half / palSpeedup, 300_000_000, false, ""},
		{3, 3, "Between", half + 30, 355_000_000, false, ""},
		{3, 4, "Between Again", half + 60, 356_000_000, false, ""},
		// never probed, the same size to the byte: the same file
		{4, 1, "Same Bytes", half, 183_500_000, true, ""},
		{4, 2, "Same Bytes", half, 183_500_000, true, ""},
		// never probed, the provider's runtime on both, and sizes a fixed
		// size encode leaves a few kilobytes apart: nothing says they are one
		{5, 1, "Told Alike", half, 183_500_000, true, ""},
		{5, 2, "Told Alike", half, 183_504_096, true, ""},
		// the same title at half the length: a copy cut short, or two
		// episodes - said, not dropped
		{6, 1, "Far Apart", half, 350_000_000, false, ""},
		{6, 2, "Far Apart", half / 2, 175_000_000, false, ""},
		// a placeholder title across a season, the first of them a special
		// length: every entry is tried against every one before it, so the
		// two alike still meet though the first meets neither
		{7, 1, "TBA", half / 2, 175_000_000, false, ""},
		{7, 2, "TBA", half, 350_000_000, false, ""},
		{7, 3, "TBA", half + 5, 352_000_000, false, ""},
		// three of one title, two the same file and a third that only runs
		// alike: a lead, with what ties the two
		{8, 1, "Three Alike", half, 350_000_000, false, ""},
		{8, 2, "Three Alike", half, 350_000_000, false, ""},
		{8, 3, "Three Alike", half + 20, 340_000_000, false, ""},
		// lengths each within 15% of the last but the first and the third
		// 22% apart: the 15% is held across a group, so no chain of them
		{9, 1, "Chained", 1000, 100_000_000, false, ""},
		{9, 2, "Chained", 1140, 114_000_000, false, ""},
		{9, 3, "Chained", 1280, 128_000_000, false, ""},
		// a show cut to a fixed length: the pair run the same to the second,
		// and so does another episode of the season
		{10, 1, "Fixed", half, 350_000_000, false, ""},
		{10, 2, "Fixed", half, 351_000_000, false, ""},
		{10, 3, "Another", half, 352_000_000, false, ""},
		// placeholders and links, every one the same small size
		{11, 1, "Placeholder", 0, 120, true, ""},
		{11, 2, "Placeholder", 0, 120, true, ""},
		{12, 1, "Linked", half, 120_000_000, true, ".strm"},
		{12, 2, "Linked", half, 120_000_000, true, ".strm"},
		// two entries alone in their season, one length to the second:
		// nothing says the show is not cut to it
		{13, 1, "Alone", half, 350_000_000, false, ""},
		{13, 2, "Alone", half, 351_000_000, false, ""},
		// three copies of one file, and an episode of another length
		{14, 1, "Thrice", half, 350_000_000, false, ""},
		{14, 2, "Between", half + 30, 355_000_000, false, ""},
		{14, 3, "Between Again", half + 60, 356_000_000, false, ""},
		{14, 4, "Thrice", half, 350_000_000, false, ""},
		{14, 7, "Thrice", half, 350_000_000, false, ""},
		// two sources, each cut to one length: E1 to E3 at film rate, E4
		// and E5 at PAL speed, and E3 and E4 two episodes sharing a title
		{15, 1, "Film Rate", half, 350_000_000, false, ""},
		{15, 2, "Film Rate Too", half, 351_000_000, false, ""},
		{15, 3, "Either Side", half, 352_000_000, false, ""},
		{15, 4, "Either Side", half / palSpeedup, 300_000_000, false, ""},
		{15, 5, "PAL Too", half / palSpeedup, 301_000_000, false, ""},
		// an airing anime cut to its slot: a double first episode, and three
		// still titled TBA, each the slot's length to the frame
		{16, 1, "Pilot", 2840, 700_000_000, false, ""},
		{16, 2, "TBA", 1420, 350_000_000, false, ""},
		{16, 3, "TBA", 1420, 351_000_000, false, ""},
		{16, 4, "TBA", 1420, 352_000_000, false, ""},
		// named alike, the same: one other length says nothing of the rest
		{17, 1, "Pilot", 2840, 700_000_000, false, ""},
		{17, 2, "Slot", 1420, 350_000_000, false, ""},
		{17, 3, "Slot", 1420, 351_000_000, false, ""},
		// a placeholder in a season whose lengths vary
		{18, 1, "Pilot", 2840, 700_000_000, false, ""},
		{18, 2, "Episode 2", 1420, 350_000_000, false, ""},
		{18, 3, "Episode 2", 1420, 351_000_000, false, ""},
		{18, 4, "Finale", 1500, 360_000_000, false, ""},
		// a two-parter beside an episode a second and a half longer
		{19, 1, "Premiere", 2840, 700_000_000, false, ""},
		{19, 2, "The Battle", 1420.0, 350_000_000, false, ""},
		{19, 3, "The Battle", 1420.3, 351_000_000, false, ""},
		{19, 4, "Aftermath", 1421.5, 352_000_000, false, ""},
		// a copy the server has listed but not yet read: no size to judge by
		{20, 1, "Unread", half, 350_000_000, false, ""},
		{20, 2, "Unread", half + 3, 0, false, ""},
	}
	out := mustCall(t, session(t, dupEpisodesServer(t, eps), Options{}), "audit_duplicate_episodes", map[string]any{})

	type want struct {
		title, confidence string
		episodes          []int
		evidence          []string
	}
	const unvaried = "but fewer than two episodes of the season under other titles run lengths of their own that differ by more than a few seconds, read off their files, so nothing says the show is not cut to one length, and no sign alone"
	placeholder := func(title string) string {
		return fmt.Sprintf("but %q is a placeholder title, which episodes not yet named share whatever they hold, and a show cut to one slot runs each to the frame, so no sign alone", title)
	}
	wants := []want{
		{"Held Twice", "near_certain", []int{1, 5}, []string{"E01 and E05: the same runtime to the second"}},
		{"Thrice", "near_certain", []int{1, 4, 7}, []string{"E01 and E04: the same runtime to the second", "E01 and E07: the same runtime to the second", "E04 and E07: the same runtime to the second"}},
		{"Alone", "lead", []int{1, 2}, []string{"E01 and E02: the same runtime to the second, " + unvaried}},
		{"TBA", "lead", []int{2, 3, 4}, []string{"E02 and E03: the same runtime to the second, " + placeholder("TBA"), "E02 and E04: the same runtime to the second, " + placeholder("TBA"), "E03 and E04: the same runtime to the second, " + placeholder("TBA")}},
		{"Slot", "lead", []int{2, 3}, []string{"E02 and E03: the same runtime to the second, " + unvaried}},
		{"Episode 2", "lead", []int{2, 3}, []string{"E02 and E03: the same runtime to the second, " + placeholder("Episode 2")}},
		{"The Battle", "lead", []int{2, 3}, []string{"E02 and E03: the same runtime to the second, but E04 of the season runs within a few seconds of it too, so no sign alone"}},
		{"Unread", "lead", []int{1, 2}, []string{"E01 and E02: E02's size is not known to the server, so the sizes say nothing"}},
		{"Either Side", "lead", []int{3, 4}, []string{"E03 and E04: one runs the other's length at PAL speed, to the second, but E01, E02, E05 of the season runs within a few seconds of it too, so no sign alone"}},
		{"From PAL", "near_certain", []int{1, 2}, []string{"E01 and E02: one runs the other's length at PAL speed, to the second: the same content from a PAL source"}},
		{"Same Bytes", "near_certain", []int{1, 2}, []string{"E01 and E02: the same size to the byte"}},
		{"Two Parts", "lead", []int{1, 2}, nil},
		{"Told Alike", "lead", []int{1, 2}, nil},
		{"TBA", "lead", []int{2, 3}, nil},
		// alone in their season, the two the same file are tied by their size
		{"Three Alike", "lead", []int{1, 2, 3}, []string{"E01 and E02: the same size to the byte"}},
		{"Chained", "lead", []int{1, 2}, nil},
		{"Fixed", "lead", []int{1, 2}, []string{"E01 and E02: the same runtime to the second, but E03 of the season runs within a few seconds of it too, so no sign alone"}},
		{"Placeholder", "lead", []int{1, 2}, []string{"E01 and E02: the same size to the byte, but files too small to be video (placeholders or links), so no sign"}},
		{"Linked", "lead", []int{1, 2}, []string{"E01 and E02: the same size to the byte, but files too small to be video (placeholders or links), so no sign"}},
		{"Far Apart", "far_apart", []int{1, 2}, nil},
		{"TBA", "far_apart", []int{1, 2, 3}, nil},
		{"Chained", "far_apart", []int{1, 2, 3}, nil},
	}
	groups := objects(t, out["groups"], "groups")
	if number(t, out["total_findings"], "total_findings") != len(wants) || len(groups) != len(wants) {
		t.Fatalf("%d groups, want %d: %v", len(groups), len(wants), groups)
	}
	rank := map[string]int{"near_certain": 0, "lead": 1, "far_apart": 2}
	for i, g := range groups {
		var numbers []int
		for _, e := range objects(t, g["episodes"], "episodes") {
			numbers = append(numbers, number(t, e["episode"], "episode"))
		}
		got := want{text(g["title"]), text(g["confidence"]), numbers, texts(g["evidence"])}
		if !slices.ContainsFunc(wants, func(w want) bool {
			return w.title == got.title && w.confidence == got.confidence && slices.Equal(w.episodes, got.episodes) && slices.Equal(w.evidence, got.evidence)
		}) {
			t.Errorf("group %+v is none wanted", got)
		}
		// near certain first, then leads, so a capped list starts with them
		if i > 0 && rank[text(g["confidence"])] < rank[text(groups[i-1]["confidence"])] {
			t.Errorf("groups out of order: %s after %s", g["confidence"], groups[i-1]["confidence"])
		}
	}
	// an entry the server never probed says so, its runtime being the
	// metadata's
	for _, g := range groups {
		title := text(g["title"])
		want := title == "Same Bytes" || title == "Told Alike" || title == "Placeholder" || title == "Linked"
		for _, e := range objects(t, g["episodes"], "episodes") {
			if unprobed := e["unprobed"] != nil && boolean(t, e["unprobed"], "unprobed"); unprobed != want {
				t.Errorf("%q E%v unprobed = %v, want %v", g["title"], e["episode"], e["unprobed"], want)
			}
		}
	}
}

// The titles a provider gives an episode it has no name for yet are
// placeholders - a number alone among them - and a real title with a number
// in it is not.
func TestPlaceholderTitles(t *testing.T) {
	t.Parallel()

	for title, want := range map[string]bool{
		"TBA": true, "tbd": true, "To Be Announced": true, "Untitled": true, "Untitled Episode": true, "Episode": true,
		"Episode 5": true, "Episode #1.5": true, "Ep. 12": true, "12": true, "#3": true, " Folge 7 ": true, "第5話": true,
		"Episode Five": true, "Episode Twenty-One": true, "episode twenty one": true, "Chapter Three": true, "Part Twelve": false,
		"Episode Five Hundred Days": false, "Five": false,
		"Pilot": false, "Part 2": false, "Episode IV": false, "Unknown Soldier": false, "The Episode 5 Story": false,
	} {
		if got := placeholderTitle(title); got != want {
			t.Errorf("placeholderTitle(%q) = %v, want %v", title, got, want)
		}
	}
}

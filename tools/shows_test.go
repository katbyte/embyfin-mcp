package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/tmdb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The show tools against a canned Emby: the server that started all of this,
// which since 4.10 answers every episode query with the files it holds and
// nothing about the run they come from. What is pinned here is what the tools
// make of that - who they ask next, and how they say they could not find out.

// ep is one episode as the fake server holds it. missing is the record a
// server that still imports the provider's run keeps for an episode the
// library lacks: neither server's item has an IsMissing field, they mark it
// LocationType Virtual, so that is what the canned one answers with.
type ep struct {
	season, number int
	number2        int // a file holding several episodes ends here (S01E01E02)
	name           string
	path           string
	missing        bool
}

// fakeSeries is one series the canned server holds.
// (a fakeServer knows which server it is pretending to be, for the session
// helper to build the matching client)

type fakeSeries struct {
	id, name string
	year     int
	ids      map[string]string
	episodes []ep
}

// wireItem is an item as the MediaBrowser API spells one.
type wireItem struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	Type              string            `json:"Type"`
	Path              string            `json:"Path,omitempty"`
	ProviderIDs       map[string]string `json:"ProviderIds,omitempty"`
	SeriesName        string            `json:"SeriesName,omitempty"`
	SeriesID          string            `json:"SeriesId,omitempty"`
	ParentIndexNumber int               `json:"ParentIndexNumber,omitempty"`
	IndexNumber       int               `json:"IndexNumber,omitempty"`
	IndexNumberEnd    int               `json:"IndexNumberEnd,omitempty"`
	LocationType      string            `json:"LocationType,omitempty"`
	PremiereDate      string            `json:"PremiereDate,omitempty"`
	ProductionYear    int               `json:"ProductionYear,omitempty"`
	RunTimeTicks      int64             `json:"RunTimeTicks,omitempty"`
	MediaSources      []wireSource      `json:"MediaSources,omitempty"`
}

type wireSource struct {
	Container    string       `json:"Container"`
	Size         int64        `json:"Size"`
	MediaStreams []wireStream `json:"MediaStreams"`
}

type wireStream struct {
	Type     string `json:"Type"`
	Codec    string `json:"Codec"`
	Width    int    `json:"Width,omitempty"`
	Height   int    `json:"Height,omitempty"`
	BitRate  int64  `json:"BitRate,omitempty"`
	Language string `json:"Language,omitempty"`
	// the servers spell the frame rate and the colour transfer on the stream,
	// and a canned one has to answer them the same way or the fields nothing
	// reads look like fields nothing sends
	Channels         int     `json:"Channels,omitempty"`
	AverageFrameRate float32 `json:"AverageFrameRate,omitempty"`
	ColorTransfer    string  `json:"ColorTransfer,omitempty"`
}

func (s *fakeSeries) item() wireItem {
	return wireItem{ID: s.id, Name: s.name, Type: "Series", Path: "/media/shows/" + s.name, ProviderIDs: s.ids, ProductionYear: s.year}
}

// items spells a series' episodes the way the server answers them, with the
// quality facts the scan probed for the ones that have a file.
func (s *fakeSeries) items() []wireItem {
	out := make([]wireItem, 0, len(s.episodes))
	for _, e := range s.episodes {
		it := wireItem{
			ID:                fmt.Sprintf("%s-%d-%d", s.id, e.season, e.number),
			Name:              e.name,
			Type:              "Episode",
			Path:              e.path,
			SeriesName:        s.name,
			SeriesID:          s.id,
			ParentIndexNumber: e.season,
			IndexNumber:       e.number,
			IndexNumberEnd:    e.number2,
			LocationType:      "FileSystem",
			RunTimeTicks:      30 * 600_000_000,
		}
		if e.missing {
			it.LocationType = "Virtual"
		}
		if e.path != "" && !e.missing {
			it.MediaSources = []wireSource{{
				Container: "mkv", Size: 700 << 20,
				MediaStreams: []wireStream{
					{Type: "Video", Codec: "h264", Width: 1920, Height: 1080, BitRate: 4_000_000, AverageFrameRate: 23.976, ColorTransfer: "bt709"},
					{Type: "Audio", Codec: "aac", Language: "eng", Channels: 6, BitRate: 448_000},
				},
			}}
		}
		out = append(out, it)
	}

	return out
}

// searchFold is how the servers' own search compares a term against a title:
// punctuation is not significant, so a search for "Americas" finds
// "AMERICA'S". An ampersand is NOT the word "and" though - which is why
// resolveSearchTerms falls back to shorter heads of a title, and why the fake
// has to keep the distinction rather than fold everything.
func searchFold(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("'", "", "\u2019", "", ":", "", ",", "", ".", "", "!", "", "?", "", "-", " ").Replace(s)

	return strings.Join(strings.Fields(s), " ")
}

// tvServer is a canned Emby holding one TV library and the series given. It
// answers the item query and the episode query, and nothing it is not asked.
func tvServer(t *testing.T, series ...*fakeSeries) *fakeServer {
	t.Helper()

	return tvServerFor(t, false, series...)
}

// tvServerJellyfin is the same library behind a Jellyfin: it honours the
// isMissing filter, which Emby has none of. Asked for the missing episodes it
// answers with those alone, and asked plainly it leaves them out - the shape
// that made show_missing report episodes it holds as missing, because nothing
// in the filtered answer had a file.
func tvServerJellyfin(t *testing.T, series ...*fakeSeries) *fakeServer {
	t.Helper()

	return tvServerFor(t, true, series...)
}

func tvServerFor(t *testing.T, jellyfin bool, series ...*fakeSeries) *fakeServer {
	t.Helper()

	f := newFakeServer(t)
	f.jellyfin = jellyfin
	byID := map[string]*fakeSeries{}
	for _, s := range series {
		byID[s.id] = s
	}

	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"Items": []map[string]any{{"Name": "Shows", "CollectionType": "tvshows", "ItemId": "lib"}}, "TotalRecordCount": 1})
	})

	f.mux.HandleFunc("GET /Shows/{id}/Episodes", func(w http.ResponseWriter, r *http.Request) {
		s := byID[r.PathValue("id")]
		if s == nil {
			http.NotFound(w, r)
			return
		}
		// Emby 4.10 takes a season number but no missing filter, and answers
		// with every episode it holds; Jellyfin splits the two apart
		rows := s.items()
		if season := r.URL.Query().Get("Season"); season != "" {
			n, _ := strconv.Atoi(season)
			rows = slices.DeleteFunc(rows, func(it wireItem) bool { return it.ParentIndexNumber != n })
		}
		if jellyfin {
			want := r.URL.Query().Get("isMissing") == "true"
			rows = slices.DeleteFunc(rows, func(it wireItem) bool { return (it.LocationType == "Virtual") != want })
		}
		writeJSON(t, w, map[string]any{"Items": rows, "TotalRecordCount": len(rows)})
	})

	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var rows []wireItem
		for _, s := range series {
			rows = append(rows, s.item())
			rows = append(rows, s.items()...)
		}
		if ids := q.Get("Ids"); ids != "" {
			want := strings.Split(ids, ",")
			rows = slices.DeleteFunc(rows, func(it wireItem) bool { return !slices.Contains(want, it.ID) })
		}
		if types := q.Get("IncludeItemTypes"); types != "" {
			want := strings.Split(types, ",")
			rows = slices.DeleteFunc(rows, func(it wireItem) bool { return !slices.Contains(want, it.Type) })
		}
		if term := q.Get("SearchTerm"); term != "" {
			rows = slices.DeleteFunc(rows, func(it wireItem) bool { return !strings.Contains(searchFold(it.Name), searchFold(term)) })
		}
		// a series as the parent is its own episodes; the library is all of them
		if parent := q.Get("ParentId"); parent != "" && parent != "lib" {
			rows = slices.DeleteFunc(rows, func(it wireItem) bool { return it.SeriesID != parent })
		}
		slices.SortStableFunc(rows, func(a, b wireItem) int {
			if c := strings.Compare(a.SeriesName, b.SeriesName); c != 0 {
				return c
			}
			if c := a.ParentIndexNumber - b.ParentIndexNumber; c != 0 {
				return c
			}
			return a.IndexNumber - b.IndexNumber
		})

		total := len(rows)
		start, _ := strconv.Atoi(q.Get("StartIndex"))
		if start > total {
			start = total
		}
		rows = rows[start:]
		if limit, _ := strconv.Atoi(q.Get("Limit")); limit > 0 && limit < len(rows) {
			rows = rows[:limit]
		}
		writeJSON(t, w, map[string]any{"Items": rows, "TotalRecordCount": total})
	})

	return f
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// mustCall calls a tool and fails the test when it refuses.
func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()

	out, refusal := callTool(t, cs, name, args)
	if refusal != "" {
		t.Fatalf("%s: %s", name, refusal)
	}

	return out
}

// mustRefuse calls a tool expecting a refusal, and returns what it said.
func mustRefuse(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()

	out, refusal := callTool(t, cs, name, args)
	if refusal == "" {
		t.Fatalf("%s answered where it should have refused: %v", name, out)
	}

	return refusal
}

// objects pulls a list of objects out of a decoded field.
func objects(t *testing.T, v any, field string) []map[string]any {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", field, v)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, row := range raw {
		m, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("a %s row is %T, want an object", field, row)
		}
		out = append(out, m)
	}

	return out
}

// boolean pulls a bool out of a decoded field, so a test reads "not
// supported" rather than comparing against a literal.
func boolean(t *testing.T, v any, field string) bool {
	t.Helper()

	b, ok := v.(bool)
	if !ok {
		t.Fatalf("%s is %T, want a bool", field, v)
	}

	return b
}

// text pulls a string out of a decoded field, "" when it was absent.
// decimal reads a fractional number out of an answer: a ratio or a margin,
// where rounding to an int would quietly pass a test that should fail.
func decimal(t *testing.T, v any, field string) float64 {
	t.Helper()

	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s is %T (%v), want a number", field, v, v)
	}

	return f
}

// object reads a nested object out of an answer.
func object(t *testing.T, v any, field string) map[string]any {
	t.Helper()

	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T (%v), want an object", field, v, v)
	}

	return m
}

// texts reads a list of strings out of an answer.
func texts(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, text(item))
	}

	return out
}

func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}

	return ""
}

// score is a candidate's score, which a ranking test reads.
func score(t *testing.T, row map[string]any) float64 {
	t.Helper()

	f, ok := row["score"].(float64)
	if !ok {
		t.Fatalf("score is %T, want a number: %v", row["score"], row)
	}

	return f
}

// codes spells a list of season/episode rows S01E02-style, so a test reads
// the way the answer does.
func codes(t *testing.T, v any, field string) []string {
	t.Helper()

	if v == nil {
		return nil
	}
	out := []string{}
	for _, m := range objects(t, v, field) {
		out = append(out, fmt.Sprintf("S%02dE%02d", number(t, m["season"], "season"), number(t, m["episode"], "episode")))
	}

	return out
}

// guideTMDBID is the series id the canned TMDB answers for, and the one the
// fixture series carries.
const guideTMDBID = "95396"

// guideServer is a canned TMDB behind the rewrite transport, holding one
// series' run.
func guideServer(t *testing.T, run map[int][]string, airDate func(season, episode int) string) http.RoundTripper {
	t.Helper()

	id := guideTMDBID

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/3/tv/"+id:
			seasons := make([]int, 0, len(run))
			for n := range run {
				seasons = append(seasons, n)
			}
			slices.Sort(seasons)
			rows := make([]string, 0, len(seasons))
			for _, n := range seasons {
				rows = append(rows, fmt.Sprintf(`{"season_number":%d,"episode_count":%d}`, n, len(run[n])))
			}
			_, _ = fmt.Fprintf(w, `{"id":%s,"seasons":[%s]}`, id, strings.Join(rows, ","))
		case strings.HasPrefix(r.URL.Path, "/3/tv/"+id+"/season/"):
			n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/3/tv/"+id+"/season/"))
			if err != nil || run[n] == nil {
				http.NotFound(w, r)
				return
			}
			eps := make([]string, 0, len(run[n]))
			for i, name := range run[n] {
				eps = append(eps, fmt.Sprintf(`{"season_number":%d,"episode_number":%d,"name":%q,"air_date":%q}`, n, i+1, name, airDate(n, i+1)))
			}
			_, _ = fmt.Fprintf(w, `{"season_number":%d,"episodes":[%s]}`, n, strings.Join(eps, ","))
		case strings.HasPrefix(r.URL.Path, "/3/find/"):
			if strings.TrimPrefix(r.URL.Path, "/3/find/") == "371980" && r.URL.Query().Get("external_source") == "tvdb_id" {
				_, _ = fmt.Fprintf(w, `{"tv_results":[{"id":%s}]}`, id)
				return
			}
			_, _ = w.Write([]byte(`{"tv_results":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return rewrite{target}
}

// aired2022 dates every episode in the past, so nothing is held back unaired.
func aired2022(season, episode int) string {
	return fmt.Sprintf("2022-%02d-%02d", season, episode)
}

// severance is the series the requirements were written against: two
// episodes of a nine-episode first season on disk, matched to TMDB, and an
// Emby that knows of nothing it has no file for.
func severance() *fakeSeries {
	return &fakeSeries{
		id: "sev", name: "Severance", year: 2022,
		ids: map[string]string{"Tmdb": "95396", "Imdb": "tt11280740"},
		episodes: []ep{
			{season: 1, number: 1, name: "Good News About Hell", path: "/media/shows/Severance/Season 01/S01E01.mkv"},
			{season: 1, number: 2, name: "Half Loop", path: "/media/shows/Severance/Season 01/S01E02.mkv"},
		},
	}
}

// C1, guarding A2: a question this server cannot answer must not come back
// as an empty list. With no provider to ask, missing is null, supported is
// false, and the reason says what would make it knowable - because a caller
// that read {"missing": []} as "nothing is missing" would delete files it
// should have kept.
func TestShowMissingUnanswerableIsNotEmpty(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, severance()), Options{})
	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})

	if boolean(t, out["supported"], "supported") {
		t.Error("supported is true with nothing to ask")
	}
	missing, present := out["missing"]
	if !present {
		t.Fatal("the answer has no missing field at all")
	}
	if missing != nil {
		t.Errorf("missing = %v, want null: an empty list reads as 'nothing is missing'", missing)
	}
	if out["source"] != sourceNone {
		t.Errorf("source = %v, want %s", out["source"], sourceNone)
	}
	reason := text(out["reason"])
	if !strings.Contains(reason, "EMBYFIN_TMDB_KEY") {
		t.Errorf("the reason does not say what would make the answer knowable: %q", reason)
	}
}

// C3, guarding A1: S01E01 and S01E03 on disk with E02 absent, and the answer
// names E02 with its season, episode and title - read from the provider,
// because Emby 4.10 keeps no record of it.
func TestShowMissingFixture(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = []ep{
		{season: 1, number: 1, name: "Good News About Hell", path: "/m/s01e01.mkv"},
		{season: 1, number: 3, name: "In Perpetuity", path: "/m/s01e03.mkv"},
	}
	run := map[int][]string{1: {"Good News About Hell", "Half Loop", "In Perpetuity"}}
	cs := session(t, tvServer(t, s), Options{TMDBKey: "k", ProviderTransport: guideServer(t, run, aired2022)})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if !boolean(t, out["supported"], "supported") || out["source"] != sourceTMDB {
		t.Fatalf("supported = %v, source = %v: %v", out["supported"], out["source"], out["reason"])
	}
	rows := objects(t, out["missing"], "missing")
	if len(rows) != 1 {
		t.Fatalf("missing = %v, want only S01E02", rows)
	}
	if number(t, rows[0]["season"], "season") != 1 || number(t, rows[0]["episode"], "episode") != 2 || rows[0]["name"] != "Half Loop" {
		t.Errorf("missing row = %v, want season 1 episode 2 Half Loop", rows[0])
	}
	// the gap between the files says the same thing from the weaker evidence
	if got := codes(t, out["gaps_on_disk"], "gaps_on_disk"); !slices.Equal(got, []string{"S01E02"}) {
		t.Errorf("gaps_on_disk = %v", got)
	}
}

// The provider answers the whole run, not just the interior gaps: what a
// library is missing is mostly the episodes after the last one it holds,
// which no reading of the files can see.
func TestShowMissingReadsTheWholeRun(t *testing.T) {
	t.Parallel()

	run := map[int][]string{1: {"Good News About Hell", "Half Loop", "In Perpetuity", "The You Are", "The Grim Barbarity of Optics and Design", "Hide and Seek", "Defiant Jazz", "What's for Dinner?", "The We Are"}}
	cs := session(t, tvServer(t, severance()), Options{TMDBKey: "k", ProviderTransport: guideServer(t, run, aired2022)})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	want := []string{"S01E03", "S01E04", "S01E05", "S01E06", "S01E07", "S01E08", "S01E09"}
	if got := codes(t, out["missing"], "missing"); !slices.Equal(got, want) {
		t.Errorf("missing = %v, want %v", got, want)
	}
	first := objects(t, out["missing"], "missing")[0]
	if first["name"] != "In Perpetuity" || first["air_date"] != "2022-01-03" {
		t.Errorf("first missing episode = %v", first)
	}
	// nothing is skipped between the two files, so the weaker fact is silent
	if got := codes(t, out["gaps_on_disk"], "gaps_on_disk"); len(got) != 0 {
		t.Errorf("gaps_on_disk = %v, want none", got)
	}
	if reason := text(out["reason"]); reason != "" {
		t.Errorf("an answered question carries a reason: %q", reason)
	}
}

// A complete series says so with supported and an empty list, which is a
// different answer from the unanswerable one above.
func TestShowMissingComplete(t *testing.T) {
	t.Parallel()

	run := map[int][]string{1: {"Good News About Hell", "Half Loop"}}
	cs := session(t, tvServer(t, severance()), Options{TMDBKey: "k", ProviderTransport: guideServer(t, run, aired2022)})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if !boolean(t, out["supported"], "supported") || out["source"] != sourceTMDB {
		t.Fatalf("supported = %v, source = %v", out["supported"], out["source"])
	}
	if out["missing"] == nil {
		t.Fatal("a complete series answered null, which means unknown")
	}
	if got := codes(t, out["missing"], "missing"); len(got) != 0 {
		t.Errorf("missing = %v, want none", got)
	}
}

// An episode TMDB has announced but not broadcast is not something the
// library is missing, unless it is asked for.
func TestShowMissingLeavesUnairedOut(t *testing.T) {
	t.Parallel()

	run := map[int][]string{1: {"Good News About Hell", "Half Loop", "In Perpetuity", "The You Are"}}
	dates := func(_, episode int) string {
		switch episode {
		case 3:
			return "2999-01-01" // announced, years away
		case 4:
			return "" // announced without a date at all
		}
		return "2022-01-01"
	}
	cs := session(t, tvServer(t, severance()), Options{TMDBKey: "k", ProviderTransport: guideServer(t, run, dates)})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if got := codes(t, out["missing"], "missing"); len(got) != 0 {
		t.Errorf("unaired episodes were reported missing: %v", got)
	}
	if !boolean(t, out["supported"], "supported") {
		t.Error("supported is false where TMDB answered")
	}

	out = mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev", "include_unaired": true})
	if got := codes(t, out["missing"], "missing"); !slices.Equal(got, []string{"S01E03", "S01E04"}) {
		t.Errorf("with include_unaired, missing = %v", got)
	}
}

// A series carrying no tmdb id is placed through the one it does carry.
func TestShowMissingPlacesASeriesByItsTVDBID(t *testing.T) {
	t.Parallel()

	s := severance()
	s.ids = map[string]string{"Tvdb": "371980"}
	run := map[int][]string{1: {"Good News About Hell", "Half Loop", "In Perpetuity"}}
	cs := session(t, tvServer(t, s), Options{TMDBKey: "k", ProviderTransport: guideServer(t, run, aired2022)})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if !boolean(t, out["supported"], "supported") || out["source"] != sourceTMDB {
		t.Fatalf("a tvdb-only series was not placed: %v", out)
	}
	if got := codes(t, out["missing"], "missing"); !slices.Equal(got, []string{"S01E03"}) {
		t.Errorf("missing = %v", got)
	}
}

// A series nobody has identified has no id to ask by, and says so rather
// than looking complete. The gaps between its files are still given, as the
// weaker fact they are.
func TestShowMissingUnidentifiedSeries(t *testing.T) {
	t.Parallel()

	s := severance()
	s.ids = nil
	s.episodes = []ep{
		{season: 1, number: 1, name: "one", path: "/m/s01e01.mkv"},
		{season: 1, number: 4, name: "four", path: "/m/s01e04.mkv"},
		{season: 3, number: 1, name: "one", path: "/m/s03e01.mkv"},
	}
	cs := session(t, tvServer(t, s), Options{TMDBKey: "k", ProviderTransport: guideServer(t, map[int][]string{1: {"a", "b", "c"}}, aired2022)})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if boolean(t, out["supported"], "supported") || out["missing"] != nil {
		t.Errorf("supported = %v, missing = %v", out["supported"], out["missing"])
	}
	if reason := text(out["reason"]); !strings.Contains(reason, "item_identify") {
		t.Errorf("the reason does not point at identification: %q", reason)
	}
	if got := codes(t, out["gaps_on_disk"], "gaps_on_disk"); !slices.Equal(got, []string{"S01E02", "S01E03"}) {
		t.Errorf("gaps_on_disk = %v", got)
	}
	seasons, ok := out["season_gaps_on_disk"].([]any)
	if !ok || len(seasons) != 1 || number(t, seasons[0], "season_gaps_on_disk[0]") != 2 {
		t.Errorf("season_gaps_on_disk = %v, want [2]", out["season_gaps_on_disk"])
	}
}

// A server that does keep the provider's run is believed, and no provider is
// asked at all.
func TestShowMissingPrefersTheServersOwnRecords(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = append(s.episodes, ep{season: 1, number: 3, name: "In Perpetuity", missing: true})
	cs := session(t, tvServer(t, s), Options{TMDBKey: "k", ProviderTransport: guideServer(t, map[int][]string{1: {"a", "b", "c", "d", "e"}}, aired2022)})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if !boolean(t, out["supported"], "supported") || out["source"] != sourceServer {
		t.Fatalf("the server's own records were not used: %v", out)
	}
	if got := codes(t, out["missing"], "missing"); !slices.Equal(got, []string{"S01E03"}) {
		t.Errorf("missing = %v, want only what the server listed", got)
	}
}

// A provider that cannot be reached leaves the run unknown rather than
// failing the call: the question is still answerable as "unknown, and here
// is why", which is the whole point of the supported flag.
func TestShowMissingSurvivesAProviderOutage(t *testing.T) {
	t.Parallel()

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(down.Close)
	target, err := url.Parse(down.URL)
	if err != nil {
		t.Fatal(err)
	}
	cs := session(t, tvServer(t, severance()), Options{TMDBKey: "k", ProviderTransport: rewrite{target}})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if boolean(t, out["supported"], "supported") || out["missing"] != nil {
		t.Errorf("a provider outage answered as though it knew: %v", out)
	}
	if reason := text(out["reason"]); !strings.Contains(reason, "could not be asked") || !strings.Contains(reason, "502") {
		t.Errorf("the reason does not name the outage: %q", reason)
	}
}

// What counts as aired, at the boundary: an episode broadcast today is one
// the library could have, and only a date still ahead holds it back. The
// dates come from TMDB as days, and the comparison is made against a clock,
// so the edges are worth pinning rather than inferring from a fixture.
func TestAired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		date string
		want bool
	}{
		{"2026-09-14", true},  // yesterday
		{"2026-09-15", true},  // today, whatever the hour
		{"2026-09-16", false}, // tomorrow
		{"2999-01-01", false}, // announced, years away
		{"", false},           // announced without a date at all
		{"soon", true},        // a date we cannot read is not evidence it is unaired
	} {
		if got := aired(tmdb.Episode{AirDate: tc.date}, now); got != tc.want {
			t.Errorf("aired(%q) = %v, want %v", tc.date, got, tc.want)
		}
	}
}

// The same question against a Jellyfin, which honours the missing filter
// where Emby has none. Asking it only for the missing episodes answers with
// no episode that has a file, and a run read from the provider is then
// entirely "missing" - including the two the library holds. Found live; this
// keeps it found.
func TestShowMissingOnJellyfin(t *testing.T) {
	t.Parallel()

	//nolint:dupword // "The You You Are" is the episode's real title
	run := map[int][]string{1: {"Good News About Hell", "Half Loop", "In Perpetuity", "The You You Are"}}
	cs := session(t, tvServerJellyfin(t, severance()), Options{TMDBKey: "k", ProviderTransport: guideServer(t, run, aired2022)})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if !boolean(t, out["supported"], "supported") || out["source"] != sourceTMDB {
		t.Fatalf("supported = %v, source = %v: %v", out["supported"], out["source"], out["reason"])
	}
	// the library holds S01E01 and S01E02; only the rest of the run is missing
	if got := codes(t, out["missing"], "missing"); !slices.Equal(got, []string{"S01E03", "S01E04"}) {
		t.Errorf("missing = %v, want S01E03 and S01E04: the episodes on disk were reported missing", got)
	}
}

// And a Jellyfin that does keep the run (the TheTVDB plugin) is believed,
// with the episodes it holds still counted as held.
func TestShowMissingOnJellyfinWithTheProviderRun(t *testing.T) {
	t.Parallel()

	s := severance()
	s.episodes = append(s.episodes, ep{season: 1, number: 3, name: "In Perpetuity", missing: true})
	cs := session(t, tvServerJellyfin(t, s), Options{})

	out := mustCall(t, cs, "show_missing", map[string]any{"series_id": "sev"})
	if !boolean(t, out["supported"], "supported") || out["source"] != sourceServer {
		t.Fatalf("supported = %v, source = %v", out["supported"], out["source"])
	}
	if got := codes(t, out["missing"], "missing"); !slices.Equal(got, []string{"S01E03"}) {
		t.Errorf("missing = %v, want only the record with no file", got)
	}
}

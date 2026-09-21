package tools

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/animelist"
)

// An invented list in Anime-Lists' shape: a series, the OVA and the film
// TVDB and TMDB fold into its specials, an OVA folded into a series the
// library does not hold, and a special a library matched as the whole show.
const animeListXML = `<anime-list>
  <anime anidbid="9101" tvdbid="71001" defaulttvdbseason="1" episodeoffset="" tmdbtv="81001" tmdbseason="1" tmdboffset="">
    <name>Zzyzx Senki</name>
    <mapping-list><mapping anidbseason="0" tvdbseason="0">;1-1;2-6;</mapping></mapping-list>
  </anime>
  <anime anidbid="9102" tvdbid="71001" defaulttvdbseason="0" episodeoffset="" tmdbtv="81001" tmdbseason="0" tmdboffset="2">
    <name>Zzyzx Senki OVA</name>
    <mapping-list><mapping anidbseason="1" tvdbseason="0">;1-3;2-4;</mapping></mapping-list>
  </anime>
  <anime anidbid="9103" tvdbid="71001" defaulttvdbseason="0" episodeoffset="4" tmdbtv="81001" tmdbseason="0" tmdboffset="">
    <name>Zzyzx Senki: The Movie</name>
  </anime>
  <anime anidbid="9104" tvdbid="72001" defaulttvdbseason="0" episodeoffset="" tmdbtv="82001" tmdbseason="0" tmdboffset="">
    <name>Zzyzx Gaiden</name>
  </anime>
  <anime anidbid="9105" tvdbid="73001" defaulttvdbseason="0" episodeoffset="" tmdbtv="83001" tmdbseason="0" tmdboffset="">
    <name>Zzyzx Tokubetsu-hen</name>
  </anime>
</anime-list>`

func animeListFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "anime-list.xml")
	if err := os.WriteFile(path, []byte(animeListXML), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestSpans(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   []int
		want string
	}{
		{[]int{3, 4}, "3–4"},
		{[]int{2, 5}, "2, 5"},
		{[]int{5}, "5"},
		// out of order and repeated, as a file holding two specials gives them
		{[]int{9, 1, 2, 3, 7, 10, 3}, "1–3, 7, 9–10"},
	} {
		if got := spans(tc.in); got != tc.want {
			t.Errorf("spans(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAuditAnimeIDs(t *testing.T) {
	t.Parallel()

	minutes := func(m int) int64 { return int64(m) * 60 * 10_000_000 }
	series := []map[string]any{
		{"Id": "a", "Name": "Zzyzx Senki", "Type": "Series", "ProviderIds": map[string]string{"AniDb": "9101", "Tvdb": "71001", "Tmdb": "81001"}},
		{"Id": "b", "Name": "Zzyzx Gaiden", "Type": "Series", "ProviderIds": map[string]string{"AniDb": "9104"}},
		{"Id": "c", "Name": "Zzyzx Senki: The Movie", "Type": "Series", "ProviderIds": map[string]string{"AniDb": "9103"}},
		{"Id": "d", "Name": "Zzyzx Tokubetsu-hen", "Type": "Series", "ProviderIds": map[string]string{"AniDb": "9105", "Tmdb": "83001"}},
		{"Id": "e", "Name": "Unrelated Show", "Type": "Series", "ProviderIds": map[string]string{"Tmdb": "99999"}},
	}
	specials := []map[string]any{
		{"Id": "sp1", "Name": "Recap", "IndexNumber": 1, "ParentIndexNumber": 0, "Path": "/m/a/S00E01.mkv", "RunTimeTicks": minutes(20)},
		// an opening at the OVA's number: a minute and a half is not the OVA
		{"Id": "op", "Name": "Opening", "IndexNumber": 3, "ParentIndexNumber": 0, "Path": "/m/a/S00E03 op.mkv", "RunTimeTicks": minutes(1) + minutes(1)/2},
		{"Id": "sp3", "Name": "OVA Part 1", "IndexNumber": 3, "ParentIndexNumber": 0, "Path": "/m/a/S00E03.mkv", "RunTimeTicks": minutes(30)},
		{"Id": "sp4", "Name": "OVA Part 2", "IndexNumber": 4, "ParentIndexNumber": 0, "Path": "/m/a/S00E04.mkv", "RunTimeTicks": minutes(30)},
		{"Id": "sp5", "Name": "The Movie", "IndexNumber": 5, "ParentIndexNumber": 0, "Path": "/m/a/S00E05.mkv", "RunTimeTicks": minutes(90)},
		// the show's own picture drama, right after the film: the list gives
		// it to the show, so the film does not run on into it
		{"Id": "sp6", "Name": "Picture Drama", "IndexNumber": 6, "ParentIndexNumber": 0, "Path": "/m/a/S00E06.mkv", "RunTimeTicks": minutes(7)},
		// a special the server lists but the library has no file for
		{"Id": "sp7", "Name": "Lost Special", "IndexNumber": 7, "ParentIndexNumber": 0, "LocationType": "Virtual"},
	}

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": series, "TotalRecordCount": len(series)})
	})
	f.mux.HandleFunc("GET /Shows/{id}/Seasons", func(w http.ResponseWriter, r *http.Request) {
		seasons := []map[string]any{}
		if r.PathValue("id") == "a" {
			seasons = []map[string]any{{"Id": "a-s0", "Name": "Specials", "Type": "Season", "IndexNumber": 0}, {"Id": "a-s1", "Name": "Season 1", "Type": "Season", "IndexNumber": 1}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": seasons, "TotalRecordCount": len(seasons)})
	})
	f.mux.HandleFunc("GET /Shows/{id}/Episodes", func(w http.ResponseWriter, r *http.Request) {
		rows := []map[string]any{}
		if r.PathValue("id") == "a" && r.URL.Query().Get("SeasonId") == "a-s0" {
			rows = specials
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": rows, "TotalRecordCount": len(rows)})
	})
	cs := session(t, f, Options{AnimeList: animeListFile(t)})

	out := mustCall(t, cs, "audit_anime_ids", map[string]any{})
	if number(t, out["list_entries"], "list_entries") != 5 || number(t, out["series_scanned"], "series_scanned") != len(series) {
		t.Fatalf("out = %v", out)
	}

	// a special matched as the whole show: its TMDB id and its AniDB id
	// cannot both be right
	disagree := objects(t, out["ids_disagree"], "ids_disagree")
	if len(disagree) != 1 || text(disagree[0]["id"]) != "d" || !strings.Contains(text(disagree[0]["detail"]), "tv 83001") ||
		text(disagree[0]["anidb"]) != "AniDB 9105 Zzyzx Tokubetsu-hen" {
		t.Errorf("ids_disagree = %v", disagree)
	}

	// held on their own, and the list says why: the justification is the entry
	separate := objects(t, out["kept_separate"], "kept_separate")
	if len(separate) != 2 || text(separate[0]["name"]) != "Zzyzx Gaiden" || text(separate[1]["id"]) != "c" ||
		text(separate[0]["detail"]) != "an AniDB entry of its own; TMDB folds it into the specials of tv 82001, and TVDB into those of series 72001" {
		t.Errorf("kept_separate = %v", separate)
	}

	// the OVA sits at specials 3 and 4, the film at 5; an opening at 3 and a
	// special with no file are not them
	split := objects(t, out["split_out"], "split_out")
	if len(split) != 2 || number(t, out["total_split_out"], "total_split_out") != 2 {
		t.Fatalf("split_out = %v", split)
	}
	ova, film := split[0], split[1]
	if text(ova["anidb"]) != "AniDB 9102 Zzyzx Senki OVA" || text(ova["where"]) != "TVDB specials 3–4" || text(ova["series_id"]) != "a" {
		t.Errorf("the OVA = %v", ova)
	}
	ids := make([]string, 0, 2)
	for _, s := range objects(t, ova["specials"], "specials") {
		ids = append(ids, text(s["id"]))
	}
	if strings.Join(ids, ",") != "sp3,sp4" {
		t.Errorf("the OVA is specials %v, want sp3 and sp4", ids)
	}
	// the film is also held as its own series: the library has it twice.
	// The list gives only where it starts, and the specials after it are the
	// show's own
	if text(film["where"]) != "TVDB specials from 5" || text(film["held_also"]) != "c" || len(objects(t, film["specials"], "specials")) != 1 {
		t.Errorf("the film = %v", film)
	}

	// only the series whose specials hold another entry are read
	for _, id := range []string{"b", "c", "d", "e"} {
		if n := len(f.requests("/Shows/" + id + "/Seasons")); n != 0 {
			t.Errorf("read the seasons of %s, whose specials hold no other entry", id)
		}
	}

	// a list that cannot be read is an error, not an empty report
	broken := session(t, f, Options{AnimeList: filepath.Join(t.TempDir(), "missing.xml")})
	if _, msg := callTool(t, broken, "audit_anime_ids", map[string]any{}); !strings.Contains(msg, "reading the anime list") {
		t.Errorf("a missing list = %q", msg)
	}
}

// Where the list says only where an entry starts, it has to be the only
// entry starting there, and not at a special the list gives to something
// else.
func TestPlaceIn(t *testing.T) {
	t.Parallel()

	list, err := animelist.Parse(strings.NewReader(`<anime-list>
  <anime anidbid="9201" tvdbid="74001" defaulttvdbseason="1"><name>Zzyzx Main</name>
    <mapping-list><mapping anidbseason="0" tvdbseason="0">;1-1;</mapping></mapping-list></anime>
  <anime anidbid="9202" tvdbid="74001" defaulttvdbseason="0" episodeoffset="1"><name>Zzyzx OVA One</name></anime>
  <anime anidbid="9203" tvdbid="74001" defaulttvdbseason="0" episodeoffset="1"><name>Zzyzx OVA Also Two</name></anime>
  <anime anidbid="9204" tvdbid="74001" defaulttvdbseason="0" episodeoffset=""><name>Zzyzx On The Show's Own</name></anime>
  <anime anidbid="9205" tvdbid="74001" defaulttvdbseason="0" episodeoffset="4"><name>Zzyzx Film</name></anime>
  <anime anidbid="9206" tvdbid="74001" defaulttvdbseason="0" episodeoffset="7"><name>Zzyzx Film Two</name></anime>
</anime-list>`))
	if err != nil {
		t.Fatal(err)
	}

	// both start at special 2: neither is placed
	for _, aid := range []string{"9202", "9203"} {
		if _, ok := placeIn(list, list.Entry(aid), "74001", ""); ok {
			t.Errorf("%s was placed where another entry starts too", aid)
		}
	}
	// special 1 is the show's own
	if _, ok := placeIn(list, list.Entry("9204"), "74001", ""); ok {
		t.Error("placed on a special the list gives to the show")
	}
	// the film runs from 5 over the specials held, up to where the next
	// entry starts
	place, ok := placeIn(list, list.Entry("9205"), "74001", "")
	if !ok || place.where != "TVDB specials from 5" {
		t.Fatalf("the film = %+v, %v", place, ok)
	}
	if got := place.in(map[int]bool{5: true, 6: true, 7: true, 8: true}); !slices.Equal(got, []int{5, 6, 7}) {
		t.Errorf("the film is specials %v, want 5-7: the next film starts at 8", got)
	}
	// and stops at a special the library does not hold
	if got := place.in(map[int]bool{5: true, 7: true}); !slices.Equal(got, []int{5}) {
		t.Errorf("across a gap: %v", got)
	}
}

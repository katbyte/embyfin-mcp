package tools

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
)

// previewFile builds a preview thumbnail file of frames thumbnails ten
// seconds apart: the header, the index with the entry that closes it, and a
// few bytes for each frame.
func previewFile(frames uint32) []byte {
	const each = 5
	b := make([]byte, 64, 64+8*(frames+1)+each*frames)
	copy(b, []byte{0x89, 'B', 'I', 'F', 0x0d, 0x0a, 0x1a, 0x0a})
	binary.LittleEndian.PutUint32(b[12:], frames)
	binary.LittleEndian.PutUint32(b[16:], 10000)
	at := 64 + 8*(frames+1)
	for i := range frames {
		b = binary.LittleEndian.AppendUint32(b, i)
		b = binary.LittleEndian.AppendUint32(b, at+each*i)
	}
	b = binary.LittleEndian.AppendUint32(b, 0xffffffff)
	b = binary.LittleEndian.AppendUint32(b, at+each*frames)

	return append(b, slices.Repeat([]byte{0xff}, int(each*frames))...)
}

// previewFake is an Emby holding three libraries - Films, which makes
// preview thumbnails, and Clips and Tapes, which each have one of the two
// options that takes off - and the films in them, with each film's preview
// file as its disk has it. A film with no file is answered the empty set.
type previewFake struct {
	*fakeServer

	mu    sync.Mutex
	films []map[string]any
	files map[string][]byte
	// overview is each film's overview, which a refresh may fill
	overview map[string]string
	// read is every film whose preview file was asked for
	read []string
}

func newPreviewFake(t *testing.T) *previewFake {
	t.Helper()

	film := func(id, folder, name string, seconds int, ids map[string]string) map[string]any {
		return map[string]any{
			"Id": id, "Name": name, "Type": "Movie", "ProviderIds": ids, "RunTimeTicks": int64(seconds) * 10_000_000,
			"Path": fmt.Sprintf("%s/%s/%s.mkv", folder, name, name),
		}
	}
	matched := map[string]string{"Tmdb": "770001"}
	sound := previewFile(703)
	p := &previewFake{
		fakeServer: newFakeServer(t),
		films: []map[string]any{
			// out of the order of their paths, which is the order answered in
			film("e", "/zz/films", "E Other (2005)", 9000, matched),
			film("a", "/zz/films", "A Sound (2001)", 7030, matched),
			film("b", "/zz/films", "B Missing (2002)", 6000, matched),
			film("c", "/zz/films", "C Garbage (2003)", 6000, matched),
			film("d", "/zz/films", "D Cut (2004)", 7030, matched),
			film("f", "/zz/films", "F Unprobed (2006)", 0, matched),
			film("j", "/zz/films", "J Unmatched (2010)", 6000, nil),
			film("g", "/zz/clips", "G Clip (2007)", 600, matched),
			film("h", "/zz/tapes", "H Tape (2008)", 600, matched),
			film("i", "/zz/loose", "I Loose (2009)", 600, matched),
		},
		files: map[string][]byte{
			"a": sound,
			"c": slices.Repeat([]byte{0x5a}, 5000),
			"d": sound[:1000],
			// made from a film two hours long, on one of two and a half
			"e": sound,
			"f": previewFile(10),
		},
		overview: map[string]string{},
	}
	library := func(name, id, folder string, extraction bool, interval int) map[string]any {
		return map[string]any{"Name": name, "CollectionType": "movies", "ItemId": id, "Locations": []string{folder}, "LibraryOptions": map[string]any{
			"EnableChapterImageExtraction": extraction, "ThumbnailImagesIntervalSeconds": interval, "MetadataSavers": []string{"Nfo"},
			"TypeOptions": []map[string]any{{"Type": "Movie", "MetadataFetchers": []string{"TheMovieDb"}}},
		}}
	}
	folders := map[string]string{"L1": "/zz/films", "L2": "/zz/clips", "L3": "/zz/tapes"}
	p.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(library("Films", "L1", "/zz/films", true, 10), library("Clips", "L2", "/zz/clips", true, -1), library("Tapes", "L3", "/zz/tapes", false, 10)))
	})
	p.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var rows []map[string]any
		for _, f := range p.films {
			id, path := text(f["Id"]), text(f["Path"])
			if ids := param(q, "Ids"); ids != "" && !slices.Contains(strings.Split(ids, ","), id) {
				continue
			}
			if parent := param(q, "ParentId"); parent != "" && !strings.HasPrefix(path, folders[parent]+"/") {
				continue
			}
			if types := param(q, "IncludeItemTypes"); types != "" && !slices.Contains(strings.Split(types, ","), "Movie") {
				continue
			}
			rows = append(rows, f)
		}
		start, _ := strconv.Atoi(param(q, "StartIndex"))
		writeJSON(t, w, map[string]any{"Items": rows[min(start, len(rows)):], "TotalRecordCount": len(rows)})
	})
	p.mux.HandleFunc("GET /Videos/{id}/index.bif", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		file, there := p.files[r.PathValue("id")]
		p.read = append(p.read, r.PathValue("id"))
		p.mu.Unlock()
		if !there {
			file = previewFile(0)
		}
		var first, last int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil || first >= len(file) {
			t.Errorf("the preview file of %s was asked for with Range %q", r.PathValue("id"), r.Header.Get("Range"))
			http.Error(w, "no range", http.StatusBadRequest)

			return
		}
		last = min(last, len(file)-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(file)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(file[first : last+1])
	})
	p.mux.HandleFunc("GET /Users/Query", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, page(map[string]any{"Id": "admin", "Name": "root", "Policy": map[string]any{"IsAdministrator": true, "EnableAllFolders": true}}))
	})
	p.mux.HandleFunc("GET /Users/admin/Items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p.mu.Lock()
		defer p.mu.Unlock()
		// a chapter shows a picture out of the thumbnails once there are any
		chapter := map[string]any{"Name": "Chapter 1", "StartPositionTicks": 0}
		if len(p.files[id]) > 5000 {
			chapter["ImageTag"] = "tag-of-the-thumbnails"
		}
		writeJSON(t, w, map[string]any{
			"Id": id, "Name": "Zzyzx " + id, "Type": "Movie", "Overview": p.overview[id], "Etag": "e" + strconv.Itoa(len(p.read)),
			"Chapters": []map[string]any{chapter}, "MediaSources": []map[string]any{{"Id": "ms", "Chapters": []map[string]any{chapter}}},
		})
	})

	return p
}

// readFor is how many times a film's preview file was asked for.
func (p *previewFake) readFor(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	n := 0
	for _, r := range p.read {
		if r == id {
			n++
		}
	}

	return n
}

// The audit asks about every film in a library that makes preview
// thumbnails, in the order of their paths, and names what is wrong with each:
// no file, a file a player cannot read, or one that does not fit the film.
// The films of a library with either option off are counted and not asked
// about, and so is one in no library's folders.
func TestAuditPreviewsNamesWhatIsWrong(t *testing.T) {
	t.Parallel()

	p := newPreviewFake(t)
	cs := session(t, p.fakeServer, Options{})

	out := mustCall(t, cs, "audit_previews", nil)
	if number(t, out["items_scanned"], "items_scanned") != 7 || number(t, out["total_findings"], "total_findings") != 5 || number(t, out["length_not_judged"], "length_not_judged") != 1 || out["next_offset"] != nil {
		t.Fatalf("out = %v", out)
	}
	by := object(t, out["by_problem"], "by_problem")
	if len(by) != 3 || number(t, by["missing"], "missing") != 2 || number(t, by["damaged"], "damaged") != 2 || number(t, by["length"], "length") != 1 {
		t.Errorf("by_problem = %v", by)
	}
	want := []struct{ id, problem, detail string }{
		{"b", "missing", "the server holds no preview thumbnails for it"},
		{"c", "damaged", "the file is not a set of thumbnails: it does not begin as one"},
		{"d", "damaged", "the file is cut off: its header counts 703 thumbnails, whose index alone ends at byte 5696, and the file is 1000 bytes"},
		{"e", "length", "703 thumbnails 10 s apart reach 1 h 57 min, and the server times the video at 2 h 30 min: they stop short of its end: cut short, or made from another file than the one there now"},
		{"j", "missing", "the server holds no preview thumbnails for it"},
	}
	found := objects(t, out["findings"], "findings")
	if len(found) != len(want) {
		t.Fatalf("findings = %v", found)
	}
	for i, w := range want {
		if f := found[i]; text(f["id"]) != w.id || text(f["problem"]) != w.problem || text(f["detail"]) != w.detail || text(f["library"]) != "Films" || !strings.HasPrefix(text(f["path"]), "/zz/films/") {
			t.Errorf("finding %d = %v, want %s %s: %s", i, f, w.id, w.problem, w.detail)
		}
	}
	// the libraries that make none, and the film in no library, by name
	off := objects(t, out["libraries_off"], "libraries_off")
	if len(off) != 3 || text(off[0]["library"]) != "" || text(off[1]["library"]) != "Clips" || text(off[2]["library"]) != "Tapes" {
		t.Fatalf("libraries_off = %v", off)
	}
	for i, why := range []string{"in no library's folders", "the library's options make no preview thumbnails", "the library's options make no preview thumbnails"} {
		if number(t, off[i]["videos"], "videos") != 1 || !strings.HasPrefix(text(off[i]["why"]), why) {
			t.Errorf("libraries_off[%d] = %v", i, off[i])
		}
	}
	// a film with no file is one request, one with a file two, a file cut
	// off in its index one - and a film whose library makes none, none
	for id, n := range map[string]int{"a": 2, "b": 1, "c": 1, "d": 1, "e": 2, "f": 2, "j": 1, "g": 0, "h": 0, "i": 0} {
		if got := p.readFor(id); got != n {
			t.Errorf("the preview file of %s was asked for %d times, want %d", id, got, n)
		}
	}

	// paged: two films a call, each call going on where the last stopped
	var paged []string
	for offset, calls := 0, 0; ; calls++ {
		page := mustCall(t, cs, "audit_previews", map[string]any{"max_checks": 2, "offset": offset})
		if n := number(t, page["items_scanned"], "items_scanned"); n > 2 {
			t.Fatalf("a page of two asked about %d", n)
		}
		for _, f := range objects(t, page["findings"], "findings") {
			paged = append(paged, text(f["id"]))
		}
		if page["next_offset"] == nil {
			if calls != 3 {
				t.Errorf("seven films two a call took %d calls, want 4", calls+1)
			}

			break
		}
		offset = number(t, page["next_offset"], "next_offset")
	}
	if !slices.Equal(paged, []string{"b", "c", "d", "e", "j"}) {
		t.Errorf("paged, the findings were %v", paged)
	}

	// limit caps the rows and not the count
	if capped := mustCall(t, cs, "audit_previews", map[string]any{"limit": 2}); len(objects(t, capped["findings"], "findings")) != 2 || number(t, capped["total_findings"], "total_findings") != 5 {
		t.Errorf("with a limit of 2: %v", capped)
	}

	// by id, a handful: the one in a library that makes none is said so
	ids := mustCall(t, cs, "audit_previews", map[string]any{"ids": []any{"b", "a", "g"}})
	if number(t, ids["items_scanned"], "items_scanned") != 2 || number(t, ids["total_findings"], "total_findings") != 1 || text(objects(t, ids["findings"], "findings")[0]["id"]) != "b" {
		t.Errorf("by id = %v", ids)
	}
	if off := objects(t, ids["libraries_off"], "libraries_off"); len(off) != 1 || text(off[0]["library"]) != "Clips" {
		t.Errorf("by id, libraries_off = %v", ids["libraries_off"])
	}

	// one library, and one that makes none
	if films := mustCall(t, cs, "audit_previews", map[string]any{"library": "Films"}); number(t, films["items_scanned"], "items_scanned") != 7 || films["libraries_off"] != nil {
		t.Errorf("the Films library = %v", films)
	}
	clips := mustCall(t, cs, "audit_previews", map[string]any{"library": "Clips"})
	if number(t, clips["items_scanned"], "items_scanned") != 0 || number(t, clips["total_findings"], "total_findings") != 0 || len(objects(t, clips["libraries_off"], "libraries_off")) != 1 {
		t.Errorf("a library that makes none = %v", clips)
	}

	for args, want := range map[*map[string]any]string{
		{"types": "Series"}:                             `types must be among Movie, Episode, Video, MusicVideo, not "Series"`,
		{"library": "Films", "ids": []any{"a"}}:         "give library or ids, not both",
		{"ids": []any{"nope"}}:                          "nope",
		{"library": "No Such Library"}:                  "No Such Library",
		{"ids": []any{"a"}, "types": "Episode"}:         "a",
		{"library": "Films", "types": "movie, Episode"}: "",
	} {
		_, msg := callTool(t, cs, "audit_previews", *args)
		if want == "" && msg != "" || want != "" && !strings.Contains(msg, want) {
			t.Errorf("audit_previews %v said %q, want %q", *args, msg, want)
		}
	}
}

// A file that fits its film to within a twentieth of its length, and two
// minutes, is sound: Emby's own count runs a frame or a few short of the
// runtime. Past that it is the length of another file.
func TestPreviewLengthAllowsForEmbysCount(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		thumbnails, seconds int
		problem             string
		judged              bool
	}{
		{703, 7030, "", true},
		{697, 7020, "", true},
		// two minutes on a short clip, where a twentieth is seconds
		{18, 300, "", true},
		{17, 300, previewLength, true},
		// a twentieth on a long film: six minutes of two hours
		{684, 7200, "", true},
		{683, 7200, previewLength, true},
		{757, 7200, previewLength, true},
		{10, 0, "", false},
	} {
		problem, _, judged := previewProblem(new(embyfin.Preview{Thumbnails: c.thumbnails, Interval: 10 * time.Second}), &embyfin.Item{RunTimeTicks: int64(c.seconds) * 10_000_000})
		if problem != c.problem || judged != c.judged {
			t.Errorf("%d thumbnails on %d s: %q judged %v, want %q judged %v", c.thumbnails, c.seconds, problem, judged, c.problem, c.judged)
		}
	}
	// which way it is off says what it may be: a runtime read wrong makes
	// sound thumbnails look too long
	if _, detail, _ := previewProblem(&embyfin.Preview{Thumbnails: 423, Interval: 10 * time.Second}, &embyfin.Item{RunTimeTicks: 8 * 10_000_000}); detail !=
		"423 thumbnails 10 s apart reach 1 h 10 min, and the server times the video at 8 s: they run past its end: made from another file than the one there now, or the runtime the server holds is wrong" {
		t.Errorf("thumbnails longer than the video: %s", detail)
	}
	if problem, detail, _ := previewProblem(&embyfin.Preview{Damage: "the file is empty"}, &embyfin.Item{}); problem != previewDamaged || detail != "the file is empty" {
		t.Errorf("a damaged file = %q: %s", problem, detail)
	}
	if problem, _, _ := previewProblem(&embyfin.Preview{}, &embyfin.Item{}); problem != previewMissing {
		t.Errorf("no file = %q", problem)
	}
}

// Jellyfin keeps trickplay tiles, which neither tool reads: both say so.
func TestPreviewToolsAreEmbysAlone(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.jellyfin = true
	cs := session(t, f, Options{})
	for tool, args := range map[string]map[string]any{
		"audit_previews":           nil,
		"item_previews_regenerate": {"ids": []any{"a"}},
	} {
		if msg := mustRefuse(t, cs, tool, args); !strings.Contains(msg, "read on Emby alone so far: Jellyfin keeps trickplay tiles") {
			t.Errorf("%s on Jellyfin said: %s", tool, msg)
		}
	}
	if msg := mustRefuse(t, cs, "item_previews_regenerate", nil); !strings.Contains(msg, "read on Emby alone so far") {
		t.Errorf("item_previews_regenerate on Jellyfin with nothing named said: %s", msg)
	}
	if n := len(f.seen); n != 0 {
		t.Errorf("Jellyfin was asked %d things", n)
	}
}

// regenerating runs a regeneration against the fake with a refresh that
// answers at once: remake says what each film's refresh leaves on disk, a
// film it does not name being left as it was, and the film's refresh is
// seen to run unless it is in queued.
func (p *previewFake) regenerating(t *testing.T, in regenerateIn, remake map[string][]byte, queued []string, clock func() time.Time) (out regenerateOut, asked []string) {
	t.Helper()

	client := p.client(t)
	out, err := regeneratePreviews(t.Context(), client, in, previewRun{
		make: func(ctx context.Context, id string) (embyfin.Preview, bool, error) {
			asked = append(asked, id)
			if id == "boom" || p.overview[id] == "boom" {
				return embyfin.Preview{}, false, errors.New("HTTP 502")
			}
			ran := !slices.Contains(queued, id)
			p.mu.Lock()
			if file, made := remake[id]; made && ran {
				p.files[id] = file
			}
			// a refresh fills what the item has empty
			if ran && id == "b" {
				p.overview[id] = "Filled from its nfo."
			}
			p.mu.Unlock()
			after, err := client.PreviewOf(ctx, id)

			return after, ran, err
		},
		now: clock,
	})
	if err != nil {
		t.Fatalf("item_previews_regenerate %+v: %v", in, err)
	}

	return out, asked
}

// Thumbnails are made again for the films that need them and no others,
// one row a film saying what happened: made; not made, where Emby ran the
// refresh and left what was there; skipped, for a sound file, a library that
// makes none, and a film with no id in a library that fetches metadata. What
// else the refresh changed is named, and the picture each chapter takes
// from the thumbnails is not counted as something else.
func TestRegeneratePreviewsMakesWhatIsMissingAndSaysWhatElseChanged(t *testing.T) {
	t.Parallel()

	p := newPreviewFake(t)
	remake := map[string][]byte{"b": previewFile(600), "j": previewFile(600), "d": previewFile(703), "e": previewFile(900)}
	out, asked := p.regenerating(t, regenerateIn{IDs: []string{"j", "g", "c", "a", "b"}}, remake, nil, time.Now)

	if !slices.Equal(asked, []string{"b", "c"}) {
		t.Fatalf("thumbnails were asked for on %v, want the missing b and the damaged c alone", asked)
	}
	if out.Made != 1 || out.NotMade != 1 || out.Skipped != 3 || out.Asked != 0 || out.Failed != 0 || out.Stopped != "" || out.NextOffset != 0 || len(out.NotReached) != 0 {
		t.Errorf("out = %+v", out)
	}
	rows := map[string]regenerateRow{}
	order := make([]string, 0, len(out.Videos))
	for _, r := range out.Videos {
		rows[r.ID] = r
		order = append(order, r.ID)
	}
	// the one not read first, then in the order of their paths
	if !slices.Equal(order, []string{"g", "a", "b", "c", "j"}) {
		t.Errorf("rows in the order %v", order)
	}
	if r := rows["b"]; r.Result != "made" || r.Thumbnails != 600 || r.Was != "missing: the server holds no preview thumbnails for it" || r.Library != "Films" ||
		!slices.Equal(r.AlsoChanged, []string{"Overview"}) {
		t.Errorf("the missing film = %+v, want it made, and the overview its refresh filled named and nothing else", r)
	}
	if r := rows["c"]; r.Result != "not_made" || !strings.HasPrefix(r.Was, "damaged: ") ||
		r.Detail != "Emby ran the refresh and the thumbnails are still damaged: the file is not a set of thumbnails: it does not begin as one" || len(r.AlsoChanged) != 0 {
		t.Errorf("the film Emby made none for = %+v", r)
	}
	if r := rows["a"]; r.Result != "skipped" || r.Was != "sound" || r.Detail != "its 703 thumbnails are sound, and are left alone" {
		t.Errorf("the sound film = %+v", r)
	}
	if r := rows["g"]; r.Result != "skipped" || r.Library != "Clips" || !strings.HasPrefix(r.Detail, "the library's options make no preview thumbnails") {
		t.Errorf("the film in a library that makes none = %+v", r)
	}
	if r := rows["j"]; r.Result != "skipped" || !strings.HasPrefix(r.Detail, "it holds no provider id and its library fetches metadata, so the refresh that makes thumbnails may match it to a title") {
		t.Errorf("the film with no id = %+v", r)
	}
	if !strings.Contains(out.Note, "Films saves nfo files: Emby wrote the nfo of each video it refreshed here again") {
		t.Errorf("note = %q, want it to say the nfo files were written again", out.Note)
	}

	// a film named is taken whatever is wrong with its thumbnails; made from
	// a file that still does not fit the runtime the server holds, it is made
	// all the same, and the row says so
	out, asked = p.regenerating(t, regenerateIn{IDs: []string{"e"}}, map[string][]byte{"e": previewFile(400)}, nil, time.Now)
	if r := out.Videos[0]; !slices.Equal(asked, []string{"e"}) || out.Made != 1 || r.Result != "made" || r.Thumbnails != 400 ||
		!strings.HasPrefix(r.Detail, "made from the file there now, and still not the length the server holds for the video: 400 thumbnails 10 s apart reach 1 h 6 min") {
		t.Errorf("a named film whose new thumbnails still do not fit: asked %v, %+v", asked, out)
	}

	// asked for, the film with no id is taken as it is
	out, asked = p.regenerating(t, regenerateIn{IDs: []string{"j"}, Unmatched: true}, remake, nil, time.Now)
	if !slices.Equal(asked, []string{"j"}) || out.Made != 1 || out.Videos[0].Result != "made" {
		t.Errorf("with unmatched: asked %v, %+v", asked, out)
	}
}

// A library's next films are taken in the order of their paths until the
// limit, the sound ones passed over without a row, and the answer says
// where to go on from; a film Emby makes none for is not met again there.
func TestRegeneratePreviewsWorksThroughALibrary(t *testing.T) {
	t.Parallel()

	p := newPreviewFake(t)
	remake := map[string][]byte{"b": previewFile(600), "d": previewFile(703), "e": previewFile(900), "j": previewFile(600)}

	// Films in the order of their paths: a sound, b missing, c damaged, d
	// cut off, e the wrong length, f sound, j with no id
	out, asked := p.regenerating(t, regenerateIn{Library: "Films", Limit: 2}, remake, nil, time.Now)
	if !slices.Equal(asked, []string{"b", "c"}) || out.Made != 1 || out.NotMade != 1 || out.NextOffset != 3 || out.Stopped != "the limit of 2 videos was reached" || len(out.Videos) != 2 || out.Checked != 3 {
		t.Fatalf("the first two: asked %v, %+v", asked, out)
	}
	// the film whose sound thumbnails are another film's length is passed
	// over, with a row saying how to take it
	out, asked = p.regenerating(t, regenerateIn{Library: "Films", Limit: 5, Offset: out.NextOffset}, remake, nil, time.Now)
	if !slices.Equal(asked, []string{"d"}) || out.Made != 1 || out.Skipped != 2 || out.NextOffset != 0 || out.Stopped != "" || out.Checked != 4 {
		t.Fatalf("the rest: asked %v, %+v", asked, out)
	}
	if e := out.Videos[1]; e.ID != "e" || e.Result != "skipped" || !strings.HasPrefix(e.Was, "length: ") || !strings.Contains(e.Detail, "pass length true to make them again from the file there now") {
		t.Errorf("the film with thumbnails of another length = %+v", e)
	}
	if last := out.Videos[len(out.Videos)-1]; last.ID != "j" || last.Result != "skipped" {
		t.Errorf("the film with no id = %+v", last)
	}
	// asked for, it is made again from the film there now
	out, asked = p.regenerating(t, regenerateIn{Library: "Films", Length: true, Offset: 4}, remake, nil, time.Now)
	if !slices.Equal(asked, []string{"e"}) || out.Made != 1 || out.Videos[0].Thumbnails != 900 || out.Videos[0].Detail != "" {
		t.Fatalf("with length: asked %v, %+v", asked, out)
	}
	// nothing is left but the film Emby could make none for and the one
	// passed over, and a sound film is not asked for twice
	if found := mustCall(t, session(t, p.fakeServer, Options{}), "audit_previews", map[string]any{"library": "Films"}); number(t, found["total_findings"], "total_findings") != 2 {
		t.Errorf("after, the audit finds %v", found["findings"])
	}
	if out, asked = p.regenerating(t, regenerateIn{Library: "Films", Limit: 1, Offset: 3}, remake, nil, time.Now); len(asked) != 0 || out.Skipped != 1 {
		t.Errorf("a second pass past the film Emby made none for: asked %v, %+v", asked, out)
	}

	// a library that makes none is said so, and nothing is asked for
	out, asked = p.regenerating(t, regenerateIn{Library: "Clips"}, remake, nil, time.Now)
	if len(asked) != 0 || out.Off == nil || out.Off.Library != "Clips" || out.Off.Videos != 1 || len(out.Videos) != 0 {
		t.Errorf("a library that makes none: asked %v, %+v", asked, out)
	}
}

// The call stops rather than queue behind what it cannot see the end of: a
// refresh that had not run when the wait for it ended, a request that
// failed, and its minutes running out. What it did not reach is said.
func TestRegeneratePreviewsStopsAndSaysWhere(t *testing.T) {
	t.Parallel()

	remake := map[string][]byte{"b": previewFile(600), "d": previewFile(703), "e": previewFile(900)}

	// b's refresh is still waiting its turn: nothing more is asked for
	p := newPreviewFake(t)
	out, asked := p.regenerating(t, regenerateIn{Library: "Films", Limit: 5}, remake, []string{"b"}, time.Now)
	if !slices.Equal(asked, []string{"b"}) || out.Asked != 1 || out.Made != 0 || out.NextOffset != 2 || out.Stopped != "a refresh had not run when the wait for it ended" {
		t.Fatalf("a refresh that had not run: asked %v, %+v", asked, out)
	}
	if r := out.Videos[0]; r.Result != "asked" || !strings.Contains(r.Detail, "It is made when its turn comes, so do not ask for it again") || len(r.AlsoChanged) != 0 {
		t.Errorf("the row = %+v", r)
	}
	out, asked = p.regenerating(t, regenerateIn{IDs: []string{"b", "d", "e"}}, remake, []string{"b"}, time.Now)
	if !slices.Equal(asked, []string{"b"}) || !slices.Equal(out.NotReached, []string{"d", "e"}) || out.NextOffset != 0 {
		t.Errorf("by id: asked %v, not reached %v, %+v", asked, out.NotReached, out)
	}

	// a request that fails is a row saying so, and the call stops there
	p = newPreviewFake(t)
	p.overview["b"] = "boom"
	out, asked = p.regenerating(t, regenerateIn{IDs: []string{"b", "d"}}, remake, nil, time.Now)
	if !slices.Equal(asked, []string{"b"}) || out.Failed != 1 || out.Videos[0].Result != "failed" || out.Videos[0].Detail != "HTTP 502" || !slices.Equal(out.NotReached, []string{"d"}) || out.Stopped != "a request failed" {
		t.Errorf("a request that failed: asked %v, %+v", asked, out)
	}

	// the minutes: a clock that is a minute on at every look, and one minute
	// to work in, lets the first film start and no other
	p = newPreviewFake(t)
	at := time.Unix(0, 0)
	clock := func() time.Time {
		at = at.Add(20 * time.Second)

		return at
	}
	out, asked = p.regenerating(t, regenerateIn{Library: "Films", Limit: 5, Minutes: 1}, remake, nil, clock)
	if !slices.Equal(asked, []string{"b"}) || out.Made != 1 || out.Stopped != "the minutes ran out" || out.NextOffset != 2 || out.Videos[0].TookS != 20 {
		t.Errorf("out of minutes: asked %v, %+v", asked, out)
	}

	// neither ids nor a library is refused before anything is read
	p = newPreviewFake(t)
	if _, err := regeneratePreviews(t.Context(), p.client(t), regenerateIn{}, previewRun{now: time.Now}); err == nil || !strings.Contains(err.Error(), "give ids, or a library to take the next videos of") {
		t.Errorf("with nothing named: %v", err)
	}
	if n := len(p.seen); n != 0 {
		t.Errorf("with nothing named the server was asked %d things", n)
	}
}

// What a refresh changed is every field that reads differently, by the
// server's names, less what moves on every save and the picture a chapter
// takes from the thumbnails themselves.
func TestAlsoChangedNamesWhatTheRefreshDidBesides(t *testing.T) {
	t.Parallel()

	chapters := func(tag bool, name string) []any {
		c := map[string]any{"Name": name, "StartPositionTicks": float64(0)}
		if tag {
			c["ImageTag"] = "t"
		}

		return []any{c}
	}
	before := map[string]any{
		"Name": "Zzyzx", "Etag": "e1", "DateModified": "2026-01-01", "UserData": map[string]any{"Played": false}, "Tagline": "gone after",
		"Chapters": chapters(false, "One"), "MediaSources": []any{map[string]any{"Id": "m", "Chapters": chapters(false, "One"), "ImageTag": "kept"}},
		"Genres": []any{"Drama"},
	}
	after := map[string]any{
		"Name": "Zzyzx", "Etag": "e2", "DateModified": "2026-01-02", "UserData": map[string]any{"Played": true}, "Overview": "new",
		"Chapters": chapters(true, "One"), "MediaSources": []any{map[string]any{"Id": "m", "Chapters": chapters(true, "One"), "ImageTag": "kept"}},
		"Genres": []any{"Drama", "Horror"},
	}
	if got := alsoChanged(before, after); !slices.Equal(got, []string{"Genres", "Overview", "Tagline"}) {
		t.Errorf("also changed = %v", got)
	}
	// a chapter renamed, and an image tag outside a chapter, are changes
	after["Chapters"] = chapters(true, "Renamed")
	after["MediaSources"] = []any{map[string]any{"Id": "m", "Chapters": chapters(true, "One"), "ImageTag": "moved"}}
	if got := alsoChanged(before, after); !slices.Equal(got, []string{"Chapters", "Genres", "MediaSources", "Overview", "Tagline"}) {
		t.Errorf("with a chapter renamed and a source's own tag moved, also changed = %v", got)
	}
	if got := alsoChanged(before, before); len(got) != 0 {
		t.Errorf("an item against itself: %v", got)
	}
}

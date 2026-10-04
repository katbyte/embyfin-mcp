package embyfin

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// A library by name or id: an exact name wins, then a name a letter's case
// apart when one library has it, and two libraries apart only in case are
// refused with both named. Nothing named means every library.
func TestFindLibraryByNameOrID(t *testing.T) {
	t.Parallel()

	c, _ := newFake(t, Jellyfin, map[string]route{
		"GET /Library/VirtualFolders": ok(`[{"Name":"Movies","ItemId":"m","Locations":["/data/films/"]},{"Name":"movies","ItemId":"m2","Locations":["/data/more"]},{"Name":"Shows","ItemId":"s","Locations":["/data/shows",""]},{"Name":"New","Locations":["/data/new"]}]`),
	})
	ctx := t.Context()
	for _, tc := range []struct{ ask, want string }{{"Movies", "m"}, {"movies", "m2"}, {"s", "s"}, {"shows", "s"}, {"SHOWS", "s"}} {
		f, err := c.FindLibrary(ctx, tc.ask)
		if err != nil || f == nil || f.ItemID != tc.want {
			t.Errorf("FindLibrary(%q) = %v, %v, want the library %s", tc.ask, f, err, tc.want)
		}
	}
	if f, err := c.FindLibrary(ctx, ""); f != nil || err != nil {
		t.Errorf("FindLibrary(\"\") = %v, %v, want nil for every library", f, err)
	}
	if _, err := c.FindLibrary(ctx, "MOVIES"); err == nil || !strings.Contains(err.Error(), `2 libraries are named "MOVIES" apart from case: "Movies" (id m), "movies" (id m2)`) {
		t.Errorf("two libraries a case apart = %v", err)
	}
	if _, err := c.FindLibrary(ctx, "Books"); err == nil || !strings.Contains(err.Error(), `no library named "Books" (have: Movies, movies, Shows, New)`) {
		t.Errorf("an unknown library = %v", err)
	}

	// a library the server lists without an id is found, and refused as
	// something to narrow to: an empty id narrows to nothing
	if f, err := c.FindLibrary(ctx, "New"); err != nil || f == nil || f.Name != "New" {
		t.Errorf("FindLibrary(New) = %v, %v", f, err)
	}
	if _, err := c.ResolveLibrary(ctx, "New"); err == nil || !strings.Contains(err.Error(), "lists the New library without an id") {
		t.Errorf("ResolveLibrary(New) = %v, want a refusal", err)
	}
	if f, err := c.ResolveLibrary(ctx, "Shows"); err != nil || f == nil || f.ItemID != "s" {
		t.Errorf("ResolveLibrary(Shows) = %v, %v", f, err)
	}

	// every library's folders, trailing separators off and empty ones left out
	paths, err := c.LibraryPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []LibraryPath{{"Movies", "/data/films"}, {"movies", "/data/more"}, {"Shows", "/data/shows"}, {"New", "/data/new"}}
	if !slices.Equal(paths, want) {
		t.Errorf("LibraryPaths = %v, want %v", paths, want)
	}
}

// A playlist or a collection by id or name: a name two share is refused
// with their ids, and a name none has says what there is.
func TestResolveByType(t *testing.T) {
	t.Parallel()

	c, f := newFake(t, Jellyfin, map[string]route{
		"GET /Items": ok(`{"Items":[{"Id":"p1","Name":"Mix"},{"Id":"p2","Name":"mix"},{"Id":"p3","Name":"Other"}],"TotalRecordCount":3}`),
	})
	ctx := t.Context()
	if it, err := c.ResolveByType(ctx, "Playlist", "p3"); err != nil || it.ID != "p3" {
		t.Errorf("by id = %v, %v", it, err)
	}
	if it, err := c.ResolveByType(ctx, "Playlist", "other"); err != nil || it.ID != "p3" {
		t.Errorf("by name, another case = %v, %v", it, err)
	}
	if _, err := c.ResolveByType(ctx, "Playlist", "MIX"); err == nil || !strings.Contains(err.Error(), `2 playlists are named "MIX" (ids p1, p2): pass an id`) {
		t.Errorf("two of a name = %v", err)
	}
	if _, err := c.ResolveByType(ctx, "BoxSet", "Nope"); err == nil || !strings.Contains(err.Error(), `no collection named "Nope" (have: Mix, mix, Other)`) {
		t.Errorf("none of a name = %v", err)
	}
	if r := f.all("GET /Items"); len(r) == 0 || r[0].query.Get("includeItemTypes") != "Playlist" {
		t.Errorf("the lookup asked for %v, want playlists", r)
	}
}

// The period an activity read covers is what the server keeps: Jellyfin's
// retention cuts a longer period short and says so, and asked for nothing
// it is the whole of what is kept; Emby keeps everything.
func TestActivityWindow(t *testing.T) {
	t.Parallel()

	jf, _ := newFake(t, Jellyfin, map[string]route{"GET /System/Configuration": ok(`{"ActivityLogRetentionDays":30}`)})
	ctx := t.Context()
	w, err := jf.ActivityWindow(ctx, 0)
	if err != nil || w.Days != 30 || w.Short || !strings.Contains(w.Note, "keeps 30 days of activity") {
		t.Errorf("nothing asked, 30 kept = %+v, %v", w, err)
	}
	w, err = jf.ActivityWindow(ctx, 60)
	if err != nil || w.Days != 60 || !w.Short || !strings.Contains(w.Note, "of the 60 days asked for only the last 30 can be read") {
		t.Errorf("60 asked, 30 kept = %+v, %v", w, err)
	}
	w, err = jf.ActivityWindow(ctx, 10)
	if err != nil || w.Days != 10 || w.Short || w.Note != "" || time.Until(w.Cutoff) > -9*24*time.Hour {
		t.Errorf("10 asked, 30 kept = %+v, %v", w, err)
	}

	emby, _ := newFake(t, Emby, map[string]route{})
	if w, err := emby.ActivityWindow(ctx, 0); err != nil || w.Days != DefaultActivityDays || w.Short || w.Note != "" {
		t.Errorf("Emby, nothing asked = %+v, %v", w, err)
	}
}

// An id no item query finds can still be a version's on Jellyfin, which the
// single read in an administrator's view answers; on Emby every version is
// an item of its own, and an id the query does not find is nothing.
func TestItemByIDOrVersion(t *testing.T) {
	t.Parallel()

	items := func(r *http.Request, _ string) (int, string) {
		if r.URL.Query().Get("ids") == "f1" {
			return http.StatusOK, `{"Items":[{"Id":"f1","Name":"Zzyzx","Path":"/m/Zzyzx.mkv"}],"TotalRecordCount":1}`
		}

		return http.StatusOK, `{"Items":[],"TotalRecordCount":0}`
	}
	c, _ := newFake(t, Jellyfin, map[string]route{
		"GET /Items":    items,
		"GET /Items/v2": ok(`{"Id":"v2","Name":"Zzyzx","MediaSources":[{"Id":"f1","Path":"/m/Zzyzx.mkv"},{"Id":"v2","Path":"/m/Zzyzx - 2160p.mkv"}]}`),
		"GET /Items/v9": answer(http.StatusNotFound, ""),
	})
	ctx := t.Context()
	if it, version, err := c.ItemByIDOrVersion(ctx, "f1"); err != nil || version || it.ID != "f1" {
		t.Errorf("the item itself = %v, %v, %v", it, version, err)
	}
	if it, version, err := c.ItemByIDOrVersion(ctx, "v2"); err != nil || !version || it.ID != "v2" || len(it.MediaSources) != 2 {
		t.Errorf("a folded version = %v, %v, %v", it, version, err)
	}
	if _, _, err := c.ItemByIDOrVersion(ctx, "v9"); err == nil || err.Error() != "no item with id v9" {
		t.Errorf("an id neither finds = %v", err)
	}

	emby, _ := newFake(t, Emby, map[string]route{"GET /Items": items})
	if _, _, err := emby.ItemByIDOrVersion(ctx, "v2"); err == nil || err.Error() != "no item with id v2" {
		t.Errorf("Emby holds no folded versions = %v", err)
	}
}

//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/jf"
	"github.com/katbyte/go-kt/pointer"
)

// TestJFItemsQuery drives /Items through every parameter the neutral layer
// uses, against the facts in the fixture table.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFItemsQuery(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	moviesID := jfLibrary(t, sdkMovies)
	jfLibrary(t, sdkShows) // the counts below span both
	fields := []jf.ItemFields{jf.ItemFieldsPath, jf.ItemFieldsProviderIds, jf.ItemFieldsOverview, jf.ItemFieldsGenres, jf.ItemFieldsTags, jf.ItemFieldsPeople, jf.ItemFieldsStudios, jf.ItemFieldsDateCreated}

	all := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{
		ParentId: moviesID, Recursive: new(true), IncludeItemTypes: []jf.BaseItemKind{jf.BaseItemKindMovie},
		Fields: fields, SortBy: []jf.ItemSortBy{jf.ItemSortByName}, SortOrder: []jf.SortOrder{jf.SortOrderAscending},
	})).Model
	if all.TotalRecordCount != len(movies) || len(all.Items) != len(movies) {
		t.Fatalf("movies = %d/%d, want %d", len(all.Items), all.TotalRecordCount, len(movies))
	}
	byTitle := map[string]jf.BaseItemDto{}
	for _, it := range all.Items {
		byTitle[it.Name] = it
		if it.Id == "" || it.Type != jf.BaseItemKindMovie || it.Path == "" || it.Overview == "" || it.DateCreated == "" || it.ServerId == "" {
			t.Errorf("%s did not decode: %+v", it.Name, it)
		}
		if it.MediaType != jf.MediaTypeVideo || it.LocationType != jf.LocationTypeFileSystem {
			t.Errorf("%s: MediaType %q LocationType %q", it.Name, it.MediaType, it.LocationType)
		}
	}
	for _, m := range movies {
		it, ok := byTitle[m.Title]
		if !ok {
			t.Errorf("%s is missing from the scan", m.Title)
			continue
		}
		if it.ProductionYear != m.Year || it.ProviderIds["Tmdb"] != m.TMDB || it.ProviderIds["Imdb"] != m.IMDB {
			t.Errorf("%s: year %d, provider ids %v; the nfo says %d/%s/%s", m.Title, it.ProductionYear, it.ProviderIds, m.Year, m.TMDB, m.IMDB)
		}
		if !slices.Contains(it.Genres, m.Genre) {
			t.Errorf("%s: genres %v lack %q", m.Title, it.Genres, m.Genre)
		}
		if !slices.ContainsFunc(it.People, func(p jf.BaseItemPerson) bool { return p.Name == m.Director && p.Type == jf.PersonKindDirector }) {
			t.Errorf("%s: people %+v lack director %s", m.Title, it.People, m.Director)
		}
	}
	// the sort order held
	if all.Items[0].Name != alien || all.Items[len(all.Items)-1].Name != "The Thirteenth Floor" {
		t.Errorf("sorted by name: first %q, last %q", all.Items[0].Name, all.Items[len(all.Items)-1].Name)
	}

	// the providers filled what the nfo does not carry: a rating and the
	// studios (the cast is not merged, since the nfo already named a person)
	if a := byTitle[alien]; a.CommunityRating == 0 || len(a.Studios) == 0 || a.Studios[0].Name == "" {
		t.Errorf("%s has no rating or studios after a scan with providers on: rating %v, studios %+v", alien, a.CommunityRating, a.Studios)
	}

	t.Run("SearchTerm", func(t *testing.T) {
		res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{ParentId: moviesID, Recursive: new(true), SearchTerm: "Dune"})).Model
		if res.TotalRecordCount != 2 {
			t.Errorf("SearchTerm=Dune found %d, want Dune and Dune: Part Two", res.TotalRecordCount)
		}
	})
	t.Run("Ids", func(t *testing.T) {
		res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{Ids: []string{byTitle[alien].Id, byTitle[aliens].Id}})).Model
		if len(res.Items) != 2 {
			t.Errorf("Ids found %d, want 2", len(res.Items))
		}
	})
	t.Run("Paging", func(t *testing.T) {
		res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{
			ParentId: moviesID, Recursive: new(true), IncludeItemTypes: []jf.BaseItemKind{jf.BaseItemKindMovie},
			SortBy: []jf.ItemSortBy{jf.ItemSortByProductionYear}, SortOrder: []jf.SortOrder{jf.SortOrderDescending}, StartIndex: new(2), Limit: new(3),
		})).Model
		if len(res.Items) != 3 || res.TotalRecordCount != len(movies) || res.StartIndex != 2 {
			t.Errorf("StartIndex=2 Limit=3: %d items of %d from %d", len(res.Items), res.TotalRecordCount, res.StartIndex)
		}
		// newest first: 2024, 2021, then the page starts at 2016
		if res.Items[0].ProductionYear != 2016 {
			t.Errorf("third newest is %s (%d), want Arrival (2016)", res.Items[0].Name, res.Items[0].ProductionYear)
		}
	})
	t.Run("Genres", func(t *testing.T) {
		res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{ParentId: moviesID, Recursive: new(true), Genres: []string{"Horror", "Animation"}})).Model
		if res.TotalRecordCount != 2 {
			t.Errorf("Genres=Horror,Animation found %d, want Alien and Princess Mononoke", res.TotalRecordCount)
		}
	})
	t.Run("Years", func(t *testing.T) {
		res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{ParentId: moviesID, Recursive: new(true), Years: []int{1982, 1999}})).Model
		if res.TotalRecordCount != 2 {
			t.Errorf("Years=1982,1999 found %d, want Blade Runner and The Thirteenth Floor", res.TotalRecordCount)
		}
	})
	t.Run("PersonIds", func(t *testing.T) {
		people := must(jfc.GetPersons(ctx, jf.GetPersonsOperationOptions{SearchTerm: ridleyScott})).Model
		i := slices.IndexFunc(people.Items, func(p jf.BaseItemDto) bool { return p.Name == ridleyScott })
		if i < 0 {
			t.Fatalf("GetPersons(%s) = %+v", ridleyScott, people.Items)
		}
		if people.Items[i].Id == "" || people.Items[i].Type != jf.BaseItemKindPerson {
			t.Errorf("person did not decode: %+v", people.Items[i])
		}
		res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{ParentId: moviesID, Recursive: new(true), PersonIds: []string{people.Items[i].Id}})).Model
		if res.TotalRecordCount != 2 {
			t.Errorf("PersonIds=%s found %d, want Alien and Blade Runner", ridleyScott, res.TotalRecordCount)
		}
	})
	t.Run("IsFavorite", func(t *testing.T) {
		id := byTitle[bladeRunner].Id
		if ud := must(jfc.MarkFavoriteItem(ctx, id, jf.MarkFavoriteItemOperationOptions{UserId: adminID})).Model; !pointer.From(ud.IsFavorite) {
			t.Error("MarkFavoriteItem returned IsFavorite=false")
		}
		res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{
			UserId: adminID, ParentId: moviesID, Recursive: new(true), Filters: []jf.ItemFilter{jf.ItemFilterIsFavorite},
		})).Model
		if len(res.Items) != 1 || res.Items[0].Id != id {
			t.Errorf("Filters=IsFavorite found %+v, want %s", res.Items, bladeRunner)
		}
		if res.Items[0].UserData == nil || !pointer.From(res.Items[0].UserData.IsFavorite) {
			t.Errorf("UserData = %+v, want IsFavorite", res.Items[0].UserData)
		}
		if ud := must(jfc.UnmarkFavoriteItem(ctx, id, jf.UnmarkFavoriteItemOperationOptions{UserId: adminID})).Model; pointer.From(ud.IsFavorite) {
			t.Error("UnmarkFavoriteItem returned IsFavorite=true")
		}
		if res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{UserId: adminID, ParentId: moviesID, Recursive: new(true), IsFavorite: new(true)})).Model; len(res.Items) != 0 {
			t.Errorf("IsFavorite=true still finds %d after unfavouriting", len(res.Items))
		}
	})
	t.Run("Counts", func(t *testing.T) {
		// the counts are the whole server's, which may hold more libraries
		// than these (the messy ones, a run's that crashed): each is what the
		// user's own item query counts, and at least the fixtures'
		counts := must(jfc.GetItemCounts(ctx, jf.GetItemCountsOperationOptions{UserId: adminID})).Model
		count := func(kind jf.BaseItemKind) int {
			return must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{UserId: adminID, Recursive: new(true), IncludeItemTypes: []jf.BaseItemKind{kind}, Limit: new(1)})).Model.TotalRecordCount
		}
		if m, s, e := count(jf.BaseItemKindMovie), count(jf.BaseItemKindSeries), count(jf.BaseItemKindEpisode); counts.MovieCount != m || counts.SeriesCount != s || counts.EpisodeCount != e {
			t.Errorf("counts = %+v, and the user's items count %d films, %d series, %d episodes", counts, m, s, e)
		}
		if counts.MovieCount < len(movies) || counts.SeriesCount < len(shows) || counts.EpisodeCount < sdkShows.Episodes {
			t.Errorf("counts = %+v, fewer than the fixtures", counts)
		}
	})
}

// TestJFItem covers the single-item shape, the update round trip, and the
// reads that hang off one item.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFItem(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	id := jfMovie(t, aliens).Id
	// the album the instant mix is made from
	musicID := jfLibrary(t, sdkMusic)
	album := jfAlbum(t, musicID, darkSide)

	full := must(jfc.GetItem(ctx, id, jf.GetItemOperationOptions{UserId: adminID})).Model
	if full.Name != aliens || full.ProductionYear != 1986 || full.Path == "" || full.Overview == "" || len(full.MediaSources) == 0 || full.RunTimeTicks == 0 {
		t.Fatalf("GetItem did not decode the full shape: %+v", full)
	}
	if full.UserData == nil {
		t.Error("GetItem with UserId has no UserData")
	}
	if len(full.MediaSources[0].MediaStreams) == 0 || full.MediaSources[0].Container == "" {
		t.Errorf("MediaSources[0] = %+v, want streams and a container", full.MediaSources[0])
	}

	// update: post the DTO back with three fields changed
	original := *full
	full.Overview, full.Genres, full.Tags = "SDK overview", []string{"SDK Genre", "Action"}, []string{"sdk"}
	if _, err := jfc.UpdateItem(ctx, id, *full); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = jfc.UpdateItem(context.WithoutCancel(ctx), id, original) })
	got := must(jfc.GetItem(ctx, id, jf.GetItemOperationOptions{UserId: adminID})).Model
	if got.Overview != "SDK overview" || !slices.Equal(got.Genres, []string{"SDK Genre", "Action"}) || !slices.Equal(got.Tags, []string{"sdk"}) {
		t.Errorf("after UpdateItem: overview %q, genres %v, tags %v", got.Overview, got.Genres, got.Tags)
	}
	if got.Name != aliens || got.ProductionYear != 1986 || got.ProviderIds["Tmdb"] != "679" {
		t.Errorf("UpdateItem disturbed the fields it should not have: %+v", got)
	}

	// a full refresh with ReplaceAllMetadata puts the provider's overview back
	if _, err := jfc.RefreshItem(ctx, id, jf.RefreshItemOperationOptions{
		MetadataRefreshMode: jf.MetadataRefreshModeFullRefresh, ImageRefreshMode: jf.MetadataRefreshModeDefault, ReplaceAllMetadata: new(true),
	}); err != nil {
		t.Fatal(err)
	}
	// (GetItem needs a UserId: without one the server answers 500, "Guid
	// can't be empty", rather than serving the item without user data)
	if !poll(2*time.Minute, func() bool {
		it, err := jfc.GetItem(ctx, id, jf.GetItemOperationOptions{UserId: adminID})
		return err == nil && it.Model.Overview != "SDK overview" && it.Model.Overview != ""
	}) {
		t.Error("RefreshItem never replaced the overview")
	}

	t.Run("Similar", func(t *testing.T) {
		res := must(jfc.GetSimilarItems(ctx, id, jf.GetSimilarItemsOperationOptions{UserId: adminID, Limit: new(3)})).Model
		if len(res.Items) == 0 {
			t.Fatal("GetSimilarItems found nothing among seven other films")
		}
		for _, it := range res.Items {
			if it.Id == "" || it.Name == "" || it.Id == id {
				t.Errorf("similar item = %+v", it)
			}
		}
	})
	t.Run("InstantMix", func(t *testing.T) {
		// a mix is music: from a film there is none
		if res := must(jfc.GetInstantMixFromItem(ctx, id, jf.GetInstantMixFromItemOperationOptions{UserId: adminID, Limit: new(5)})).Model; len(res.Items) != 0 {
			t.Errorf("an instant mix from a film = %+v, want nothing", res.Items)
		}
		// from an album it is the songs of the album's genre, the other
		// album by the same artist's included
		mix := must(jfc.GetInstantMixFromItem(ctx, album.Id, jf.GetInstantMixFromItemOperationOptions{UserId: adminID, Limit: new(50)})).Model
		genre := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{ParentId: musicID, Recursive: new(true), IncludeItemTypes: []jf.BaseItemKind{jf.BaseItemKindAudio}, Genres: []string{progressiveRock}})).Model
		if len(genre.Items) == 0 {
			t.Fatalf("no song in %s", progressiveRock)
		}
		want := map[string]bool{}
		for _, s := range genre.Items {
			want[s.Id] = true
		}
		got := map[string]bool{}
		for _, it := range mix.Items {
			if it.Type != jf.BaseItemKindAudio || !want[it.Id] {
				t.Errorf("the mix from %s holds %s (%s), which is not a %s song", darkSide, it.Name, it.Type, progressiveRock)
			}
			got[it.Id] = true
		}
		if len(got) != len(want) {
			t.Errorf("the mix from %s holds %d of the %d %s songs", darkSide, len(got), len(want), progressiveRock)
		}
	})
	t.Run("Images", func(t *testing.T) {
		images := must(jfc.GetItemImageInfos(ctx, id)).Model
		i := slices.IndexFunc(images, func(img jf.ImageInfo) bool { return img.ImageType == jf.ImageTypePrimary })
		if i < 0 {
			t.Fatalf("no Primary image among %+v; the fixture has a poster.jpg", images)
		}
		if images[i].Path == "" || images[i].Width == 0 || images[i].Height == 0 || images[i].Size == 0 {
			t.Errorf("Primary image = %+v", images[i])
		}
	})
	t.Run("ExternalIds", func(t *testing.T) {
		infos := must(jfc.GetExternalIdInfos(ctx, id)).Model
		if !slices.ContainsFunc(infos, func(e jf.ExternalIdInfo) bool { return e.Key == "Tmdb" && e.Name != "" }) {
			t.Errorf("GetExternalIdInfos = %+v, want a Tmdb entry with a url format", infos)
		}
	})
	t.Run("Ancestors", func(t *testing.T) {
		anc := must(jfc.GetAncestors(ctx, id, jf.GetAncestorsOperationOptions{UserId: adminID})).Model
		if !slices.ContainsFunc(anc, func(a jf.BaseItemDto) bool { return a.Name == sdkMovies.Name }) {
			t.Errorf("GetAncestors = %+v, want %s among them", anc, sdkMovies.Name)
		}
	})
	t.Run("PlaybackInfo", func(t *testing.T) {
		info := must(jfc.GetPlaybackInfo(ctx, id, jf.GetPlaybackInfoOperationOptions{UserId: adminID})).Model
		if len(info.MediaSources) == 0 || info.PlaySessionId == "" {
			t.Errorf("GetPlaybackInfo = %+v", info)
		}
	})
	t.Run("UserData", func(t *testing.T) {
		ud := must(jfc.GetItemUserData(ctx, id, jf.GetItemUserDataOperationOptions{UserId: adminID})).Model
		if ud.ItemId != id || ud.Key == "" {
			t.Errorf("GetItemUserData = %+v", ud)
		}
	})
	t.Run("Extras", func(t *testing.T) {
		// none of these have content for a bare movie; they must still answer
		if _, err := jfc.GetThemeMedia(ctx, id, jf.GetThemeMediaOperationOptions{UserId: adminID}); err != nil {
			t.Error(err)
		}
		if extras := must(jfc.GetSpecialFeatures(ctx, id, jf.GetSpecialFeaturesOperationOptions{UserId: adminID})).Model; len(extras) != 0 {
			t.Errorf("GetSpecialFeatures = %+v", extras)
		}
		if trailers := must(jfc.GetLocalTrailers(ctx, id, jf.GetLocalTrailersOperationOptions{UserId: adminID})).Model; len(trailers) != 0 {
			t.Errorf("GetLocalTrailers = %+v", trailers)
		}
		if intros := must(jfc.GetIntros(ctx, id, jf.GetIntrosOperationOptions{UserId: adminID})).Model; len(intros.Items) != 0 {
			t.Errorf("GetIntros = %+v", intros)
		}
		if parts := must(jfc.GetAdditionalPart(ctx, id, jf.GetAdditionalPartOperationOptions{UserId: adminID})).Model; len(parts.Items) != 0 {
			t.Errorf("GetAdditionalPart = %+v", parts)
		}
		if segs := must(jfc.GetItemSegments(ctx, id, jf.GetItemSegmentsOperationOptions{})).Model; len(segs.Items) != 0 {
			t.Errorf("GetItemSegments = %+v", segs)
		}
		// a movie has no lyrics: a 404, not a decode failure
		if _, err := jfc.GetLyrics(ctx, id); !client.IsNotFound(err) {
			t.Errorf("GetLyrics on a movie = %v, want a 404", err)
		}
	})
}

// TestJFCatalogue covers the by-name folders and the browse endpoints.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFCatalogue(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	moviesID := jfLibrary(t, sdkMovies)

	genres := must(jfc.GetGenres(ctx, jf.GetGenresOperationOptions{ParentId: moviesID, UserId: adminID})).Model
	if !slices.ContainsFunc(genres.Items, func(g jf.BaseItemDto) bool { return g.Name == "Horror" && g.Id != "" && g.Type == jf.BaseItemKindGenre }) {
		t.Errorf("GetGenres = %+v, want Horror", genres.Items)
	}
	studios := must(jfc.GetStudios(ctx, jf.GetStudiosOperationOptions{ParentId: moviesID, UserId: adminID})).Model
	if len(studios.Items) == 0 || studios.Items[0].Name == "" || studios.Items[0].Id == "" {
		t.Errorf("GetStudios = %+v, want the studios TMDB fills in", studios.Items)
	}
	years := must(jfc.GetYears(ctx, jf.GetYearsOperationOptions{ParentId: moviesID, Recursive: new(true), UserId: adminID})).Model
	if !slices.ContainsFunc(years.Items, func(y jf.BaseItemDto) bool { return y.Name == "1979" }) {
		t.Errorf("GetYears = %+v, want 1979", years.Items)
	}
	filters := must(jfc.GetQueryFilters(ctx, jf.GetQueryFiltersOperationOptions{ParentId: moviesID, UserId: adminID, Recursive: new(true)})).Model
	if !slices.ContainsFunc(filters.Genres, func(g jf.NameGuidPair) bool { return g.Name == "Horror" && g.Id != "" }) {
		t.Errorf("GetQueryFilters = %+v, want Horror among the genres", filters)
	}
	legacy := must(jfc.GetQueryFiltersLegacy(ctx, jf.GetQueryFiltersLegacyOperationOptions{ParentId: moviesID, UserId: adminID})).Model
	if !slices.Contains(legacy.Genres, "Horror") || !slices.Contains(legacy.Years, 1979) {
		t.Errorf("GetQueryFiltersLegacy = %+v", legacy)
	}
	hints := must(jfc.GetSearchHints(ctx, jf.GetSearchHintsOperationOptions{SearchTerm: "Alien", UserId: adminID, IncludeItemTypes: []jf.BaseItemKind{jf.BaseItemKindMovie}})).Model
	if hints.TotalRecordCount < 2 || !slices.ContainsFunc(hints.SearchHints, func(h jf.SearchHint) bool { return h.Name == alien && h.Id != "" && h.ProductionYear == 1979 }) {
		t.Errorf("GetSearchHints(Alien) = %+v", hints)
	}
	latest := must(jfc.GetLatestMedia(ctx, jf.GetLatestMediaOperationOptions{UserId: adminID, ParentId: moviesID, Limit: new(3)})).Model
	if len(latest) != 3 || latest[0].Id == "" {
		t.Errorf("GetLatestMedia = %d items", len(latest))
	}
	suggestions := must(jfc.GetSuggestions(ctx, jf.GetSuggestionsOperationOptions{UserId: adminID, Type: []jf.BaseItemKind{jf.BaseItemKindMovie}, Limit: new(2)})).Model
	if len(suggestions.Items) == 0 {
		t.Error("GetSuggestions returned nothing")
	}
	root := must(jfc.GetRootFolder(ctx, jf.GetRootFolderOperationOptions{UserId: adminID})).Model
	if root.Id == "" || !pointer.From(root.IsFolder) {
		t.Errorf("GetRootFolder = %+v", root)
	}
	views := must(jfc.GetUserViews(ctx, jf.GetUserViewsOperationOptions{UserId: adminID})).Model
	if !slices.ContainsFunc(views.Items, func(v jf.BaseItemDto) bool { return v.Id == moviesID && v.CollectionType == jf.CollectionTypeMovies }) {
		t.Errorf("GetUserViews = %+v, want %s", views.Items, sdkMovies.Name)
	}

	// /Trailers reads the calling user's claims, which an API key has none
	// of: a NullReferenceException behind a 500, userId or not
	if _, err := jfc.GetTrailers(ctx, jf.GetTrailersOperationOptions{UserId: adminID}); client.StatusCode(err) != 500 {
		t.Errorf("GetTrailers with an API key = %v, want a 500", err)
	}
	// no channel plugin is installed
	if res := must(jfc.GetChannels(ctx, jf.GetChannelsOperationOptions{UserId: adminID})).Model; res.TotalRecordCount != 0 || len(res.Items) != 0 {
		t.Errorf("GetChannels = %+v", res)
	}

	// the music families, read by library: none in the film library, and the
	// fixtures' in the music one (whatever else the server holds, so the
	// counts do not depend on what ran before). Jellyfin documents no music
	// genre listing: /Genres of a music library lists its music genres.
	for _, lib := range []string{moviesID, jfLibrary(t, sdkMusic)} {
		wantArtists, wantAlbumArtists, wantGenres := []string{}, []string{}, []string{}
		if lib != moviesID {
			// the tracks' artists, The Pink Floyd among them (one track's
			// tag spells it so), and the albums': Various Artists over Battle
			// Tapes' Polygon, which Jellyfin lists as an album artist alone
			wantArtists = []string{"Battle Tapes", "Coyote Kisses", "Pink Floyd", "SirensCeol", "The Pink Floyd"}
			wantAlbumArtists = []string{"Coyote Kisses", "Pink Floyd", "SirensCeol", "Various Artists"}
			wantGenres = []string{"Electronic", "Electronica", progressiveRock}
		}
		names := func(items []jf.BaseItemDto) []string {
			out := make([]string, 0, len(items))
			for i := range items {
				out = append(out, items[i].Name)
			}
			slices.Sort(out)
			return out
		}
		artists := must(jfc.GetArtists(ctx, jf.GetArtistsOperationOptions{ParentId: lib, UserId: adminID})).Model
		if got := names(artists.Items); !slices.Equal(got, wantArtists) || artists.TotalRecordCount != len(wantArtists) {
			t.Errorf("GetArtists(%s) = %v (%d), want %v", lib, got, artists.TotalRecordCount, wantArtists)
		}
		albumArtists := must(jfc.GetAlbumArtists(ctx, jf.GetAlbumArtistsOperationOptions{ParentId: lib, UserId: adminID})).Model
		if got := names(albumArtists.Items); !slices.Equal(got, wantAlbumArtists) || albumArtists.TotalRecordCount != len(wantAlbumArtists) {
			t.Errorf("GetAlbumArtists(%s) = %v (%d), want %v", lib, got, albumArtists.TotalRecordCount, wantAlbumArtists)
		}
		if lib == moviesID {
			continue // the film library's genres are the films'
		}
		genres := must(jfc.GetGenres(ctx, jf.GetGenresOperationOptions{ParentId: lib, UserId: adminID})).Model
		if got := names(genres.Items); !slices.Equal(got, wantGenres) || genres.Items[0].Type != jf.BaseItemKindMusicGenre {
			t.Errorf("GetGenres(%s) = %v (%+v), want the music genres %v", lib, got, genres.Items, wantGenres)
		}
	}
}

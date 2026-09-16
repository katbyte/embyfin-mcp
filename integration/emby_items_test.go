//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/go-kt/pointer"
)

// TestEmbyItemsQuery drives /Items and the per-user /Users/{id}/Items
// through every parameter the neutral layer uses, against the facts in the
// fixture table.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyItemsQuery(t *testing.T) {
	ctx := skipUnlessEmby(t)
	moviesID := embyLibrary(t, sdkMovies)
	embyLibrary(t, sdkShows) // the counts below span both
	// (ProductionYear is not among the fields the server sends by default)
	const fields = "Path,ProviderIds,Overview,Genres,Tags,People,Studios,DateCreated,CommunityRating,ProductionYear"

	all := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{
		ParentId: moviesID, Recursive: new(true), IncludeItemTypes: "Movie",
		Fields: fields, SortBy: "SortName", SortOrder: "Ascending",
	})).Model
	if all.TotalRecordCount != len(movies) || len(all.Items) != len(movies) {
		t.Fatalf("movies = %d/%d, want %d", len(all.Items), all.TotalRecordCount, len(movies))
	}
	byTitle := map[string]emby.BaseItemDto{}
	for _, it := range all.Items {
		byTitle[it.Name] = it
		if it.Id == "" || it.Type != "Movie" || it.Path == "" || it.Overview == "" || it.DateCreated == "" || it.ServerId == "" {
			t.Errorf("%s did not decode: %+v", it.Name, it)
		}
		if it.MediaType != "Video" {
			t.Errorf("%s: MediaType %q", it.Name, it.MediaType)
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
		if !slices.ContainsFunc(it.People, func(p emby.BaseItemPerson) bool { return p.Name == m.Director && p.Type == "Director" }) {
			t.Errorf("%s: people %+v lack director %s", m.Title, it.People, m.Director)
		}
	}
	if all.Items[0].Name != alien || all.Items[len(all.Items)-1].Name != "The Thirteenth Floor" {
		t.Errorf("sorted by name: first %q, last %q", all.Items[0].Name, all.Items[len(all.Items)-1].Name)
	}

	// the providers filled what the nfo does not carry
	if a := byTitle[alien]; a.CommunityRating == 0 || len(a.Studios) == 0 || a.Studios[0].Name == "" {
		t.Errorf("%s has no rating or studios after a scan with providers on: rating %v, studios %+v", alien, a.CommunityRating, a.Studios)
	}

	t.Run("PerUser", func(t *testing.T) {
		res := must(embyc.GetUsersByUserIdItems(ctx, adminID, emby.GetUsersByUserIdItemsOperationOptions{
			ParentId: moviesID, Recursive: new(true), IncludeItemTypes: "Movie", Fields: "Path",
		})).Model
		if res.TotalRecordCount != len(movies) {
			t.Errorf("/Users/{id}/Items found %d, want %d", res.TotalRecordCount, len(movies))
		}
		for _, it := range res.Items {
			if it.UserData == nil || it.Path == "" {
				t.Errorf("%s in the per-user shape has no UserData or Path", it.Name)
			}
		}
	})
	t.Run("NameStartsWith", func(t *testing.T) {
		res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{ParentId: moviesID, Recursive: new(true), IncludeItemTypes: "Movie", NameStartsWith: "Dune"})).Model
		if res.TotalRecordCount != 2 {
			t.Errorf("NameStartsWith=Dune found %d, want Dune and Dune: Part Two", res.TotalRecordCount)
		}
	})
	t.Run("SearchTerm", func(t *testing.T) {
		// the Emby spec has no SearchTerm on /Items (the server reads one);
		// the emby-search-term workaround adds it to the generated options
		res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{ParentId: moviesID, Recursive: new(true), IncludeItemTypes: "Movie", SearchTerm: "Dune"})).Model
		if res.TotalRecordCount != 2 {
			t.Errorf("SearchTerm=Dune found %d, want Dune and Dune: Part Two", res.TotalRecordCount)
		}
	})
	t.Run("Ids", func(t *testing.T) {
		res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{Ids: byTitle[alien].Id + "," + byTitle[aliens].Id})).Model
		if len(res.Items) != 2 {
			t.Errorf("Ids found %d, want 2", len(res.Items))
		}
	})
	t.Run("Paging", func(t *testing.T) {
		res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{
			ParentId: moviesID, Recursive: new(true), IncludeItemTypes: "Movie",
			SortBy: "ProductionYear", SortOrder: "Descending", StartIndex: 2, Limit: 3, Fields: "ProductionYear",
		})).Model
		if len(res.Items) != 3 || res.TotalRecordCount != len(movies) {
			t.Errorf("StartIndex=2 Limit=3: %d items of %d", len(res.Items), res.TotalRecordCount)
		}
		if res.Items[0].ProductionYear != 2016 {
			t.Errorf("third newest is %s (%d), want Arrival (2016)", res.Items[0].Name, res.Items[0].ProductionYear)
		}
	})
	t.Run("Genres", func(t *testing.T) {
		res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{ParentId: moviesID, Recursive: new(true), Genres: "Horror|Animation"})).Model
		if res.TotalRecordCount != 2 {
			t.Errorf("Genres=Horror|Animation found %d, want Alien and Princess Mononoke", res.TotalRecordCount)
		}
	})
	t.Run("Years", func(t *testing.T) {
		res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{ParentId: moviesID, Recursive: new(true), Years: "1982,1999"})).Model
		if res.TotalRecordCount != 2 {
			t.Errorf("Years=1982,1999 found %d, want Blade Runner and The Thirteenth Floor", res.TotalRecordCount)
		}
	})
	t.Run("PersonIds", func(t *testing.T) {
		people := must(embyc.GetPersons(ctx, emby.GetPersonsOperationOptions{NameStartsWith: ridleyScott})).Model
		i := slices.IndexFunc(people.Items, func(p emby.BaseItemDto) bool { return p.Name == ridleyScott })
		if i < 0 {
			t.Fatalf("GetPersons(%s) = %+v", ridleyScott, people.Items)
		}
		if people.Items[i].Id == "" || people.Items[i].Type != "Person" {
			t.Errorf("person did not decode: %+v", people.Items[i])
		}
		res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{ParentId: moviesID, Recursive: new(true), PersonIds: people.Items[i].Id})).Model
		if res.TotalRecordCount != 2 {
			t.Errorf("PersonIds=%s found %d, want Alien and Blade Runner", ridleyScott, res.TotalRecordCount)
		}
	})
	t.Run("AnyProviderIdEquals", func(t *testing.T) {
		res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{Recursive: new(true), AnyProviderIdEquals: "imdb.tt0078748"})).Model
		if len(res.Items) != 1 || res.Items[0].Name != alien {
			t.Errorf("AnyProviderIdEquals=imdb.tt0078748 found %+v, want %s", res.Items, alien)
		}
	})
	t.Run("IsFavorite", func(t *testing.T) {
		id := byTitle[bladeRunner].Id
		if ud := must(embyc.PostUsersByUserIdFavoriteItemsById(ctx, adminID, id)).Model; !pointer.From(ud.IsFavorite) {
			t.Error("favouriting returned IsFavorite=false")
		}
		res := must(embyc.GetUsersByUserIdItems(ctx, adminID, emby.GetUsersByUserIdItemsOperationOptions{ParentId: moviesID, Recursive: new(true), Filters: "IsFavorite"})).Model
		if len(res.Items) != 1 || res.Items[0].Id != id {
			t.Errorf("Filters=IsFavorite found %+v, want %s", res.Items, bladeRunner)
		}
		if res.Items[0].UserData == nil || !pointer.From(res.Items[0].UserData.IsFavorite) {
			t.Errorf("UserData = %+v, want IsFavorite", res.Items[0].UserData)
		}
		if ud := must(embyc.DeleteUsersByUserIdFavoriteItemsById(ctx, adminID, id)).Model; pointer.From(ud.IsFavorite) {
			t.Error("unfavouriting returned IsFavorite=true")
		}
		if res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{UserId: adminID, ParentId: moviesID, Recursive: new(true), IsFavorite: new(true)})).Model; len(res.Items) != 0 {
			t.Errorf("IsFavorite=true still finds %d after unfavouriting", len(res.Items))
		}
	})
	t.Run("Counts", func(t *testing.T) {
		counts := must(embyc.GetItemsCounts(ctx, emby.GetItemsCountsOperationOptions{UserId: adminID})).Model
		if counts.MovieCount < len(movies) || counts.SeriesCount != len(shows) || counts.EpisodeCount != sdkShows.Episodes {
			t.Errorf("counts = %+v", counts)
		}
	})
}

// TestEmbyItem covers the single-item shape, the update round trip, and the
// reads that hang off one item.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyItem(t *testing.T) {
	ctx := skipUnlessEmby(t)
	id := embyMovie(t, aliens).Id

	full := must(embyc.GetUsersByUserIdItemsById(ctx, adminID, id)).Model
	if full.Name != aliens || full.ProductionYear != 1986 || full.Path == "" || full.Overview == "" || len(full.MediaSources) == 0 || full.RunTimeTicks == 0 {
		t.Fatalf("the full item did not decode: %+v", full)
	}
	if full.UserData == nil {
		t.Error("the per-user item has no UserData")
	}
	if len(full.MediaSources[0].MediaStreams) == 0 || full.MediaSources[0].Container == "" {
		t.Errorf("MediaSources[0] = %+v, want streams and a container", full.MediaSources[0])
	}

	original := *full
	// the server keeps the tags a post leaves out, so the restore sends an
	// empty list rather than none to take the test's tag off again
	if original.TagItems == nil {
		original.TagItems = []emby.NameLongIdPair{}
	}
	// Emby reads the named records (GenreItems, and TagItems, which the
	// spec's BaseItemDto does not carry until the emby-item-tag-items
	// workaround adds it) and ignores the plain Genres and Tags lists on an
	// update, so genres are set through GenreItems and tags through TagItems
	full.Overview = "SDK overview"
	full.Genres, full.GenreItems = []string{"SDK Genre", "Action"}, []emby.NameLongIdPair{{Name: "SDK Genre"}, {Name: "Action"}}
	full.Tags, full.TagItems = []string{"sdk"}, []emby.NameLongIdPair{{Name: "sdk"}}
	if _, err := embyc.PostItemsByItemId(ctx, id, *full); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = embyc.PostItemsByItemId(context.WithoutCancel(ctx), id, original) })
	got := must(embyc.GetUsersByUserIdItemsById(ctx, adminID, id)).Model
	if got.Overview != "SDK overview" || !slices.Equal(got.Genres, []string{"SDK Genre", "Action"}) {
		t.Errorf("after the update: overview %q, genres %v", got.Overview, got.Genres)
	}
	// the tags come back in TagItems, which /Items sends when Fields names
	// it; the plain Tags list stays null
	if len(got.Tags) != 0 {
		t.Errorf("after the update tags = %v; the server now fills the plain Tags list", got.Tags)
	}
	tagged := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{Ids: id, Fields: "Tags,TagItems"})).Model
	if len(tagged.Items) != 1 || !slices.ContainsFunc(tagged.Items[0].TagItems, func(p emby.NameLongIdPair) bool { return p.Name == "sdk" }) {
		t.Errorf("after the update the item reads %+v, want sdk among its TagItems", tagged.Items)
	}
	if got.Name != aliens || got.ProductionYear != 1986 || got.ProviderIds["Tmdb"] != "679" {
		t.Errorf("the update disturbed the fields it should not have: %+v", got)
	}

	// a full refresh with ReplaceAllMetadata puts the provider's overview back
	if _, err := embyc.PostItemsByIdRefresh(ctx, id, emby.BaseRefreshRequest{}, emby.PostItemsByIdRefreshOperationOptions{
		MetadataRefreshMode: emby.MetadataRefreshModeFullRefresh, ReplaceAllMetadata: new(true),
	}); err != nil {
		t.Fatal(err)
	}
	if !poll(2*time.Minute, func() bool {
		it, err := embyc.GetUsersByUserIdItemsById(ctx, adminID, id)
		return err == nil && it.Model.Overview != "SDK overview" && it.Model.Overview != ""
	}) {
		t.Error("the refresh never replaced the overview")
	}

	t.Run("Similar", func(t *testing.T) {
		// the server finds nothing similar among the fixtures (it needs
		// more than shared genres and people); the shape is what is asserted
		res := must(embyc.GetItemsByIdSimilar(ctx, id, emby.GetItemsByIdSimilarOperationOptions{UserId: adminID, Limit: 3})).Model
		t.Logf("%d similar items", len(res.Items))
		for _, it := range res.Items {
			if it.Id == "" || it.Name == "" || it.Id == id {
				t.Errorf("similar item = %+v", it)
			}
		}
	})
	t.Run("InstantMix", func(t *testing.T) {
		res := must(embyc.GetItemsByIdInstantMix(ctx, id, emby.GetItemsByIdInstantMixOperationOptions{UserId: adminID, Limit: 5})).Model
		for _, it := range res.Items {
			if it.Id == "" || it.Name == "" {
				t.Errorf("instant mix item = %+v", it)
			}
		}
	})
	t.Run("Images", func(t *testing.T) {
		images := must(embyc.GetItemsByIdImages(ctx, id)).Model
		i := slices.IndexFunc(images, func(img emby.ImageInfo) bool { return img.ImageType == "Primary" })
		if i < 0 {
			t.Fatalf("no Primary image among %+v; the fixture has a poster.jpg", images)
		}
		// (Size is reported as 0)
		if images[i].Path == "" || images[i].Width == 0 || images[i].Height == 0 {
			t.Errorf("Primary image = %+v", images[i])
		}
	})
	t.Run("ExternalIds", func(t *testing.T) {
		infos := must(embyc.GetItemsByIdExternalIdInfos(ctx, id)).Model
		if !slices.ContainsFunc(infos, func(e emby.ExternalIdInfo) bool { return e.Key == "Tmdb" && e.Name != "" }) {
			t.Errorf("external id infos = %+v, want a Tmdb entry", infos)
		}
	})
	t.Run("Ancestors", func(t *testing.T) {
		anc := must(embyc.GetItemsByIdAncestors(ctx, id, emby.GetItemsByIdAncestorsOperationOptions{UserId: adminID})).Model
		if !slices.ContainsFunc(anc, func(a emby.BaseItemDto) bool { return a.Name == sdkMovies.Name }) {
			t.Errorf("ancestors = %+v, want %s among them", anc, sdkMovies.Name)
		}
	})
	t.Run("PlaybackInfo", func(t *testing.T) {
		info := must(embyc.GetItemsByIdPlaybackInfo(ctx, id, emby.GetItemsByIdPlaybackInfoOperationOptions{UserId: adminID})).Model
		if len(info.MediaSources) == 0 || info.PlaySessionId == "" {
			t.Errorf("playback info = %+v", info)
		}
	})
	t.Run("Extras", func(t *testing.T) {
		if _, err := embyc.GetItemsByIdCriticReviews(ctx, id, emby.GetItemsByIdCriticReviewsOperationOptions{}); err != nil {
			t.Error(err)
		}
		if _, err := embyc.GetItemsByIdThemeMedia(ctx, id, emby.GetItemsByIdThemeMediaOperationOptions{UserId: adminID}); err != nil {
			t.Error(err)
		}
		if intros := must(embyc.GetUsersByUserIdItemsByIdIntros(ctx, adminID, id, emby.GetUsersByUserIdItemsByIdIntrosOperationOptions{})).Model; len(intros.Items) != 0 {
			t.Errorf("intros = %+v", intros)
		}
		if parts := must(embyc.GetVideosByIdAdditionalParts(ctx, id, emby.GetVideosByIdAdditionalPartsOperationOptions{UserId: adminID})).Model; len(parts.Items) != 0 {
			t.Errorf("additional parts = %+v", parts)
		}
		// the delete preview wants a user behind the request: an API key
		// gets a 400, "Value cannot be null (Parameter 'user')"
		if _, err := embyc.GetItemsByIdDeleteInfo(ctx, id); client.StatusCode(err) != 400 {
			t.Errorf("GetItemsByIdDeleteInfo with an API key = %v, want a 400", err)
		}
	})
}

// TestEmbyCatalogue covers the by-name folders and the browse endpoints.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyCatalogue(t *testing.T) {
	ctx := skipUnlessEmby(t)
	moviesID := embyLibrary(t, sdkMovies)

	genres := must(embyc.GetGenres(ctx, emby.GetGenresOperationOptions{ParentId: moviesID, Recursive: new(true), UserId: adminID})).Model
	if !slices.ContainsFunc(genres.Items, func(g emby.BaseItemDto) bool { return g.Name == "Horror" && g.Id != "" && g.Type == "Genre" }) {
		t.Errorf("GetGenres = %+v, want Horror", genres.Items)
	}
	studios := must(embyc.GetStudios(ctx, emby.GetStudiosOperationOptions{ParentId: moviesID, Recursive: new(true), UserId: adminID})).Model
	if len(studios.Items) == 0 || studios.Items[0].Name == "" || studios.Items[0].Id == "" {
		t.Errorf("GetStudios = %+v, want the studios TMDB fills in", studios.Items)
	}
	years := must(embyc.GetYears(ctx, emby.GetYearsOperationOptions{ParentId: moviesID, Recursive: new(true), UserId: adminID})).Model
	if !slices.ContainsFunc(years.Items, func(y emby.UserLibraryTagItem) bool { return y.Name == "1979" }) {
		t.Errorf("GetYears = %+v, want 1979", years.Items)
	}
	tags := must(embyc.GetTags(ctx, emby.GetTagsOperationOptions{ParentId: moviesID, Recursive: new(true), UserId: adminID})).Model
	if tags.TotalRecordCount != 0 {
		t.Errorf("GetTags = %+v on a library with no tags", tags.Items)
	}
	prefixes := must(embyc.GetItemsPrefixes(ctx, emby.GetItemsPrefixesOperationOptions{ParentId: moviesID, Recursive: new(true), IncludeItemTypes: "Movie", UserId: adminID})).Model
	if !slices.ContainsFunc(prefixes, func(p emby.NameValuePair) bool { return p.Name == "A" }) {
		t.Errorf("GetItemsPrefixes = %+v, want A for Alien", prefixes)
	}
	latest := must(embyc.GetUsersByUserIdItemsLatest(ctx, adminID, emby.GetUsersByUserIdItemsLatestOperationOptions{ParentId: moviesID, Limit: 3})).Model
	if len(latest) != 3 || latest[0].Id == "" {
		t.Errorf("latest = %d items", len(latest))
	}

	// the families that are empty on a video-only server still answer in
	// shape. /Trailers answers with the user's top-level folders rather
	// than nothing, so what is asserted is that none of them is a trailer.
	for _, it := range must(embyc.GetTrailers(ctx, emby.GetTrailersOperationOptions{UserId: adminID})).Model.Items {
		if it.Type == "Trailer" || it.Id == "" {
			t.Errorf("GetTrailers = %+v", it)
		}
	}
	if res := must(embyc.GetMusicGenres(ctx, emby.GetMusicGenresOperationOptions{UserId: adminID})).Model; res.TotalRecordCount != 0 {
		t.Errorf("GetMusicGenres = %+v", res)
	}
	if res := must(embyc.GetArtists(ctx, emby.GetArtistsOperationOptions{UserId: adminID})).Model; res.TotalRecordCount != 0 {
		t.Errorf("GetArtists = %+v", res)
	}
	if res := must(embyc.GetChannels(ctx, emby.GetChannelsOperationOptions{UserId: adminID})).Model; res.TotalRecordCount != 0 {
		t.Errorf("GetChannels = %+v", res)
	}
}

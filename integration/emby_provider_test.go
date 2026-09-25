//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/emby"
)

// The endpoints that make the server ask a provider: remote images, remote
// search and identify. They run on the movie library, the one with the
// fetchers on, and their traffic is what the cassettes hold.

//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyRemoteImages(t *testing.T) {
	ctx := skipUnlessEmby(t)
	id := embyMovie(t, alien).Id

	providers := must(embyc.GetItemsByIdRemoteImagesProviders(ctx, id)).Model
	if !slices.ContainsFunc(providers, func(p emby.ImageProviderInfo) bool {
		return p.Name != "" && slices.Contains(p.SupportedImages, "Primary")
	}) {
		t.Fatalf("remote image providers = %+v, want one offering Primary", providers)
	}

	res := must(embyc.GetItemsByIdRemoteImages(ctx, id, emby.GetItemsByIdRemoteImagesOperationOptions{Type: "Primary", Limit: new(3), IncludeAllLanguages: new(false)})).Model
	if res.TotalRecordCount == 0 || len(res.Images) == 0 {
		t.Fatalf("remote Primary images for %s = %+v", alien, res)
	}
	img := res.Images[0]
	if img.Url == "" || img.ProviderName == "" || img.Type != "Primary" || img.Width == 0 || img.Height == 0 {
		t.Errorf("remote image = %+v", img)
	}

	// the film's poster before: the poster.jpg beside it
	primary := func() emby.ImageInfo {
		images := must(embyc.GetItemsByIdImages(ctx, id)).Model
		i := slices.IndexFunc(images, func(i emby.ImageInfo) bool { return i.ImageType == "Primary" })
		if i < 0 {
			t.Fatalf("%s has no Primary image among %+v", alien, images)
		}
		return images[i]
	}
	poster := ""
	if os.Getenv("EMBYFIN_TEST_DATA") != "" {
		poster = filepath.Join(dataDir(), "movies", "Alien (1979)", "poster.jpg")
		if _, err := os.Stat(poster); err != nil {
			t.Fatalf("the fixture's poster: %v", err)
		}
	}
	before := primary()
	if before.Path != "/media/movies/Alien (1979)/poster.jpg" || before.Width != 200 || before.Height != 300 {
		t.Fatalf("before the download the Primary image is %+v, want the fixture's 200x300 poster.jpg", before)
	}

	// download it as the item's poster; the proxy substitutes a placeholder
	// for the bytes (a 2x2 jpeg), which is still an image the server can size
	if _, err := embyc.PostItemsByIdRemoteImagesDownload(ctx, id, emby.ImagesBaseDownloadRemoteImage{}, emby.PostItemsByIdRemoteImagesDownloadOperationOptions{Type: emby.ImageTypePrimary, ProviderName: img.ProviderName, ImageUrl: img.Url}); err != nil {
		t.Fatal(err)
	}
	// the new poster is saved in the server's own metadata (the library does
	// not save artwork beside the media), and Emby deletes the poster.jpg it
	// replaces from the film's folder
	after := primary()
	if after.Path == before.Path || !strings.HasPrefix(after.Path, "/config/metadata/") || (after.Width == before.Width && after.Height == before.Height) {
		t.Errorf("after the download the Primary image is %+v, want the downloaded one in place of %+v", after, before)
	}
	if _, err := os.Stat(poster); poster != "" && !os.IsNotExist(err) {
		t.Errorf("after the download %s is still on disk (%v); Emby deleted the poster it replaced until now", poster, err)
	}
}

//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyRemoteSearch(t *testing.T) {
	ctx := skipUnlessEmby(t)
	movie := embyMovie(t, bladeRunner)
	// Emby item ids are integers; the search query carries one as a number
	itemID, err := strconv.ParseInt(movie.Id, 10, 64)
	if err != nil {
		t.Fatalf("item id %q is not an integer: %v", movie.Id, err)
	}

	results := must(embyc.PostItemsRemoteSearchMovie(ctx, emby.RemoteSearchQueryMovieInfo{
		ItemId:     itemID,
		SearchInfo: &emby.MovieInfo{Name: bladeRunner, Year: 1982},
	})).Model
	if len(results) == 0 {
		t.Fatalf("no remote search results for %s", bladeRunner)
	}
	i := slices.IndexFunc(results, func(r emby.RemoteSearchResult) bool { return r.ProviderIds["Tmdb"] == "78" })
	if i < 0 {
		t.Fatalf("remote search for %s did not return tmdb 78: %+v", bladeRunner, results)
	}
	hit := results[i]
	if hit.Name != bladeRunner || hit.ProductionYear != 1982 || hit.SearchProviderName == "" || hit.ImageUrl == "" {
		t.Errorf("remote search result = %+v", hit)
	}

	// applying the match the item already has is a safe identify
	if _, err := embyc.PostItemsRemoteSearchApplyById(ctx, movie.Id, hit, emby.PostItemsRemoteSearchApplyByIdOperationOptions{ReplaceAllImages: new(false)}); err != nil {
		t.Fatal(err)
	}
	if !poll(2*time.Minute, func() bool {
		it, err := embyc.GetUsersByUserIdItemsById(ctx, adminID, movie.Id)
		return err == nil && it.Model.ProviderIds["Tmdb"] == "78" && it.Model.Overview != ""
	}) {
		t.Error("after applying the search result the item lost its ids or overview")
	}

	series := must(embyc.PostItemsRemoteSearchSeries(ctx, emby.RemoteSearchQuerySeriesInfo{
		SearchInfo:               &emby.SeriesInfo{Name: severance, Year: 2022},
		IncludeDisabledProviders: new(true), // the show library has its fetchers off
	})).Model
	if !slices.ContainsFunc(series, func(r emby.RemoteSearchResult) bool {
		return r.Name == severance && (r.ProviderIds["Tmdb"] == "95396" || r.ProviderIds["Tvdb"] == "371980")
	}) {
		t.Errorf("series remote search for %s = %+v, want tmdb 95396 or tvdb 371980", severance, series)
	}
}

// Remote subtitle search: Emby ships an OpenSubtitles fetcher that has no
// account configured, so the search asks no one and answers with nothing.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyRemoteSubtitles(t *testing.T) {
	ctx := skipUnlessEmby(t)
	id := embyMovie(t, alien).Id

	subs, err := embyc.GetItemsByIdRemoteSearchSubtitlesByLanguage(ctx, id, "eng", emby.GetItemsByIdRemoteSearchSubtitlesByLanguageOperationOptions{})
	if err != nil {
		t.Fatalf("remote subtitle search: %v", err)
	}
	if len(subs.Model) != 0 {
		t.Errorf("remote subtitle search found %d without a provider account: %+v", len(subs.Model), subs.Model)
	}
}

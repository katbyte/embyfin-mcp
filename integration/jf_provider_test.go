//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/jf"
)

// The endpoints that make the server ask a provider: remote images, remote
// search and identify. They run on the movie library, the one with the
// fetchers on, and their traffic is what the cassettes hold.

//nolint:paralleltest // the tests share one server and its libraries
func TestJFRemoteImages(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	id := jfMovie(t, alien).Id

	providers := must(jfc.GetRemoteImageProviders(ctx, id)).Model
	if !slices.ContainsFunc(providers, func(p jf.ImageProviderInfo) bool {
		return p.Name == "TheMovieDb" && slices.Contains(p.SupportedImages, jf.ImageTypePrimary)
	}) {
		t.Fatalf("GetRemoteImageProviders = %+v, want TheMovieDb with Primary", providers)
	}

	res := must(jfc.GetRemoteImages(ctx, id, jf.GetRemoteImagesOperationOptions{Type: jf.ImageTypePrimary, Limit: new(3), IncludeAllLanguages: new(false)})).Model
	if res.TotalRecordCount == 0 || len(res.Images) == 0 {
		t.Fatalf("GetRemoteImages(Primary) for %s = %+v", alien, res)
	}
	img := res.Images[0]
	// (the TMDB plugin reports no dimensions for posters; the url, provider
	// and rating are what it fills in)
	if img.Url == "" || img.ProviderName == "" || img.Type != jf.ImageTypePrimary || img.CommunityRating == 0 || img.Language == "" {
		t.Errorf("remote image = %+v", img)
	}

	// the film's poster before: the poster.jpg beside it
	primary := func() jf.ImageInfo {
		images := must(jfc.GetItemImageInfos(ctx, id)).Model
		i := slices.IndexFunc(images, func(i jf.ImageInfo) bool { return i.ImageType == jf.ImageTypePrimary })
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
	if _, err := jfc.DownloadRemoteImage(ctx, id, jf.DownloadRemoteImageOperationOptions{Type: jf.ImageTypePrimary, ImageUrl: img.Url}); err != nil {
		t.Fatal(err)
	}
	// the new poster is saved in the server's own metadata (the library does
	// not save artwork beside the media), and the poster.jpg it replaces is
	// left in the film's folder (where Emby deletes it)
	after := primary()
	if after.Path == before.Path || !strings.HasPrefix(after.Path, "/config/metadata/") || (after.Width == before.Width && after.Height == before.Height) {
		t.Errorf("after the download the Primary image is %+v, want the downloaded one in place of %+v", after, before)
	}
	if _, err := os.Stat(poster); poster != "" && err != nil {
		t.Errorf("after the download %s: %v; Jellyfin left the poster it replaced until now", poster, err)
	}
}

//nolint:paralleltest // the tests share one server and its libraries
func TestJFRemoteSearch(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	movie := jfMovie(t, bladeRunner)

	results := must(jfc.GetMovieRemoteSearchResults(ctx, jf.MovieInfoRemoteSearchQuery{
		ItemId:     movie.Id,
		SearchInfo: &jf.MovieInfo{Name: bladeRunner, Year: 1982},
	})).Model
	if len(results) == 0 {
		t.Fatalf("no remote search results for %s", bladeRunner)
	}
	i := slices.IndexFunc(results, func(r jf.RemoteSearchResult) bool { return r.ProviderIds["Tmdb"] == "78" })
	if i < 0 {
		t.Fatalf("remote search for %s did not return tmdb 78: %+v", bladeRunner, results)
	}
	hit := results[i]
	if hit.Name != bladeRunner || hit.ProductionYear != 1982 || hit.SearchProviderName == "" || hit.Overview == "" || hit.ImageUrl == "" {
		t.Errorf("remote search result = %+v", hit)
	}

	// applying the match the item already has is a safe identify: the item
	// keeps its ids and gets a refresh
	if _, err := jfc.ApplySearchCriteria(ctx, movie.Id, hit, jf.ApplySearchCriteriaOperationOptions{ReplaceAllImages: new(false)}); err != nil {
		t.Fatal(err)
	}
	if !poll(2*time.Minute, func() bool {
		it, err := jfc.GetItem(ctx, movie.Id, jf.GetItemOperationOptions{UserId: adminID})
		return err == nil && it.Model.ProviderIds["Tmdb"] == "78" && it.Model.Overview != ""
	}) {
		t.Error("after ApplySearchCriteria the item lost its ids or overview")
	}

	series := must(jfc.GetSeriesRemoteSearchResults(ctx, jf.SeriesInfoRemoteSearchQuery{
		SearchInfo:               &jf.SeriesInfo{Name: severance, Year: 2022},
		IncludeDisabledProviders: new(true), // the show library has its fetchers off
	})).Model
	if !slices.ContainsFunc(series, func(r jf.RemoteSearchResult) bool { return r.ProviderIds["Tmdb"] == "95396" && r.Name == severance }) {
		t.Errorf("series remote search for %s = %+v, want tmdb 95396", severance, series)
	}
}

// Remote subtitle search: the container has no subtitle provider, so the
// call has nothing to return, and what matters is that the empty answer
// decodes rather than errors.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFRemoteSubtitles(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	id := jfMovie(t, alien).Id

	res, err := jfc.SearchRemoteSubtitles(ctx, id, "eng", jf.SearchRemoteSubtitlesOperationOptions{})
	if err != nil {
		t.Fatalf("SearchRemoteSubtitles: %v", err)
	}
	if subs := res.Model; len(subs) != 0 {
		t.Errorf("SearchRemoteSubtitles found %d without a provider: %+v", len(subs), subs)
	}
}

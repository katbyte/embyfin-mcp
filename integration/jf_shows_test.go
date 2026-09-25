//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/jf"
)

//nolint:paralleltest // the tests share one server and its libraries
func TestJFShows(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	showsID := jfLibrary(t, sdkShows)

	series := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{
		ParentId: showsID, Recursive: new(true), IncludeItemTypes: []jf.BaseItemKind{jf.BaseItemKindSeries},
		Fields: []jf.ItemFields{jf.ItemFieldsProviderIds, jf.ItemFieldsOverview}, SortBy: []jf.ItemSortBy{jf.ItemSortByName},
	})).Model
	if len(series.Items) != len(shows) {
		t.Fatalf("series = %d, want %d", len(series.Items), len(shows))
	}
	for _, s := range shows {
		i := slices.IndexFunc(series.Items, func(it jf.BaseItemDto) bool { return it.Name == s.Title })
		if i < 0 {
			t.Errorf("%s is missing", s.Title)
			continue
		}
		it := series.Items[i]
		if it.Type != jf.BaseItemKindSeries || it.ProductionYear != s.Year || it.ProviderIds["Tmdb"] != s.TMDB || it.ProviderIds["Tvdb"] != s.TVDB || it.Overview == "" {
			t.Errorf("%s = %+v; the nfo says %d/%s/%s", s.Title, it, s.Year, s.TMDB, s.TVDB)
		}
	}

	sev := jfSeries(t, severance)
	seasons := must(jfc.GetSeasons(ctx, sev.Id, jf.GetSeasonsOperationOptions{UserId: adminID})).Model
	if len(seasons.Items) != 2 {
		t.Fatalf("%s has %d seasons, want 2", severance, len(seasons.Items))
	}
	for _, s := range seasons.Items {
		if s.Id == "" || s.Type != jf.BaseItemKindSeason || s.IndexNumber == 0 || s.SeriesId != sev.Id || s.SeriesName != severance {
			t.Errorf("season = %+v", s)
		}
	}

	episodes := must(jfc.GetEpisodes(ctx, sev.Id, jf.GetEpisodesOperationOptions{UserId: adminID, Fields: []jf.ItemFields{jf.ItemFieldsOverview, jf.ItemFieldsPath}})).Model
	if episodes.TotalRecordCount != 4 {
		t.Fatalf("%s has %d episodes, want 4", severance, episodes.TotalRecordCount)
	}
	for _, e := range episodes.Items {
		if e.Id == "" || e.Type != jf.BaseItemKindEpisode || e.IndexNumber == 0 || e.ParentIndexNumber == 0 || e.SeasonId == "" || e.SeriesId != sev.Id || e.Overview == "" || e.Path == "" {
			t.Errorf("episode = %+v", e)
		}
	}
	// the first episode by its nfo title
	if e := episodes.Items[0]; e.Name != "Good News About Hell" || e.ParentIndexNumber != 1 || e.IndexNumber != 1 {
		t.Errorf("first episode = %s S%02dE%02d", e.Name, e.ParentIndexNumber, e.IndexNumber)
	}

	one := must(jfc.GetEpisodes(ctx, sev.Id, jf.GetEpisodesOperationOptions{UserId: adminID, Season: new(2)})).Model
	if len(one.Items) != 2 {
		t.Errorf("season 2 has %d episodes, want 2", len(one.Items))
	}
	bySeason := must(jfc.GetEpisodes(ctx, sev.Id, jf.GetEpisodesOperationOptions{UserId: adminID, SeasonId: seasons.Items[0].Id})).Model
	if len(bySeason.Items) != 2 {
		t.Errorf("SeasonId=%s has %d episodes, want 2", seasons.Items[0].Id, len(bySeason.Items))
	}
	// with the fetchers off nothing is known to be missing; the filter
	// still has to be accepted
	if missing := must(jfc.GetEpisodes(ctx, sev.Id, jf.GetEpisodesOperationOptions{UserId: adminID, IsMissing: new(true)})).Model; missing.TotalRecordCount != 0 {
		t.Errorf("IsMissing=true = %+v with the providers off", missing.Items)
	}
	if present := must(jfc.GetEpisodes(ctx, sev.Id, jf.GetEpisodesOperationOptions{UserId: adminID, IsMissing: new(false)})).Model; present.TotalRecordCount != 4 {
		t.Errorf("IsMissing=false = %d episodes, want 4", present.TotalRecordCount)
	}

	t.Run("NextUp", func(t *testing.T) {
		first := episodes.Items[0].Id
		if _, err := jfc.MarkPlayedItem(ctx, first, jf.MarkPlayedItemOperationOptions{UserId: adminID}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = jfc.MarkUnplayedItem(context.WithoutCancel(ctx), first, jf.MarkUnplayedItemOperationOptions{UserId: adminID})
		})

		next := must(jfc.GetNextUp(ctx, jf.GetNextUpOperationOptions{UserId: adminID, SeriesId: sev.Id, Fields: []jf.ItemFields{jf.ItemFieldsOverview}})).Model
		if len(next.Items) != 1 {
			t.Fatalf("GetNextUp for %s = %+v, want the second episode", severance, next.Items)
		}
		if e := next.Items[0]; e.Name != "Half Loop" || e.IndexNumber != 2 || e.SeriesName != severance {
			t.Errorf("next up = %+v", e)
		}
		if all := must(jfc.GetNextUp(ctx, jf.GetNextUpOperationOptions{UserId: adminID, ParentId: showsID})).Model; len(all.Items) != 1 {
			t.Errorf("GetNextUp across the library = %d, want 1", len(all.Items))
		}
	})

	if up := must(jfc.GetUpcomingEpisodes(ctx, jf.GetUpcomingEpisodesOperationOptions{UserId: adminID})).Model; len(up.Items) != 0 {
		t.Errorf("GetUpcomingEpisodes = %+v", up.Items)
	}
	if similar := must(jfc.GetSimilarShows(ctx, sev.Id, jf.GetSimilarShowsOperationOptions{UserId: adminID, Limit: new(2)})).Model; len(similar.Items) == 0 {
		t.Error("GetSimilarShows found nothing among the other two shows, one of them a drama too")
	}
}

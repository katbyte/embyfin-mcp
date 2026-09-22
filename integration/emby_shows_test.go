//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/emby"
)

//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyShows(t *testing.T) {
	ctx := skipUnlessEmby(t)
	showsID := embyLibrary(t, sdkShows)

	series := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{
		ParentId: showsID, Recursive: new(true), IncludeItemTypes: "Series",
		Fields: "ProviderIds,Overview,ProductionYear", SortBy: "SortName",
	})).Model
	if len(series.Items) != len(shows) {
		t.Fatalf("series = %d, want %d", len(series.Items), len(shows))
	}
	for _, s := range shows {
		i := slices.IndexFunc(series.Items, func(it emby.BaseItemDto) bool { return it.Name == s.Title })
		if i < 0 {
			t.Errorf("%s is missing", s.Title)
			continue
		}
		it := series.Items[i]
		if it.Type != "Series" || it.ProductionYear != s.Year || it.ProviderIds["Tmdb"] != s.TMDB || it.ProviderIds["Tvdb"] != s.TVDB || it.Overview == "" {
			t.Errorf("%s = %+v; the nfo says %d/%s/%s", s.Title, it, s.Year, s.TMDB, s.TVDB)
		}
	}

	sev := embySeries(t, severance)
	seasons := must(embyc.GetShowsByIdSeasons(ctx, sev.Id, emby.GetShowsByIdSeasonsOperationOptions{UserId: adminID})).Model
	if len(seasons.Items) != 2 {
		t.Fatalf("%s has %d seasons, want 2", severance, len(seasons.Items))
	}
	for _, s := range seasons.Items {
		if s.Id == "" || s.Type != "Season" || s.IndexNumber == 0 || s.SeriesId != sev.Id || s.SeriesName != severance {
			t.Errorf("season = %+v", s)
		}
	}

	episodes := must(embyc.GetShowsByIdEpisodes(ctx, sev.Id, emby.GetShowsByIdEpisodesOperationOptions{UserId: adminID, Fields: "Overview,Path"})).Model
	if episodes.TotalRecordCount != 4 {
		t.Fatalf("%s has %d episodes, want 4", severance, episodes.TotalRecordCount)
	}
	for _, e := range episodes.Items {
		if e.Id == "" || e.Type != "Episode" || e.IndexNumber == 0 || e.ParentIndexNumber == 0 || e.SeasonId == "" || e.SeriesId != sev.Id || e.Overview == "" || e.Path == "" {
			t.Errorf("episode = %+v", e)
		}
	}
	if e := episodes.Items[0]; e.Name != "Good News About Hell" || e.ParentIndexNumber != 1 || e.IndexNumber != 1 {
		t.Errorf("first episode = %s S%02dE%02d", e.Name, e.ParentIndexNumber, e.IndexNumber)
	}

	one := must(embyc.GetShowsByIdEpisodes(ctx, sev.Id, emby.GetShowsByIdEpisodesOperationOptions{UserId: adminID, Season: new(2)})).Model
	if len(one.Items) != 2 {
		t.Errorf("season 2 has %d episodes, want 2", len(one.Items))
	}
	bySeason := must(embyc.GetShowsByIdEpisodes(ctx, sev.Id, emby.GetShowsByIdEpisodesOperationOptions{UserId: adminID, SeasonId: seasons.Items[0].Id})).Model
	if len(bySeason.Items) != 2 {
		t.Errorf("SeasonId=%s has %d episodes, want 2", seasons.Items[0].Id, len(bySeason.Items))
	}
	// with the fetchers off nothing is known to be missing: the four on
	// disk are all the series holds, none of them virtual (Emby 4.10 has no
	// IsMissing filter on its item queries)
	all := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{ParentId: sev.Id, Recursive: new(true), IncludeItemTypes: "Episode"})).Model
	if all.TotalRecordCount != 4 || slices.ContainsFunc(all.Items, func(e emby.BaseItemDto) bool { return e.LocationType == "Virtual" }) {
		t.Errorf("%d episodes with the providers off, want the 4 on disk", all.TotalRecordCount)
	}

	t.Run("NextUp", func(t *testing.T) {
		first := episodes.Items[0].Id
		if _, err := embyc.PostUsersByUserIdPlayedItemsById(ctx, adminID, first, emby.PostUsersByUserIdPlayedItemsByIdOperationOptions{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = embyc.DeleteUsersByUserIdPlayedItemsById(context.WithoutCancel(ctx), adminID, first) })

		// Emby 4.10's default next-up mode answers nothing for an episode
		// marked played through the API; the legacy per-series mode is
		// what lists it, so the default answer is asserted empty and the
		// legacy one checked (LegacyNextUp, which the spec does not declare,
		// is added to the generated options by the emby-next-up-legacy
		// workaround)
		if next := must(embyc.GetShowsNextUp(ctx, emby.GetShowsNextUpOperationOptions{UserId: adminID, ParentId: showsID, Fields: "Overview"})).Model; len(next.Items) != 0 {
			t.Errorf("GetShowsNextUp = %+v, want nothing from the default mode", next.Items)
		}
		res, err := embyc.GetShowsNextUp(ctx, emby.GetShowsNextUpOperationOptions{UserId: adminID, ParentId: showsID, LegacyNextUp: new(true)})
		if err != nil {
			t.Fatal(err)
		}
		next := res.Model
		if len(next.Items) != 1 {
			t.Fatalf("legacy next up = %+v, want the second episode of %s", next.Items, severance)
		}
		if e := next.Items[0]; e.Name != "Half Loop" || e.IndexNumber != 2 || e.SeriesName != severance {
			t.Errorf("next up = %+v", e)
		}
	})
}

//go:build integration

package integration

import (
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/emby"
)

//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyPlaylists(t *testing.T) {
	ctx := skipUnlessEmby(t)
	a, b := embyMovie(t, "Princess Mononoke"), embyMovie(t, "Arrival")

	since := embyScanEnded(ctx)
	var created *emby.PlaylistsPlaylistCreationResult
	if err := embyRetry500(func() (err error) {
		var res emby.PostPlaylistsOperationResponse
		res, err = embyc.PostPlaylists(ctx, emby.PostPlaylistsOperationOptions{Name: "SDK Playlist", Ids: a.Id, MediaType: "Video"})
		created = res.Model
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if created == nil || created.Id == "" {
		t.Fatal("PostPlaylists returned no id")
	}
	id := created.Id
	t.Cleanup(func() { _, _ = embyc.DeleteItemsById(context.WithoutCancel(ctx), id) })

	// the first playlist on a server creates its playlists folder, which
	// queues a library scan; the scan validates every playlist and saves each
	// as it found it, so an add that lands meanwhile is answered, logged and
	// saved, then lost. Wait out a scan the create started.
	if poll(3*time.Second, func() bool {
		task := embyScanTask(ctx, t)
		return task.State != "Idle" || (task.LastExecutionResult != nil && task.LastExecutionResult.EndTimeUtc != since)
	}) {
		embyWaitForScan(ctx, t, since)
	}

	// the create adds the initial items before it answers
	var items *emby.QueryResultBaseItemDto
	if !poll(30*time.Second, func() bool {
		items = must(embyc.GetPlaylistsByIdItems(ctx, id, emby.GetPlaylistsByIdItemsOperationOptions{UserId: adminID})).Model
		return len(items.Items) == 1
	}) || items.TotalRecordCount != 1 || items.Items[0].Id != a.Id || items.Items[0].PlaylistItemId == "" {
		t.Fatalf("GetPlaylistsByIdItems = %+v", items)
	}

	if err := embyRetry500(func() error {
		_, err := embyc.PostPlaylistsByIdItems(ctx, id, emby.PostPlaylistsByIdItemsOperationOptions{Ids: b.Id, UserId: adminID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// the add is applied before it answers
	if !poll(30*time.Second, func() bool {
		items = must(embyc.GetPlaylistsByIdItems(ctx, id, emby.GetPlaylistsByIdItemsOperationOptions{UserId: adminID})).Model
		return len(items.Items) == 2
	}) || items.Items[1].Id != b.Id {
		t.Fatalf("after adding = %+v", items.Items)
	}

	// entries move by their playlist entry id, a number here
	moved, err := strconv.ParseInt(items.Items[1].PlaylistItemId, 10, 64)
	if err != nil {
		t.Fatalf("entry id %q is not a number: %v", items.Items[1].PlaylistItemId, err)
	}
	if _, err := embyc.PostPlaylistsByIdItemsByItemIdMoveByNewIndex(ctx, id, moved, 0); err != nil {
		t.Fatal(err)
	}
	if !poll(30*time.Second, func() bool {
		items = must(embyc.GetPlaylistsByIdItems(ctx, id, emby.GetPlaylistsByIdItemsOperationOptions{UserId: adminID})).Model
		return len(items.Items) == 2 && items.Items[0].Id == b.Id
	}) {
		t.Fatalf("after moving the second entry to the top = %+v", items.Items)
	}

	// refreshing the playlist (a library scan does) reads it back from its
	// file and numbers the entries 1..n in order, so an entry id read before
	// a refresh names another entry, or none, after it
	if _, err := embyc.PostItemsByIdRefresh(ctx, id, emby.BaseRefreshRequest{}, emby.PostItemsByIdRefreshOperationOptions{
		Recursive: new(true), MetadataRefreshMode: emby.MetadataRefreshModeDefault, ImageRefreshMode: emby.MetadataRefreshModeDefault,
	}); err != nil {
		t.Fatal(err)
	}
	if !poll(30*time.Second, func() bool {
		items = must(embyc.GetPlaylistsByIdItems(ctx, id, emby.GetPlaylistsByIdItemsOperationOptions{UserId: adminID})).Model
		return len(items.Items) == 2 && items.Items[0].PlaylistItemId == "1" && items.Items[1].PlaylistItemId == "2"
	}) || items.Items[0].Id != b.Id {
		t.Fatalf("after a refresh = %+v", items.Items)
	}

	// and a move or removal of an entry id the playlist does not hold is
	// answered 204 and changes nothing
	if _, err := embyc.PostPlaylistsByIdItemsByItemIdMoveByNewIndex(ctx, id, 99, 1); err != nil {
		t.Errorf("moving an unknown entry = %v", err)
	}
	if _, err := embyc.DeletePlaylistsByIdItems(ctx, id, emby.DeletePlaylistsByIdItemsOperationOptions{EntryIds: "99"}); err != nil {
		t.Errorf("removing an unknown entry = %v", err)
	}
	items = must(embyc.GetPlaylistsByIdItems(ctx, id, emby.GetPlaylistsByIdItemsOperationOptions{UserId: adminID})).Model
	if len(items.Items) != 2 || items.Items[0].Id != b.Id || items.Items[1].Id != a.Id {
		t.Errorf("after an unknown entry's move and removal = %+v", items.Items)
	}

	// entries are removed by their playlist entry id, not the item id
	if _, err := embyc.DeletePlaylistsByIdItems(ctx, id, emby.DeletePlaylistsByIdItemsOperationOptions{EntryIds: items.Items[1].PlaylistItemId}); err != nil {
		t.Fatal(err)
	}
	items = must(embyc.GetPlaylistsByIdItems(ctx, id, emby.GetPlaylistsByIdItemsOperationOptions{UserId: adminID})).Model
	if len(items.Items) != 1 || items.Items[0].Id != b.Id {
		t.Errorf("after removing = %+v", items.Items)
	}

	// the playlist is an item too
	if it := must(embyc.GetUsersByUserIdItemsById(ctx, adminID, id)).Model; it.Type != "Playlist" || it.Name != "SDK Playlist" {
		t.Errorf("the playlist as an item = %+v", it)
	}
}

//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyCollections(t *testing.T) {
	ctx := skipUnlessEmby(t)
	a, b, c := embyMovie(t, alien), embyMovie(t, aliens), embyMovie(t, bladeRunner)

	// the name is fixed because Emby looks it up on TMDB (the Collections
	// folder has the TheMovieDb fetcher on) and that search is in the
	// cassette. A second run against the same container, after the first
	// removed its libraries, can meet a FOREIGN KEY constraint (HTTP 500)
	// on this create: the make targets use a fresh container.
	const name = "SDK Collection"
	var created *emby.CollectionsCollectionCreationResult
	if err := embyRetry500(func() (err error) {
		var res emby.PostCollectionsOperationResponse
		res, err = embyc.PostCollections(ctx, emby.PostCollectionsOperationOptions{Name: name, Ids: a.Id + "," + b.Id})
		created = res.Model
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if created == nil || created.Id == "" {
		t.Fatal("PostCollections returned no id")
	}
	id := created.Id
	t.Cleanup(func() { _, _ = embyc.DeleteItemsById(context.WithoutCancel(ctx), id) })

	members := func() []string {
		res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{ParentId: id, SortBy: "SortName"})).Model
		ids := make([]string, 0, len(res.Items))
		for _, it := range res.Items {
			ids = append(ids, it.Id)
		}
		return ids
	}
	if got := members(); !slices.Equal(got, []string{a.Id, b.Id}) {
		t.Errorf("after PostCollections members = %v, want %s and %s", got, alien, aliens)
	}
	if err := embyRetry500(func() error {
		_, err := embyc.PostCollectionsByIdItems(ctx, id, emby.PostCollectionsByIdItemsOperationOptions{Ids: c.Id})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := members(); len(got) != 3 {
		t.Errorf("after adding members = %v", got)
	}
	if err := embyRetry500(func() error {
		_, err := embyc.DeleteCollectionsByIdItems(ctx, id, emby.DeleteCollectionsByIdItemsOperationOptions{Ids: a.Id})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := members(); !slices.Equal(got, []string{b.Id, c.Id}) {
		t.Errorf("after removing members = %v", got)
	}
	// removing an item the collection does not hold is answered 204 and
	// changes nothing, which is why lib/embyfin checks membership first
	if _, err := embyc.DeleteCollectionsByIdItems(ctx, id, emby.DeleteCollectionsByIdItemsOperationOptions{Ids: a.Id}); err != nil {
		t.Errorf("removing a non-member = %v", err)
	}
	if got := members(); !slices.Equal(got, []string{b.Id, c.Id}) {
		t.Errorf("after removing a non-member members = %v", got)
	}
	if it := must(embyc.GetUsersByUserIdItemsById(ctx, adminID, id)).Model; it.Type != "BoxSet" || it.Name != name || it.ChildCount != 2 {
		t.Errorf("the collection as an item = %+v", it)
	}
}

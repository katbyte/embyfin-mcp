//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

//nolint:paralleltest // the tests share one server and its libraries
func TestJFPlaylists(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	a, b := jfMovie(t, alien), jfMovie(t, aliens)

	created := must(jfc.CreatePlaylist(ctx, jf.CreatePlaylistDto{
		Name: "SDK Playlist", Ids: []string{a.Id}, UserId: adminID, MediaType: jf.MediaTypeVideo, IsPublic: new(true),
	})).Model
	if created.Id == "" {
		t.Fatal("CreatePlaylist returned no id")
	}
	id := created.Id
	t.Cleanup(func() { _, _ = jfc.DeleteItem(context.WithoutCancel(ctx), id) })

	// the playlist's own record and its share list want a user behind the
	// request: with an API key the server answers 400 ("Guid can't be
	// empty") rather than serving them, userId query or not
	if _, err := jfc.GetPlaylist(ctx, id); client.StatusCode(err) != 400 {
		t.Errorf("GetPlaylist with an API key = %v, want a 400", err)
	}
	if _, err := jfc.GetPlaylistUsers(ctx, id); client.StatusCode(err) != 400 {
		t.Errorf("GetPlaylistUsers with an API key = %v, want a 400", err)
	}
	// and so do the playlist's own update and move, which lib/embyfin works
	// around with the item update and a remove and re-add
	if _, err := jfc.UpdatePlaylist(ctx, id, jf.UpdatePlaylistDto{Name: "SDK Renamed"}); client.StatusCode(err) != 400 {
		t.Errorf("UpdatePlaylist with an API key = %v, want a 400", err)
	}
	if _, err := jfc.MoveItem(ctx, id, a.Id, 0); client.StatusCode(err) != 400 {
		t.Errorf("MoveItem with an API key = %v, want a 400", err)
	}

	items := must(jfc.GetPlaylistItems(ctx, id, jf.GetPlaylistItemsOperationOptions{UserId: adminID})).Model
	if items.TotalRecordCount != 1 || len(items.Items) != 1 || items.Items[0].Id != a.Id || items.Items[0].PlaylistItemId == "" {
		t.Fatalf("GetPlaylistItems = %+v", items)
	}

	if _, err := jfc.AddItemToPlaylist(ctx, id, jf.AddItemToPlaylistOperationOptions{Ids: []string{b.Id}, UserId: adminID}); err != nil {
		t.Fatal(err)
	}
	items = must(jfc.GetPlaylistItems(ctx, id, jf.GetPlaylistItemsOperationOptions{UserId: adminID})).Model
	if len(items.Items) != 2 || items.Items[1].Id != b.Id {
		t.Errorf("after AddItemToPlaylist = %+v", items.Items)
	}

	// an entry's id is its item's id (Emby numbers them), so a refresh
	// cannot change it
	if items.Items[0].PlaylistItemId != a.Id || items.Items[1].PlaylistItemId != b.Id {
		t.Errorf("entry ids = %s %s, want the item ids %s %s", items.Items[0].PlaylistItemId, items.Items[1].PlaylistItemId, a.Id, b.Id)
	}
	// a removal of an entry the playlist does not hold is answered 204 and
	// changes nothing
	if _, err := jfc.RemoveItemFromPlaylist(ctx, id, jf.RemoveItemFromPlaylistOperationOptions{EntryIds: []string{"00000000000000000000000000000001"}}); err != nil {
		t.Errorf("removing an unknown entry = %v", err)
	}
	if n := len(must(jfc.GetPlaylistItems(ctx, id, jf.GetPlaylistItemsOperationOptions{UserId: adminID})).Model.Items); n != 2 {
		t.Errorf("after removing an unknown entry the playlist holds %d", n)
	}

	// entries are removed by their playlist entry id
	if _, err := jfc.RemoveItemFromPlaylist(ctx, id, jf.RemoveItemFromPlaylistOperationOptions{EntryIds: []string{items.Items[0].PlaylistItemId}}); err != nil {
		t.Fatal(err)
	}
	items = must(jfc.GetPlaylistItems(ctx, id, jf.GetPlaylistItemsOperationOptions{UserId: adminID})).Model
	if len(items.Items) != 1 || items.Items[0].Id != b.Id {
		t.Errorf("after RemoveItemFromPlaylist = %+v", items.Items)
	}

	// the playlist is an item too
	if it := must(jfc.GetItem(ctx, id, jf.GetItemOperationOptions{UserId: adminID})).Model; it.Type != jf.BaseItemKindPlaylist || it.Name != "SDK Playlist" {
		t.Errorf("the playlist as an item = %+v", it)
	}
}

// TestJFCollections covers a collection's create, add and removal. A library
// scan's refresh of a collection writes its saved members over any added
// meanwhile (lib/embyfin re-sends for this; the behaviour is in
// api-defs/README.md), so the create waits out a scan first.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFCollections(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	a, b, c := jfMovie(t, alien), jfMovie(t, aliens), jfMovie(t, bladeRunner)

	jfScanIdle(ctx, t)
	const name = "SDK Collection"
	created := must(jfc.CreateCollection(ctx, jf.CreateCollectionOperationOptions{Name: name, Ids: []string{a.Id, b.Id}})).Model
	if created.Id == "" {
		t.Fatal("CreateCollection returned no id")
	}
	id := created.Id
	t.Cleanup(func() { jfDeleteCollection(context.WithoutCancel(ctx), t, id) })

	// a collection's children are linked, not parented, and the server only
	// resolves the links for a user: without UserId the list is empty
	members := func() []string {
		res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{ParentId: id, UserId: adminID, SortBy: []jf.ItemSortBy{jf.ItemSortByName}})).Model
		ids := make([]string, 0, len(res.Items))
		for _, it := range res.Items {
			ids = append(ids, it.Id)
		}
		return ids
	}
	// each step waits for what it asked for: the server answers a collection
	// change before it has applied it, and a read straight after can still
	// see the membership from before
	holds := func(within time.Duration, want ...string) ([]string, bool) {
		var got []string
		ok := poll(within, func() bool {
			got = members()

			return slices.Equal(got, want)
		})
		return got, ok
	}
	// the create's own members land with nothing sent again
	if got, ok := holds(editPatience, a.Id, b.Id); !ok {
		t.Fatalf("after CreateCollection members = %v, want %s and %s", got, alien, aliens)
	}
	// Jellyfin loses an add or a removal made while it is still refreshing
	// the collection after the change before (lib/embyfin re-sends for this),
	// so one that has not landed in a third of the patience is sent once more:
	// the same call, so one that never works still fails
	change := func(step string, send func() error, want ...string) {
		t.Helper()
		if err := send(); err != nil {
			t.Fatal(err)
		}
		if _, ok := holds(editPatience/3, want...); ok {
			return
		}
		t.Logf("%s did not land; sending it again", step)
		if err := send(); err != nil {
			t.Fatal(err)
		}
		if got, ok := holds(editPatience, want...); !ok {
			t.Errorf("after %s members = %v, want %v", step, got, want)
		}
	}
	change("AddToCollection", func() error {
		_, err := jfc.AddToCollection(ctx, id, jf.AddToCollectionOperationOptions{Ids: []string{c.Id}})
		return err
	}, a.Id, b.Id, c.Id)
	change("RemoveFromCollection", func() error {
		_, err := jfc.RemoveFromCollection(ctx, id, jf.RemoveFromCollectionOperationOptions{Ids: []string{a.Id}})
		return err
	}, b.Id, c.Id)
	// removing an item the collection does not hold is answered 204 and
	// changes nothing, which is why lib/embyfin checks membership first
	if _, err := jfc.RemoveFromCollection(ctx, id, jf.RemoveFromCollectionOperationOptions{Ids: []string{a.Id}}); err != nil {
		t.Errorf("removing a non-member = %v", err)
	}
	if got, ok := holds(editPatience, b.Id, c.Id); !ok {
		t.Errorf("after removing a non-member members = %v", got)
	}
	var item *jf.BaseItemDto
	if !poll(editPatience, func() bool {
		item = must(jfc.GetItem(ctx, id, jf.GetItemOperationOptions{UserId: adminID})).Model

		return item != nil && item.Type == jf.BaseItemKindBoxSet && item.Name == name && item.ChildCount == 2
	}) {
		t.Errorf("the collection as an item = %+v", item)
	}
}

// jfScanIdle waits for the library scan to be idle, so a change about to be
// made is not written over by a scan's refresh.
func jfScanIdle(ctx context.Context, t *testing.T) {
	t.Helper()

	if !poll(scanPatience, func() bool { return jfScanTask(ctx, t).State == jf.TaskStateIdle }) {
		t.Fatal("the library scan never went idle")
	}
}

// jfDeleteCollection deletes a collection and makes sure it stays deleted.
// A refresh still queued on the collection when the delete lands saves it
// again (its collection.xml), and the next library scan brings it back, its
// members hidden from /Items listings behind it; so a scan is run after the
// delete, and a collection it brings back is deleted again.
func jfDeleteCollection(ctx context.Context, t *testing.T, id string) {
	t.Helper()

	for range 3 {
		if _, err := jfc.DeleteItem(ctx, id); err != nil && !client.IsNotFound(err) {
			t.Errorf("deleting collection %s: %v", id, err)
			return
		}
		since := jfScanEnded(ctx) //nolint:azproviderlint // read before the refresh queues the scan, not after
		if _, err := jfc.RefreshLibrary(ctx); err != nil {
			t.Errorf("scanning after deleting collection %s: %v", id, err)
			return
		}
		jfWaitForScan(ctx, t, since)
		if back := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{Ids: []string{id}})).Model; len(back.Items) == 0 {
			return
		}
		t.Logf("collection %s came back after a scan; deleting it again", id)
	}
	t.Errorf("collection %s came back after each of three deletes", id)
}

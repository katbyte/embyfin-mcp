//go:build integration

package integration

import (
	"context"
	"slices"
	"testing"

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

//nolint:paralleltest // the tests share one server and its libraries
func TestJFCollections(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	a, b, c := jfMovie(t, alien), jfMovie(t, aliens), jfMovie(t, bladeRunner)

	created := must(jfc.CreateCollection(ctx, jf.CreateCollectionOperationOptions{Name: "SDK Collection", Ids: []string{a.Id, b.Id}})).Model
	if created.Id == "" {
		t.Fatal("CreateCollection returned no id")
	}
	id := created.Id
	t.Cleanup(func() { _, _ = jfc.DeleteItem(context.WithoutCancel(ctx), id) })

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
	holds := func(step string, want ...string) {
		t.Helper()

		var got []string
		if !poll(editPatience, func() bool {
			got = members()

			return slices.Equal(got, want)
		}) {
			t.Errorf("%s members = %v, want %v", step, got, want)
		}
	}
	// a collection's initial members and an add are written the same way,
	// and both are lost when a library scan's refresh of the collection
	// writes over them (lib/embyfin re-sends for this; the behaviour is in
	// api-defs/README.md), so one that has not landed in a third of the
	// patience is sent once more before it is a failure
	ensure := func(step string, want ...string) {
		t.Helper()
		if !poll(editPatience/3, func() bool { return slices.Equal(members(), want) }) {
			t.Logf("%s: the members did not land; adding them again", step)
			if _, err := jfc.AddToCollection(ctx, id, jf.AddToCollectionOperationOptions{Ids: want}); err != nil {
				t.Fatal(err)
			}
		}
		holds(step, want...)
	}
	ensure("after CreateCollection", a.Id, b.Id)
	if _, err := jfc.AddToCollection(ctx, id, jf.AddToCollectionOperationOptions{Ids: []string{c.Id}}); err != nil {
		t.Fatal(err)
	}
	ensure("after AddToCollection", a.Id, b.Id, c.Id)
	// Jellyfin loses a collection edit made while it is still refreshing the
	// collection after the last one (lib/embyfin re-sends for this; the
	// behaviour is in api-defs/README.md), so one that has not landed in a third
	// of the patience is sent once more before it is a failure
	if _, err := jfc.RemoveFromCollection(ctx, id, jf.RemoveFromCollectionOperationOptions{Ids: []string{a.Id}}); err != nil {
		t.Fatal(err)
	}
	if !poll(editPatience/3, func() bool { return slices.Equal(members(), []string{b.Id, c.Id}) }) {
		t.Logf("the removal of %s did not land; sending it again", a.Id)
		if _, err := jfc.RemoveFromCollection(ctx, id, jf.RemoveFromCollectionOperationOptions{Ids: []string{a.Id}}); err != nil {
			t.Fatal(err)
		}
	}
	holds("after RemoveFromCollection", b.Id, c.Id)
	// removing an item the collection does not hold is answered 204 and
	// changes nothing, which is why lib/embyfin checks membership first
	if _, err := jfc.RemoveFromCollection(ctx, id, jf.RemoveFromCollectionOperationOptions{Ids: []string{a.Id}}); err != nil {
		t.Errorf("removing a non-member = %v", err)
	}
	holds("after removing a non-member", b.Id, c.Id)
	var item *jf.BaseItemDto
	if !poll(editPatience, func() bool {
		item = must(jfc.GetItem(ctx, id, jf.GetItemOperationOptions{UserId: adminID})).Model

		return item != nil && item.Type == jf.BaseItemKindBoxSet && item.Name == "SDK Collection" && item.ChildCount == 2
	}) {
		t.Errorf("the collection as an item = %+v", item)
	}
}

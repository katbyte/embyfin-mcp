//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/jf"
)

// TestJFDeleteItem removes a movie the test laid out itself, in a library of
// its own, so nothing the rest of the suite asserts on can disappear.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFDeleteItem(t *testing.T) {
	ctx := skipUnlessJellyfin(t)

	dir := scratchDir(t)
	t.Cleanup(func() {
		_, _ = jfc.RemoveVirtualFolder(context.WithoutCancel(ctx), jf.RemoveVirtualFolderOperationOptions{Name: sdkScratch.Name, RefreshLibrary: new(false)})
		_ = os.RemoveAll(dir)
	})
	scratchID := jfLibrary(t, sdkScratch)

	res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{ParentId: scratchID, Recursive: new(true), IncludeItemTypes: []jf.BaseItemKind{jf.BaseItemKindMovie}, Fields: []jf.ItemFields{jf.ItemFieldsPath}})).Model
	// the server keeps the folder's year in the name of an unmatched movie
	if len(res.Items) != 1 || !strings.HasPrefix(res.Items[0].Name, scratchTitle) || res.Items[0].ProductionYear != 1995 {
		t.Fatalf("the scratch library holds %+v, want %s", res.Items, scratchTitle)
	}
	id, path := res.Items[0].Id, res.Items[0].Path

	if _, err := jfc.DeleteItem(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := jfc.GetItem(ctx, id, jf.GetItemOperationOptions{UserId: adminID}); !client.IsNotFound(err) {
		t.Errorf("GetItem after DeleteItem = %v, want a 404", err)
	}
	// the server deleted the file, not only the record
	if _, err := os.Stat(dir + "/" + scratchTitle + " (1995)/" + scratchTitle + " (1995).mp4"); !os.IsNotExist(err) {
		t.Errorf("after DeleteItem %s is still on disk (%v)", path, err)
	}
	if n := jfCount(ctx, scratchID, jf.BaseItemKindMovie); n != 0 {
		t.Errorf("the scratch library still counts %d movies", n)
	}
}

// TestJFDeleteItems removes several items in one call (DELETE /Items with
// ids, the route item_orphans_delete takes), from a library of its own: two
// films at once, then a film beside an id the server does not know, either
// way round, which Jellyfin does not treat alike.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFDeleteItems(t *testing.T) {
	ctx := skipUnlessJellyfin(t)

	dir := layOut(t, "sdk-bulk", bulkTitles...)
	t.Cleanup(func() {
		_, _ = jfc.RemoveVirtualFolder(context.WithoutCancel(ctx), jf.RemoveVirtualFolderOperationOptions{Name: sdkBulk.Name, RefreshLibrary: new(false)})
		_ = os.RemoveAll(dir)
	})
	bulkID := jfLibrary(t, sdkBulk)

	// the films by the folder they were laid out in
	films := map[string]jf.BaseItemDto{}
	for _, it := range must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{ParentId: bulkID, Recursive: new(true), IncludeItemTypes: []jf.BaseItemKind{jf.BaseItemKindMovie}, Fields: []jf.ItemFields{jf.ItemFieldsPath}})).Model.Items {
		films[filepath.Base(filepath.Dir(it.Path))] = it
	}
	if len(films) != len(bulkTitles) {
		t.Fatalf("the bulk library holds %d films, want %d: %+v", len(films), len(bulkTitles), films)
	}
	// gone is whether a film's record and its file are both gone
	gone := func(title string) bool {
		_, err := jfc.GetItem(ctx, films[title].Id, jf.GetItemOperationOptions{UserId: adminID})
		_, statErr := os.Stat(filepath.Join(dir, title, title+".mp4"))
		switch {
		case client.IsNotFound(err) && os.IsNotExist(statErr):
			return true
		case err == nil && statErr == nil:
			return false
		}
		t.Fatalf("%s is half deleted: the read answers %v, the file %v", title, err, statErr)
		return false
	}
	del := func(ids ...string) error {
		_, err := jfc.DeleteItems(ctx, jf.DeleteItemsOperationOptions{Ids: ids})
		return err
	}
	// unknown is an id no item has
	const unknown = "00000000000000000000000000000001"

	// two at once, and nothing else
	a, b, c, d := bulkTitles[0], bulkTitles[1], bulkTitles[2], bulkTitles[3]
	if err := del(films[a].Id, films[b].Id); err != nil {
		t.Fatal(err)
	}
	if !gone(a) || !gone(b) || gone(c) || gone(d) {
		t.Fatalf("after deleting %s and %s together: %s gone %t, %s gone %t, %s gone %t, %s gone %t", a, b, a, gone(a), b, gone(b), c, gone(c), d, gone(d))
	}

	// an unknown id stops the batch where it stands, with a 404: what came
	// before it is deleted, what comes after it is not
	if err := del(films[c].Id, unknown); !client.IsNotFound(err) {
		t.Errorf("deleting %s and then an unknown id = %v, want a 404", c, err)
	}
	if !gone(c) {
		t.Errorf("%s, before the unknown id, is still there", c)
	}
	if err := del(unknown, films[d].Id); !client.IsNotFound(err) {
		t.Errorf("deleting an unknown id and then %s = %v, want a 404", d, err)
	}
	if gone(d) {
		t.Errorf("%s, after the unknown id, was deleted", d)
	}
	// an id that is not one at all is dropped, and a call with no id left
	// deletes nothing
	if err := del("not-an-id"); err != nil {
		t.Errorf("deleting a malformed id = %v", err)
	}
	if err := del(); err != nil {
		t.Errorf("deleting no id = %v", err)
	}
	if gone(d) {
		t.Errorf("%s went with a malformed id or none", d)
	}
	if err := del(films[d].Id); err != nil || !gone(d) {
		t.Errorf("deleting %s alone = %v, gone %t", d, err, gone(d))
	}
	if n := jfCount(ctx, bulkID, jf.BaseItemKindMovie); n != 0 {
		t.Errorf("the bulk library still counts %d films", n)
	}
}

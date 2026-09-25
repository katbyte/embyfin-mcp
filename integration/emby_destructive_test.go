//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/emby"
)

// TestEmbyDeleteItem removes a movie the test laid out itself, in a library
// of its own, so nothing the rest of the suite asserts on can disappear.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyDeleteItem(t *testing.T) {
	ctx := skipUnlessEmby(t)

	dir := scratchDir(t)
	t.Cleanup(func() {
		_ = embyDeleteVirtualFolder(context.WithoutCancel(ctx), sdkScratch.Name)
		_ = os.RemoveAll(dir)
	})
	scratchID := embyLibrary(t, sdkScratch)

	res := must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{ParentId: scratchID, Recursive: new(true), IncludeItemTypes: "Movie", Fields: "Path,ProductionYear"})).Model
	if len(res.Items) != 1 || !strings.HasPrefix(res.Items[0].Name, scratchTitle) || res.Items[0].ProductionYear != 1995 {
		t.Fatalf("the scratch library holds %+v, want %s", res.Items, scratchTitle)
	}
	id, path := res.Items[0].Id, res.Items[0].Path

	if _, err := embyc.DeleteItemsById(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := embyc.GetUsersByUserIdItemsById(ctx, adminID, id); !client.IsNotFound(err) {
		t.Errorf("fetching after the delete = %v, want a 404", err)
	}
	// the server deleted the file, not only the record
	if _, err := os.Stat(dir + "/" + scratchTitle + " (1995)/" + scratchTitle + " (1995).mp4"); !os.IsNotExist(err) {
		t.Errorf("after the delete %s is still on disk (%v)", path, err)
	}
	if n := embyCount(ctx, scratchID, "Movie"); n != 0 {
		t.Errorf("the scratch library still counts %d movies", n)
	}
}

// TestEmbyDeleteItems removes several items in one call (DELETE /Items with
// Ids, the route item_orphans_delete takes), from a library of its own: two
// films at once, then a film beside an id the server does not know, either
// way round.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyDeleteItems(t *testing.T) {
	ctx := skipUnlessEmby(t)

	dir := layOut(t, "sdk-bulk", bulkTitles...)
	t.Cleanup(func() {
		_ = embyDeleteVirtualFolder(context.WithoutCancel(ctx), sdkBulk.Name)
		_ = os.RemoveAll(dir)
	})
	bulkID := embyLibrary(t, sdkBulk)

	// the films by the folder they were laid out in
	films := map[string]emby.BaseItemDto{}
	for _, it := range must(embyc.GetItems(ctx, emby.GetItemsOperationOptions{ParentId: bulkID, Recursive: new(true), IncludeItemTypes: "Movie", Fields: "Path"})).Model.Items {
		films[filepath.Base(filepath.Dir(it.Path))] = it
	}
	if len(films) != len(bulkTitles) {
		t.Fatalf("the bulk library holds %d films, want %d: %+v", len(films), len(bulkTitles), films)
	}
	// gone is whether a film's record and its file are both gone
	gone := func(title string) bool {
		_, err := embyc.GetUsersByUserIdItemsById(ctx, adminID, films[title].Id)
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
	// unknown is an id no item has
	const unknown = "999999999"

	// two at once, and nothing else
	a, b, c, d := bulkTitles[0], bulkTitles[1], bulkTitles[2], bulkTitles[3]
	if _, err := embyc.DeleteItems(ctx, emby.DeleteItemsOperationOptions{Ids: films[a].Id + "," + films[b].Id}); err != nil {
		t.Fatal(err)
	}
	if !gone(a) || !gone(b) || gone(c) || gone(d) {
		t.Fatalf("after deleting %s and %s together: %s gone %t, %s gone %t, %s gone %t, %s gone %t", a, b, a, gone(a), b, gone(b), c, gone(c), d, gone(d))
	}

	// an unknown id beside a known one is passed over, before it or after
	// it: answered 204, and the known one deleted
	if _, err := embyc.DeleteItems(ctx, emby.DeleteItemsOperationOptions{Ids: films[c].Id + "," + unknown}); err != nil {
		t.Errorf("deleting %s and an unknown id = %v", c, err)
	}
	if !gone(c) || gone(d) {
		t.Errorf("after deleting %s beside an unknown id: %s gone %t, %s gone %t", c, c, gone(c), d, gone(d))
	}
	if _, err := embyc.DeleteItems(ctx, emby.DeleteItemsOperationOptions{Ids: unknown + "," + films[d].Id}); err != nil {
		t.Errorf("deleting an unknown id and %s = %v", d, err)
	}
	if !gone(d) {
		t.Errorf("after deleting an unknown id before %s it is still there", d)
	}
	if n := embyCount(ctx, bulkID, "Movie"); n != 0 {
		t.Errorf("the bulk library still counts %d films", n)
	}

	// an unknown id alone is answered the same, and one that is not a
	// number at all is a 500
	if _, err := embyc.DeleteItems(ctx, emby.DeleteItemsOperationOptions{Ids: unknown}); err != nil {
		t.Errorf("deleting only an unknown id = %v", err)
	}
	if _, err := embyc.DeleteItems(ctx, emby.DeleteItemsOperationOptions{Ids: "not-an-id"}); client.StatusCode(err) != 500 {
		t.Errorf("deleting a malformed id = %v, want a 500", err)
	}
}

//go:build integration

package integration

import (
	"context"
	"os"
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

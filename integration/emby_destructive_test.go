//go:build integration

package integration

import (
	"context"
	"os"
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

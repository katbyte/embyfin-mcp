//go:build integration

package integration

import (
	"slices"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/jf"
	"github.com/katbyte/go-kt/pointer"
)

//nolint:paralleltest // the tests share one server and its libraries
func TestJFUsers(t *testing.T) {
	ctx := skipUnlessJellyfin(t)

	users := must(jfc.GetUsers(ctx, jf.GetUsersOperationOptions{})).Model
	if len(users) != 2 {
		t.Fatalf("GetUsers = %d, want root and alice", len(users))
	}
	for _, u := range users {
		if u.Id == "" || u.Name == "" || u.ServerId == "" || u.Policy == nil || u.Configuration == nil {
			t.Errorf("user did not decode: %+v", u)
		}
	}
	root := users[slices.IndexFunc(users, func(u jf.UserDto) bool { return u.Id == adminID })]
	if root.Name != "root" || !pointer.From(root.Policy.IsAdministrator) {
		t.Errorf("root = %+v", root)
	}

	alice := must(jfc.GetUserById(ctx, aliceID)).Model
	if alice.Name != "alice" || alice.Policy == nil || pointer.From(alice.Policy.IsAdministrator) {
		t.Errorf("GetUserById(alice) = %+v", alice)
	}
	// new users are hidden from the login screen by default, so the public
	// list is empty; it has to decode all the same
	for _, u := range must(jfc.GetPublicUsers(ctx)).Model {
		if u.Id == "" || u.Name == "" {
			t.Errorf("public user = %+v", u)
		}
	}
	// an API key has no user behind it
	if _, err := jfc.GetCurrentUser(ctx); client.StatusCode(err) == 0 {
		t.Errorf("GetCurrentUser with an API key = %v, want an HTTP error", err)
	}

	auth := must(jfc.AuthenticateUserByName(ctx, jf.AuthenticateUserByName{Username: "root", Pw: password})).Model
	if auth.AccessToken == "" || auth.ServerId == "" || auth.User == nil || auth.User.Id != adminID || auth.SessionInfo == nil || auth.SessionInfo.Id == "" {
		t.Errorf("AuthenticateUserByName = %+v", auth)
	}
	if _, err := jfc.AuthenticateUserByName(ctx, jf.AuthenticateUserByName{Username: "root", Pw: "wrong"}); client.StatusCode(err) != 401 {
		t.Errorf("a wrong password = %v, want a 401", err)
	}
}

//nolint:paralleltest // the tests share one server and its libraries
func TestJFAPIKeys(t *testing.T) {
	ctx := skipUnlessJellyfin(t)

	if _, err := jfc.CreateKey(ctx, jf.CreateKeyOperationOptions{App: sdkApp}); err != nil {
		t.Fatal(err)
	}
	keys := must(jfc.GetKeys(ctx)).Model
	i := slices.IndexFunc(keys.Items, func(k jf.AuthenticationInfo) bool { return k.AppName == sdkApp })
	if i < 0 {
		t.Fatalf("GetKeys does not list %s: %+v", sdkApp, keys.Items)
	}
	key := keys.Items[i]
	// (the server lists keys with Id 0 and IsActive false; the token and
	// dates are what it fills in)
	if key.AccessToken == "" || key.DateCreated == "" {
		t.Errorf("key = %+v", key)
	}
	// the testenv key is there too
	if !slices.ContainsFunc(keys.Items, func(k jf.AuthenticationInfo) bool { return k.AppName == "embyfin-mcp-test" }) {
		t.Errorf("GetKeys does not list the testenv key: %+v", keys.Items)
	}

	if _, err := jfc.RevokeKey(ctx, key.AccessToken); err != nil {
		t.Fatal(err)
	}
	if after := must(jfc.GetKeys(ctx)).Model; slices.ContainsFunc(after.Items, func(k jf.AuthenticationInfo) bool { return k.AccessToken == key.AccessToken }) {
		t.Error("the key is still listed after RevokeKey")
	}
}

//nolint:paralleltest // the tests share one server and its libraries
func TestJFPlayedAndResume(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	id := jfMovie(t, "Princess Mononoke").Id

	ud := must(jfc.MarkPlayedItem(ctx, id, jf.MarkPlayedItemOperationOptions{UserId: adminID})).Model
	if !pointer.From(ud.Played) || ud.PlayCount == 0 || ud.LastPlayedDate == "" {
		t.Errorf("MarkPlayedItem = %+v", ud)
	}
	if it := must(jfc.GetItem(ctx, id, jf.GetItemOperationOptions{UserId: adminID})).Model; it.UserData == nil || !pointer.From(it.UserData.Played) {
		t.Errorf("after MarkPlayedItem the item reads %+v", it.UserData)
	}
	if res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{UserId: adminID, ParentId: jfLibrary(t, sdkMovies), Recursive: new(true), IsPlayed: new(true)})).Model; len(res.Items) != 1 || res.Items[0].Id != id {
		t.Errorf("IsPlayed=true = %+v, want Princess Mononoke", res.Items)
	}
	// alice never watched it
	if it := must(jfc.GetItem(ctx, id, jf.GetItemOperationOptions{UserId: aliceID})).Model; it.UserData != nil && pointer.From(it.UserData.Played) {
		t.Error("marking played for root marked it for alice too")
	}
	if ud := must(jfc.MarkUnplayedItem(ctx, id, jf.MarkUnplayedItemOperationOptions{UserId: adminID})).Model; pointer.From(ud.Played) {
		t.Errorf("MarkUnplayedItem = %+v", ud)
	}

	// nothing is in progress yet, so the list is empty but must decode
	resume := func() *jf.BaseItemDtoQueryResult {
		return must(jfc.GetResumeItems(ctx, jf.GetResumeItemsOperationOptions{UserId: adminID, MediaTypes: []jf.MediaType{jf.MediaTypeVideo}, Limit: 5})).Model
	}
	if res := resume(); res.TotalRecordCount != 0 {
		t.Errorf("GetResumeItems = %+v", res)
	}

	// the user data update applies only what is set: a favourite survives a
	// position posted on its own
	if _, err := jfc.MarkFavoriteItem(ctx, id, jf.MarkFavoriteItemOperationOptions{UserId: adminID}); err != nil {
		t.Fatal(err)
	}
	ud = must(jfc.UpdateItemUserData(ctx, id, jf.UpdateUserItemDataDto{PlaybackPositionTicks: 600_000_000}, jf.UpdateItemUserDataOperationOptions{UserId: adminID})).Model
	if ud.PlaybackPositionTicks != 600_000_000 || !pointer.From(ud.IsFavorite) {
		t.Errorf("UpdateItemUserData = %+v, want the position and the favourite kept", ud)
	}
	// a position past a one-second file's end is kept, and puts it in progress
	if res := resume(); len(res.Items) != 1 || res.Items[0].Id != id {
		t.Errorf("GetResumeItems after setting a position = %+v", res.Items)
	}
	// marking unplayed clears the position
	if ud := must(jfc.MarkUnplayedItem(ctx, id, jf.MarkUnplayedItemOperationOptions{UserId: adminID})).Model; ud.PlaybackPositionTicks != 0 {
		t.Errorf("MarkUnplayedItem left the position: %+v", ud)
	}
	if _, err := jfc.UnmarkFavoriteItem(ctx, id, jf.UnmarkFavoriteItemOperationOptions{UserId: adminID}); err != nil {
		t.Fatal(err)
	}
	if res := resume(); len(res.Items) != 0 {
		t.Errorf("GetResumeItems after marking unplayed = %+v", res.Items)
	}
}

package embyfin

import (
	"strings"

	"github.com/katbyte/go-kt/lock"
)

// lockItem waits until no other change from this process is being made to
// the item, takes it, and returns the unlock.
//
// The tools change an item by reading it whole and posting it back (neither
// server has a conditional update), and an MCP client calls tools in
// parallel: two edits of one item at once each post back what they read, and
// the later post undoes the earlier one's change. Holding the item for the
// whole round keeps them apart within this process, for every client of the
// same server in it; another client of that server is not covered.
//
// An id is an item's within its server, so the server is part of what is
// locked: its address and the id, a space between them, which no address
// holds. Jellyfin reads a GUID with or without its dashes and in either
// case, so the id is locked in one spelling: two edits that name one item
// differently still wait for each other. (Emby's ids are numbers, which the
// spelling leaves as they are.)
func (c *Client) lockItem(id string) (unlock func()) {
	return lock.ByString(c.baseURL + " " + strings.ToLower(strings.ReplaceAll(id, "-", "")))
}

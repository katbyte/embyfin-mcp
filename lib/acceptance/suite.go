// Package acceptance is what a suite that runs against a live media server
// adds to go-kt's (mcp/acctest): the waits on a scan, which is asked for
// until it has taken, and a put-back for an item the test may since have
// deleted. They are here and not there because they call this application's
// own tools.
//
// The suite under ../../acceptance is what it was written for; nothing here
// knows its fixtures.
package acceptance

import (
	"strings"
	"testing"

	"github.com/katbyte/go-kt/mcp/acctest"
)

// Suite is go-kt's suite - one client session to the server under test, and
// the record of what it was asked - with what a media server needs beside
// it.
type Suite struct {
	*acctest.Suite
}

// New is a suite that skips every call, saying notReady, until its session
// is connected.
func New(notReady string) *Suite {
	return &Suite{Suite: &acctest.Suite{NotReady: notReady}}
}

// UndoIfThere is Undo for a change to an item the test may since have
// deleted, and its state with it: an item gone (item_get answering that
// there is no item with the id) needs nothing put back, and a read that
// can't say whether it is gone is reported.
func (s *Suite) UndoIfThere(t *testing.T, id, tool string, args map[string]any) {
	t.Helper()

	if _, err := s.Invoke("item_get", map[string]any{"id": id}); err != nil {
		if !strings.Contains(err.Error(), "no item with id") {
			t.Errorf("reading whether %s is still there: %v", id, err)
		}

		return
	}
	s.Undo(t, tool, args)
}

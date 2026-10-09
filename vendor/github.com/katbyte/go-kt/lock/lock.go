// Package lock makes the callers in one process take turns at a thing, so
// one does not undo another's change to it.
//
// A tool that edits a record by reading it whole and sending it back - no
// server it talks to has a conditional update - loses a change when two such
// edits run at once: each sends back what it read, and the later one undoes
// the earlier. An MCP client runs the calls of one turn at once, so eight
// calls each adding a tag to one book left one tag. Locking the record for
// the whole read and write keeps them apart:
//
//	unlock := lock.ByName(id, "playlist")
//	defer unlock()
//	// read the playlist, change it, send it back
//
// A thing is locked by its id (ByID), or by its name and what kind of thing
// it is (ByName) where two kinds can share a name; several are locked in one
// call (MultipleByID, MultipleByName), and All locks everything for a change
// that reads a whole collection and writes it back.
//
// The locks are one set, shared by every caller in the process. Another
// client of the same server, its own web app included, is not held back.
//
// A caller locks once at a time, naming everything it needs in that one
// call: locking a second time while holding the first can wait on an All
// that is itself waiting for the first to be unlocked.
package lock

// process is the one set of locks every caller in the process shares.
var process set

// ByID locks the thing with this id until unlock is called, waiting for
// whoever has it. The id is compared as it is written, so a caller whose
// server reads one id in several spellings folds them to one first.
func ByID(id string) (unlock func()) {
	return process.hold(id)
}

// ByName locks the thing of one kind with this name, where the name alone
// could be another kind of thing's too: a playlist and a collection can both
// be number 12.
func ByName(name, kind string) (unlock func()) {
	return process.hold(NameID(name, kind))
}

// MultipleByID locks every one of these ids in one call. They are taken in
// sorted order, each once however often it is named, so two callers locking
// overlapping sets cannot wait on each other for ever.
func MultipleByID(ids []string) (unlock func()) {
	return process.hold(ids...)
}

// MultipleByName locks every one of these names of one kind in one call, in
// the same safe order as MultipleByID.
func MultipleByName(names []string, kind string) (unlock func()) {
	ids := make([]string, 0, len(names))
	for _, name := range names {
		ids = append(ids, NameID(name, kind))
	}

	return process.hold(ids...)
}

// All locks everything there is or could be, for a change that reads a whole
// collection and writes it back: it waits for every other lock to be
// unlocked, and none is granted until it is.
func All() (unlock func()) {
	return process.holdAll()
}

// NameID is the id ByName locks, for locking things of several kinds in one
// call:
//
//	lock.MultipleByID([]string{lock.NameID(id, "item"), "bookmarks"})
func NameID(name, kind string) string {
	return kind + "." + name
}

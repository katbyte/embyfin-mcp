package tools

import (
	"strings"
	"testing"
	"time"
)

// A film sharing its folder takes, besides its own file, every nfo, subtitle
// and piece of artwork whose name begins with its file's name whatever the
// case - a neighbour's too, when the neighbour's name begins the same way -
// and never a media file, the neighbour's or its own trailer. That is what
// Emby 4.10 was seen to delete, by its own list of kinds (an .ass subtitle
// goes, an .idx does not), and the refusal names exactly that.
func TestItemDeleteNamesTheSidecarsTheServerTakes(t *testing.T) {
	t.Parallel()

	const dir = "/zz/films/Shared"
	items := map[string]map[string]any{
		"9": {
			"Id": "9", "Name": "Plugh", "Type": "Movie", "Path": dir + "/Plugh.mkv",
			"MediaSources": []map[string]any{{"Path": dir + "/Plugh.mkv"}},
		},
	}
	disk := &diskState{paths: map[string]bool{}, deletes: map[string][]string{}}
	taken := []string{"Plugh.mkv", "Plugh.nfo", "Plugh-poster.jpg", "Plugh II.nfo", "Plugh II.eng.srt", "Plugh II.ass", "plugh iii-fanart.JPG"}
	kept := []string{"Plugh II.mkv", "Plugh-trailer.mkv", "Plugh II.idx", "Plugh II.txt", "Xyzzy.nfo"}
	for _, p := range []string{"/zz/", "/zz/films/", dir + "/"} {
		disk.paths[p] = true
	}
	for _, name := range append(append([]string{}, taken...), kept...) {
		disk.paths[dir+"/"+name] = true
	}
	f := deleteServer(t, items, disk)
	r := &registry{client: f.client(t), settle: time.Millisecond, opts: Options{EnableDelete: true}}
	registerItemTools(r)
	cs := hostRegistry(t, r)

	msg := mustRefuse(t, cs, "item_delete", map[string]any{"id": "9"})
	if strings.Contains(msg, "the folder") {
		t.Fatalf("a film sharing its folder would take the folder: %s", msg)
	}
	for _, name := range taken {
		if !strings.Contains(msg, dir+"/"+name) {
			t.Errorf("the refusal does not name %s: %s", name, msg)
		}
	}
	for _, name := range kept {
		if strings.Contains(msg, dir+"/"+name+",") || strings.HasSuffix(msg, dir+"/"+name) {
			t.Errorf("the refusal names %s, which the server keeps: %s", name, msg)
		}
	}
	// and it says which of them are not the film's own: Plugh II's sidecars,
	// and a Plugh III fanart with no film beside it, go only because their
	// names begin with the film's
	_, others, found := strings.Cut(msg, "Of those, ")
	if !found {
		t.Fatalf("the refusal does not say which files are another item's: %s", msg)
	}
	others, _, _ = strings.Cut(others, " are not this item's own")
	for _, name := range []string{"Plugh II.nfo", "Plugh II.eng.srt", "Plugh II.ass", "plugh iii-fanart.JPG"} {
		if !strings.Contains(others, dir+"/"+name) {
			t.Errorf("%s is not named as another's: %s", name, others)
		}
	}
	for _, name := range []string{"Plugh.mkv", "Plugh.nfo", "Plugh-poster.jpg"} {
		if strings.Contains(others, dir+"/"+name) {
			t.Errorf("the film's own %s is named as another's: %s", name, others)
		}
	}
}

package tools

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/mediapath"
	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/katbyte/go-kt/whitespace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// audit_whitespace: spaces where a name should not have them. A double space,
// a space at either end or before the extension, a space before a colon and
// a tab or a non-breaking space all look like one ordinary space on screen,
// and none of them is found by a search for the name as it reads. The design
// is abs-mcp's audit_whitespace, so the two servers' audits read alike.
//
// A double space in a folder or file name is often not a typo but the mark
// of a character a renamer dropped: a colon ("Dune  Part Two" for "Dune:
// Part Two"), or a word a title spells in asterisks. So every row carries the
// item's own title beside the path, and what the title holds where the name
// has the gap when the two line up: the fix may be putting it back rather
// than closing the gap.

var whitespaceWhereOrder = []string{"name", "sort_name", "original_title", "genre", "tag", "studio", "person", "folder", "file"}

// whitespaceTypes are the items whose names and paths the audit reads: the
// films, the shows down to their episodes, the music down to its tracks,
// music videos, home videos and books. Photos are not read.
const whitespaceTypes = "Movie,Series,Season,Episode,MusicArtist,MusicAlbum,Audio,MusicVideo,Video,Book,AudioBook"

// renamedOnDisk is the fix for a folder or a file: no tool here renames one.
const renamedOnDisk = "rename it on disk and scan the library; no tool here renames files or folders. A double space often marks a character a renamer dropped, a colon or a censored word: check the name against the title (and dropped) before closing the gap"

var whitespaceFixes = map[string]string{
	"name":           "item_edit name= the suggested value, on the item's id",
	"sort_name":      "item_edit sort_name= the suggested value, on the item's id",
	"original_title": "no tool here edits an original title: it comes from the provider or the item's nfo, so correct it there and refresh the item",
	"genre":          "metadata_rename field=genres from= the row's value (exactly as held, spaces and all) to= its suggest; renaming onto one an item already carries merges the two",
	"tag":            "metadata_rename field=tags from= the row's value (exactly as held, spaces and all) to= its suggest; renaming onto one an item already carries merges the two",
	"studio":         "metadata_rename field=studios from= the row's value (exactly as held, spaces and all) to= its suggest; renaming onto one an item already carries merges the two",
	"folder":         renamedOnDisk,
	"file":           renamedOnDisk,
}

// personFix is how to put a person's name right, which the two servers take
// differently. Emby renames the person and every item keeps them (seen on
// 4.10). Jellyfin 12.1 credits a person by name: a renamed one drops off
// every item crediting them until the old name is put back, which is worse
// than the space. There the name is put right at its source, the nfo, and
// the item refreshed - which took the new name on a test server when TMDB
// could be asked about the person, and kept the old credit when it could
// not, so the fix says to check.
func personFix(backend embyfin.Backend) string {
	if backend == embyfin.Emby {
		return "item_edit name= the suggested value, on the person's id (the row's id): every item crediting them keeps them under the new name"
	}

	return "not item_edit: renaming a person on Jellyfin takes them off every item that credits them. Put the name right where it came from - the nfo beside an item crediting them - and item_refresh that item, then check the credit: a refresh does not always replace the name an item already credits"
}

// sortNameSet is an item's sort name as set by hand or by an nfo, "" when
// the server made it from the name, and whether it was set. Jellyfin's
// listing gives its own reading of a sort name - lowercased, its articles
// and dashes dropped, two spaces left where an ampersand stood ("asterix
// obelix: big fight") - and the one set as ForcedSortName, empty when none
// was. Emby's gives the sort name as it stands, set or made from the name,
// and lists SortName among the locked fields of one set by an edit.
func sortNameSet(it *embyfin.Item, backend embyfin.Backend) (sort string, set bool) {
	settings := cmp.Or(it.Settings, &embyfin.ItemSettings{})
	if backend == embyfin.Emby {
		return it.SortName, slices.Contains(settings.LockedFields, "SortName")
	}

	return settings.ForcedSortName, settings.ForcedSortName != ""
}

// mediaFile is a path's last segment when it names a file of the kinds the
// servers hold as items. A film kept as a disc is held at its folder, so a
// last segment that is none of these is read as a folder.
var mediaFile = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|m4v|ts|m2ts|mts|vob|ssif|mov|wmv|mpg|mpeg|m2v|flv|webm|ogm|divx|iso|strm|3gp|mp3|flac|m4a|m4b|aac|ogg|oga|opus|wav|wma|ape|alac|aiff|aif|dsf|dff|mka|wv|mpc|epub|pdf|mobi|azw3?|cbz|cbr|cb7|djvu|fb2)$`)

// folderItems are the types a server holds at a folder rather than a file.
var folderItems = []string{"Series", "Season", "MusicAlbum", "MusicArtist", "BoxSet", "Folder"}

type whitespaceRow struct {
	Where   string `json:"where"             jsonschema:"name, sort_name, original_title, genre, tag, studio, person, folder or file"`
	Problem string `json:"problem"           jsonschema:"odd_space: a tab, line break or non-breaking or typographic space, the ideographic space aside; double_space: two or more spaces in a row; edge_space: a space at the start or the end; space_before_extension: a file name's stem ends in a space ('Film (2001) .mkv'); space_before_colon: a space before a colon or the look-alike ꞉ ('Title ꞉ Subtitle')"`
	Text    string `json:"text"              jsonschema:"the name or value with the offending spaces made visible: ␣ for each one, [U+00A0] for a space that is not the ordinary one"`
	Value   string `json:"value"             jsonschema:"the name or value exactly as the server holds it, spaces and all (for a folder or file row, its name on disk): what metadata_rename takes as from"`
	Suggest string `json:"suggest"           jsonschema:"the text with its spaces put right. A double space in a folder or file name may mark a dropped character: check it against title and dropped first"`
	Dropped string `json:"dropped,omitempty" jsonschema:"folder and file rows with a double space: what the item's title holds where the name has the gap, when the words either side line up (':' for 'Dune  Part Two' against 'Dune: Part Two'): the character a renamer dropped, which may be the fix rather than one space"`
	Items   int    `json:"items,omitempty"   jsonschema:"genre, tag, studio, person and folder rows: how many items carry the value or sit in the folder, reported once; id, title and path are the first met (for a person, the person's id, and the title and path of an item crediting them)"`
	ID      string `json:"id"                jsonschema:"the item the row is about; for a person row the person, whose id item_edit takes on Emby"`
	Type    string `json:"type,omitempty"    jsonschema:"the item's type"`
	Title   string `json:"title"             jsonschema:"the item's own title (an episode's with its series and number), to set a folder or file name beside"`
	Path    string `json:"path,omitempty"    jsonschema:"the item's path; for a folder row the folder"`
	Fix     string `json:"fix"`
}

type whitespaceCounts struct {
	OddSpace             int `json:"odd_space"`
	DoubleSpace          int `json:"double_space"`
	EdgeSpace            int `json:"edge_space"`
	SpaceBeforeExtension int `json:"space_before_extension"`
	SpaceBeforeColon     int `json:"space_before_colon"`
}

type whitespaceWhere struct {
	Name          int `json:"name"`
	SortName      int `json:"sort_name"`
	OriginalTitle int `json:"original_title"`
	Genre         int `json:"genre"`
	Tag           int `json:"tag"`
	Studio        int `json:"studio"`
	Person        int `json:"person"`
	Folder        int `json:"folder"`
	File          int `json:"file"`
}

type whitespaceIn struct {
	Library string `json:"library,omitempty" jsonschema:"one library by name or id; default every library"`
	Where   string `json:"where,omitempty"   jsonschema:"comma-separated: name, sort_name, original_title, genre, tag, studio, person, folder, file; default all of them. file alone lists the file names, which a library of episodes has most of"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum rows, default 100"`
}

type whitespaceOut struct {
	Scanned  int              `json:"items_scanned"`
	People   int              `json:"people_scanned" jsonschema:"people read from the server's list of them, which is the whole server's"`
	Found    int              `json:"total_findings" jsonschema:"rows before limit: a value or folder shared by many items is one row, and a name or file one row per problem"`
	Counts   whitespaceCounts `json:"counts"         jsonschema:"rows by problem"`
	ByWhere  whitespaceWhere  `json:"by_where"       jsonschema:"rows by where the text is"`
	Findings []whitespaceRow  `json:"findings"       jsonschema:"by where (name, sort_name, original_title, genre, tag, studio, person, folder, file), then by problem; capped at limit"`
	Note     string           `json:"note,omitempty" jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	files    int              // how many files the file rows are of, which audit_all says
	// changed is what the reads said of the library changing: what
	// audit_all reports
	changed string
}

// whitespaceSweep gathers the rows from a library's items.
type whitespaceSweep struct {
	scanned   int
	rows      []whitespaceRow
	shared    map[string]int // a value or folder already reported -> its row
	want      map[string]bool
	libraries []embyfin.LibraryPath // the libraries' own folders, whose names are no item's
	person    string                // the fix for a person's name, on this server
	versioned []string              // items held in several files, whose other files are read after
	// how well the item named on a folder row stands for the folder: 2 held
	// at the folder itself, 1 its file in it, 0 anything else under it
	named   map[int]int
	backend embyfin.Backend
	people  int
	reads   []embyfin.ReadResult // what each read saw of the library changing
}

func newWhitespaceSweep(want map[string]bool, libraries []embyfin.LibraryPath, backend embyfin.Backend) *whitespaceSweep {
	return &whitespaceSweep{
		shared: map[string]int{}, want: want, libraries: libraries, person: personFix(backend), named: map[int]int{}, backend: backend,
	}
}

// check reports each problem with one text on the row for item it at path,
// but those in except. A shared text, key set, is reported once however many
// items carry it.
func (w *whitespaceSweep) check(it *embyfin.Item, where, text, key string, k whitespace.Text, at string, except ...string) {
	if !w.want[where] {
		return
	}
	for _, problem := range whitespace.Problems(text, k) {
		if slices.Contains(except, problem) {
			continue
		}
		if key != "" {
			seen := where + "|" + problem + "|" + key
			if i, ok := w.shared[seen]; ok {
				w.rows[i].Items++

				continue
			}
			w.shared[seen] = len(w.rows)
		}
		row := whitespaceRow{
			Where: where, Problem: problem, Text: whitespace.Visible(text, k), Value: text, Suggest: whitespace.Fixed(text, k),
			ID: it.ID, Type: it.Type, Title: episodeOrItemName(it), Path: at, Fix: whitespaceFixes[where],
		}
		if key != "" {
			row.Items = 1
		}
		if where == "file" && problem == "double_space" {
			row.Dropped = whitespace.DroppedAt(strings.TrimSuffix(text, path.Ext(text)), it.Name)
		}
		w.rows = append(w.rows, row)
	}
}

// add reads one item's names and its path.
func (w *whitespaceSweep) add(it *embyfin.Item) {
	w.scanned++
	w.check(it, "name", it.Name, "", whitespace.Name, it.Path)
	// a sort name set by hand is judged whole: it no longer follows the
	// name, so putting the name right leaves it as it is. One Emby made from
	// the name keeps the name's spaces, which the name's row says, and is
	// judged for any the name has not; one Jellyfin made is its own reading
	// of the name, with two spaces where it drops a dash, and not judged
	switch sort, set := sortNameSet(it, w.backend); {
	case set:
		w.check(it, "sort_name", sort, "", whitespace.Name, it.Path)
	case sort != "" && w.backend == embyfin.Emby:
		w.check(it, "sort_name", sort, "", whitespace.Name, it.Path, whitespace.Problems(it.Name, whitespace.Name)...)
	}
	// an original title the same as the name says nothing the name's row
	// does not; one that differs is written in its own language
	if it.OriginalTitle != "" && it.OriginalTitle != it.Name {
		w.check(it, "original_title", it.OriginalTitle, "", whitespace.Foreign, it.Path)
	}
	w.path(it, it.Path, true)
	if it.MediaSourceCount > 1 {
		w.versioned = append(w.versioned, it.ID)
	}
}

// vocabulary reads the genres, tags and studios an item carries, each value
// reported once with how many items carry it.
func (w *whitespaceSweep) vocabulary(it *embyfin.Item) {
	for where, field := range map[string]string{"genre": fieldGenres, "tag": fieldTags, "studio": fieldStudios} {
		for _, v := range valuesOf(field, it) {
			w.check(it, where, v, v, whitespace.Name, it.Path)
		}
	}
}

// peopleOf reads the server's list of people, one row a person, and reports
// each name with a space out of place once, with how many items of the
// library credit them - asked of the server for those names alone, and a
// name no item of the library credits left out. Read off every item's full
// cast instead, a page of ten thousand films was tens of megabytes.
func (w *whitespaceSweep) peopleOf(ctx context.Context, client *embyfin.Client, parent string) error {
	var named []embyfin.Item
	read, err := sweepAll(ctx, client, embyfin.SearchOptions{IncludeItemTypes: "Person", Fields: "SortName"}, embyfin.ToAnswer, func(items []embyfin.Item) {
		for i := range items {
			if len(whitespace.Problems(items[i].Name, whitespace.Name)) > 0 {
				named = append(named, items[i])
			}
		}
	})
	w.people = read.Read
	if err != nil {
		return err
	}
	w.reads = append(w.reads, read)
	for i := range named {
		p := &named[i]
		credits, total, err := client.Search(ctx, embyfin.SearchOptions{PersonIDs: p.ID, IncludeItemTypes: whitespaceTypes, ParentID: parent, Fields: "Path", Limit: 1})
		if err != nil {
			return err
		}
		if total == 0 || len(credits) == 0 {
			continue
		}
		before := len(w.rows)
		w.check(new(embyfin.Item{ID: p.ID, Name: p.Name, Type: "Person"}), "person", p.Name, p.ID, whitespace.Name, "")
		// the person is who the fix is for; the item is one crediting them
		for j := before; j < len(w.rows); j++ {
			w.rows[j].Items, w.rows[j].Title, w.rows[j].Path, w.rows[j].Fix = total, episodeOrItemName(&credits[0]), credits[0].Path, w.person
		}
	}

	return nil
}

// path reads the folders of one of an item's paths, below the library's own
// folder, and the file name when the path ends in one. own is false for the
// other files of an item held in several (Jellyfin's versions), so the item
// is counted once in a folder they share.
func (w *whitespaceSweep) path(it *embyfin.Item, p string, own bool) {
	if p == "" || !mediapath.OnDisk(p) {
		return
	}
	// one separator, for a server on Windows; of the same length, so the
	// server's own spelling of a folder is cut from the same place
	slashed := strings.ReplaceAll(p, `\`, "/")
	start := 0
	if lib, ok := inLibrary(p, w.libraries); ok {
		// a library at a filesystem's top ("/") ends in its separator
		start = len(strings.TrimRight(mediapath.Trim(lib.Path), `/\`))
	}
	file := !slices.Contains(folderItems, it.Type) && mediaFile.MatchString(slashed)
	end := len(slashed)
	if file {
		end = strings.LastIndex(slashed, "/")
	}
	for at := start; at < end; {
		stop := end
		if next := strings.IndexByte(slashed[at+1:], '/'); next >= 0 {
			stop = min(at+1+next, end)
		}
		if segment := slashed[at+1 : stop]; segment != "" {
			w.folder(it, segment, p[:stop], own)
		}
		at = stop
	}
	if file {
		w.check(it, "file", slashed[end+1:], "", whitespace.File, p)
	}
}

// folder reports a folder's problems once, with how many items sit in it,
// beside the item the folder was named for when one comes by: the series or
// the season held at the folder itself, or the film whose file is in it.
func (w *whitespaceSweep) folder(it *embyfin.Item, name, full string, own bool) {
	if !w.want["folder"] {
		return
	}
	rank := 0
	switch {
	case mediapath.Trim(it.Path) == mediapath.Trim(full):
		rank = 2
	case mediapath.Dir(it.Path) == full:
		rank = 1
	}
	for _, problem := range whitespace.Problems(name, whitespace.Name) {
		key := "folder|" + problem + "|" + full
		at, seen := w.shared[key]
		if !seen {
			at = len(w.rows)
			w.shared[key] = at
			w.rows = append(w.rows, whitespaceRow{
				Where: "folder", Problem: problem, Text: whitespace.Visible(name, whitespace.Name), Value: name, Suggest: whitespace.Fixed(name, whitespace.Name),
				Path: full, Fix: renamedOnDisk,
			})
			w.named[at] = -1
		}
		// an item's other file counts it in a folder only when it is the
		// first met there: most sit beside the item's own file, which
		// counted it already
		if own || !seen {
			w.rows[at].Items++
		}
		if rank > w.named[at] {
			w.named[at] = rank
			row := &w.rows[at]
			row.ID, row.Type, row.Title = it.ID, it.Type, episodeOrItemName(it)
			if problem == "double_space" {
				row.Dropped = whitespace.DroppedAt(name, it.Name)
			}
		}
	}
}

// versions reads the other files of the items held in several, which a
// listing names only the first of: Jellyfin's versions of one film. Emby
// holds each version as an item of its own, which the sweep read already.
func (w *whitespaceSweep) versions(ctx context.Context, client *embyfin.Client) error {
	for chunk := range slices.Chunk(w.versioned, idsPerRequest) {
		read, err := client.ReadAll(ctx, embyfin.SearchOptions{IDs: strings.Join(chunk, ","), Fields: "Path,MediaSources"}, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			for i := range items {
				for _, src := range items[i].MediaSources {
					if src.Path != "" && src.Path != items[i].Path {
						w.path(&items[i], src.Path, false)
					}
				}
			}

			return true
		})
		if err != nil {
			return err
		}
		w.reads = append(w.reads, read)
	}

	return nil
}

// report is what was found, by where and problem, keeping at most limit rows.
func (w *whitespaceSweep) report(limit int) whitespaceOut {
	out := whitespaceOut{Scanned: w.scanned, People: w.people, Found: len(w.rows), Findings: []whitespaceRow{}}
	rows := slices.Clone(w.rows)
	slices.SortStableFunc(rows, func(a, b whitespaceRow) int {
		return cmp.Or(
			cmp.Compare(slices.Index(whitespaceWhereOrder, a.Where), slices.Index(whitespaceWhereOrder, b.Where)),
			cmp.Compare(slices.Index(whitespace.ProblemOrder, a.Problem), slices.Index(whitespace.ProblemOrder, b.Problem)),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Text, b.Text),
			cmp.Compare(a.ID, b.ID),
		)
	})
	for _, row := range rows {
		switch row.Problem {
		case "odd_space":
			out.Counts.OddSpace++
		case "double_space":
			out.Counts.DoubleSpace++
		case "edge_space":
			out.Counts.EdgeSpace++
		case "space_before_extension":
			out.Counts.SpaceBeforeExtension++
		case "space_before_colon":
			out.Counts.SpaceBeforeColon++
		}
		switch row.Where {
		case "name":
			out.ByWhere.Name++
		case "sort_name":
			out.ByWhere.SortName++
		case "original_title":
			out.ByWhere.OriginalTitle++
		case "genre":
			out.ByWhere.Genre++
		case "tag":
			out.ByWhere.Tag++
		case "studio":
			out.ByWhere.Studio++
		case "person":
			out.ByWhere.Person++
		case "folder":
			out.ByWhere.Folder++
		case "file":
			out.ByWhere.File++
		}
	}
	out.Findings = append(out.Findings, rows[:min(len(rows), limit)]...)
	files := map[string]bool{}
	for _, row := range rows {
		if row.Where == "file" {
			files[row.Path] = true
		}
	}
	out.files = len(files)
	out.changed = changedNote(w.reads...)
	out.Note = out.changed

	return out
}

// whitespaceWanted reads the where input: every place when empty.
func whitespaceWanted(s string) (map[string]bool, error) {
	want := map[string]bool{}
	if strings.TrimSpace(s) == "" {
		for _, w := range whitespaceWhereOrder {
			want[w] = true
		}

		return want, nil
	}
	for w := range strings.SplitSeq(s, ",") {
		w = strings.ToLower(strings.TrimSpace(w))
		if !slices.Contains(whitespaceWhereOrder, w) {
			return nil, fmt.Errorf("where must be among %s, not %q", strings.Join(whitespaceWhereOrder, ", "), w)
		}
		want[w] = true
	}

	return want, nil
}

// auditWhitespace sweeps a library, or every one, for spaces out of place:
// every item's names and path, then the genres, tags, studios and people of
// the films, series and albums, as audit_spelling reads those.
func auditWhitespace(ctx context.Context, client *embyfin.Client, in whitespaceIn) (whitespaceOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	want, err := whitespaceWanted(in.Where)
	if err != nil {
		return whitespaceOut{}, err
	}
	folder, err := client.ResolveLibrary(ctx, in.Library)
	if err != nil {
		return whitespaceOut{}, err
	}
	libraries, err := client.LibraryPaths(ctx)
	if err != nil {
		return whitespaceOut{}, err
	}
	w := newWhitespaceSweep(want, libraries, client.Backend())

	// Settings carries the sort name as set, where it differs from the one
	// listed (see sortNameSet)
	opts := embyfin.SearchOptions{IncludeItemTypes: whitespaceTypes, Fields: "Path,SortName,OriginalTitle,MediaSourceCount,Settings"}
	if folder != nil {
		opts.ParentID = folder.ItemID
	}
	swept, err := sweepAll(ctx, client, opts, embyfin.ToAnswer, func(items []embyfin.Item) {
		for i := range items {
			w.add(&items[i])
		}
	})
	if err != nil {
		return whitespaceOut{}, err
	}
	w.reads = append(w.reads, swept)
	if err := w.versions(ctx, client); err != nil {
		return whitespaceOut{}, err
	}

	// the genres, tags and studios as audit_spelling reads them: films and
	// series, and albums where the library holds music
	types, applies := auditAllTypes(folder, vocabularyTypes, musicAlbum)
	if applies && (want["genre"] || want["tag"] || want["studio"]) {
		vocab := embyfin.SearchOptions{IncludeItemTypes: cmp.Or(types, vocabularyTypes), Fields: embyfin.FieldsVocabulary, ParentID: opts.ParentID}
		read, err := sweepAll(ctx, client, vocab, embyfin.ToAnswer, func(items []embyfin.Item) {
			for i := range items {
				w.vocabulary(&items[i])
			}
		})
		if err != nil {
			return whitespaceOut{}, err
		}
		w.reads = append(w.reads, read)
	}
	if want["person"] {
		if err := w.peopleOf(ctx, client, opts.ParentID); err != nil {
			return whitespaceOut{}, err
		}
	}

	return w.report(limit), nil
}

func registerWhitespaceAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_whitespace",
		Description: "Find spaces where a name should not have them: in the names of films, series, seasons, episodes, music artists, albums and tracks, music videos, home videos and books (photos are not read), their sort names and original titles, the genres, tags and studios of films, series and albums, the names of the people the library credits, every folder of an item's path below the library's own, and every file name. " +
			"double_space: two or more spaces in a row. edge_space: a space at the start or the end. space_before_extension: a file name's stem ends in a space, 'Film (2001) .mkv'. space_before_colon: a space before a colon or the look-alike ꞉ ('Title ꞉ Subtitle', where a renamer writes 'Title꞉ Subtitle'). odd_space: a tab, a line break, a non-breaking or typographic space; the ideographic space U+3000 is left alone, as Japanese titles use it. " +
			"An original title is written in its own language, so a space before a colon and a no-break or narrow space in one, which French sets on purpose, are left alone. A sort name set by hand is judged whole, as it no longer follows the name; one Emby made from the name is judged for what the name has not, and one Jellyfin made is not judged - it writes two spaces where it drops an ampersand or a dash ('asterix  obelix'), which is no fault. " +
			"Each row shows the text with the offending spaces made visible (␣, and [U+00A0] for an odd space), the text exactly as held (value, what metadata_rename takes as from), the text put right, and the item's own title. A value, person or folder many items share is one row with how many; a name or a file is one row per problem. " +
			"A double space in a folder or file name often marks a character a renamer dropped - a colon ('Dune  Part Two' for 'Dune: Part Two'), a word the title spells in asterisks - so check the name against the title before closing the gap; dropped says what the title holds there when the words either side line up. " +
			"Fix names and sort names with item_edit, genres, tags and studios with metadata_rename; each row's fix says how, a person's included. Folders and files are renamed on disk, and no tool here does that. where narrows the rows to some places: file alone for the file names, which a library of episodes has most of.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in whitespaceIn) (*mcp.CallToolResult, whitespaceOut, error) {
		out, err := auditWhitespace(ctx, client, in)

		return nil, out, err
	})
}

// whitespaceAllRow is audit_all's audit_whitespace row: every place but the
// file names, which are one row a file and problem and would bury the rest
// in a library of episodes, and are counted in the note instead.
func whitespaceAllRow(ctx context.Context, client *embyfin.Client, library string) (auditAllRow, error) {
	out, err := auditWhitespace(ctx, client, whitespaceIn{Library: library, Limit: 1})
	if err != nil {
		return auditAllRow{}, err
	}

	return auditAllRow{
		Audit: "audit_whitespace", Findings: out.Found - out.ByWhere.File, Scanned: out.Scanned,
		Where:   strings.Join(slices.DeleteFunc(slices.Clone(whitespaceWhereOrder), func(w string) bool { return w == "file" }), ","),
		Note:    fmt.Sprintf("names, sort names, original titles, genres, tags, studios, people and folders with a space out of place; %d files with one in their name (%d rows, one a file and problem) are not counted here: audit_whitespace where=file lists them", out.files, out.ByWhere.File),
		changed: out.changed,
	}, nil
}

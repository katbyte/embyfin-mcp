package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	spell "github.com/katbyte/go-kt/spelling"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The spelling audit: the same genre, tag or studio spelled several ways
// across a library, found by normalising every value and grouping the ones
// that meet ("Sci-Fi" and "sci fi"), then pairing the ones a typo apart
// ("Romance" and "Romances") or, for studios, one cut short of the other
// ("Warner Bros." and "Warner Bros. Pictures"). Detection is code; which
// spelling to keep is the caller's to decide, and metadata_rename merges.
// The detectors are abs-mcp's, which found them in a real library first.

// spellingCounts gathers, per field, every spelling of every value and how
// many items carry it: field -> normalised key -> spelling -> count.
type spellingCounts map[string]map[string]map[string]int

func newSpellingCounts(fields []string) spellingCounts {
	c := spellingCounts{}
	for _, f := range fields {
		c[f] = map[string]map[string]int{}
	}

	return c
}

// add counts an item's values for every field being gathered.
func (c spellingCounts) add(it *embyfin.Item) {
	for f := range c {
		for _, v := range valuesOf(f, it) {
			c.addValue(f, v)
		}
	}
}

func (c spellingCounts) addValue(field, v string) {
	v = strings.TrimSpace(v)
	k := spell.Key(v)
	if k == "" {
		return
	}
	// the marks go into the key: folded away, "discovery+" was a spelling of
	// "Discovery" and "Idea(L)" one of "Ideal"
	if marks := spell.Marks(v); marks != "" {
		k += markSep + marks
	}
	if c[field][k] == nil {
		c[field][k] = map[string]int{}
	}
	c[field][k][v]++
}

// markSep parts a key's folded value from its marks. Two keys of one folded
// value and other marks are two names, never paired.
const markSep = "\x00"

// folded is a key's folded value, without its marks.
func folded(key string) string {
	f, _, _ := strings.Cut(key, markSep)

	return f
}

// marksOf is a key's marks, "" for none.
func marksOf(key string) string {
	_, m, _ := strings.Cut(key, markSep)

	return m
}

type spelling struct {
	Value string `json:"value"`
	Items int    `json:"items" jsonschema:"how many items carry this exact spelling"`
}

type vocabGroup struct {
	Field string `json:"field" jsonschema:"genres, tags or studios: pass to metadata_rename. albums or artists: names in the tracks' own tags, which a rename on the server does not change - correct the tags in the files and scan"`
	// AlbumArtist is the album artist an albums group was found within:
	// album names are compared only among one artist's albums
	AlbumArtist string     `json:"album_artist,omitempty" jsonschema:"albums: the album artist whose albums these are, as most of its tracks spell it; album names are only compared within one album artist"`
	Kind        string     `json:"kind"                   jsonschema:"spelling: one value spelled several ways; near: a letter or two apart, or only a mark apart (note says), a typo or two different things; contains: two studios, one named by the other's first words - the same company cut short, or two companies (Paramount and Paramount Television), so check before merging"`
	Keep        string     `json:"keep"                   jsonschema:"the most used spelling, which a merge would keep; for near and contains, merge only once both are known to be one thing"`
	Spellings   []spelling `json:"spellings"              jsonschema:"every spelling involved, the most used first"`
	Note        string     `json:"note,omitempty"         jsonschema:"contains: the other studios the shorter name begins, when there are any - it may then be their parent company rather than either one cut short. near: when values differ only by a mark (+, !, brackets), which can make another name or be a typo"`
}

// spellingsOf lists one key's spellings, most used first, then alphabetical
// so the output is stable.
func (c spellingCounts) spellingsOf(field, key string) []spelling {
	out := make([]spelling, 0, len(c[field][key]))
	for v, n := range c[field][key] {
		out = append(out, spelling{Value: v, Items: n})
	}
	sortSpellings(out)

	return out
}

func sortSpellings(sp []spelling) {
	slices.SortFunc(sp, func(a, b spelling) int {
		if a.Items != b.Items {
			return b.Items - a.Items
		}
		return strings.Compare(a.Value, b.Value)
	})
}

// report is everything audit_spelling has to say about one field: the keys
// spelled more than one way, then clusters of keys a typo apart, then, for
// studios, each name beside a longer one it begins. Stable order, so two
// runs read the same.
func (c spellingCounts) report(field string) []vocabGroup {
	keys := make([]string, 0, len(c[field]))
	for k := range c[field] {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var out []vocabGroup
	for _, k := range keys {
		if sp := c.spellingsOf(field, k); len(sp) > 1 {
			out = append(out, vocabGroup{Field: field, Kind: "spelling", Keep: sp[0].Value, Spellings: sp})
		}
	}

	// pairs a typo apart, joined into clusters so that Superhero,
	// Superheroes and Superhreo are one group rather than three overlapping
	// ones
	parent := map[string]string{}
	find := func(k string) string {
		for parent[k] != "" && parent[k] != k {
			k = parent[k]
		}
		return k
	}
	// and a studio beside each longer name it begins, a pair at a time:
	// joined into clusters, "Warner Bros." made one group of itself, Warner
	// Bros. Pictures and Warner Bros. Television, to be merged into the most
	// used - two companies merged on every item through the name of a third
	type pair struct{ short, long string }
	var contains []pair
	marked := map[string]bool{} // a key a mark alone keeps apart from another
	for i, a := range keys {
		for _, b := range keys[i+1:] {
			fa, fb := folded(a), folded(b)
			// one value and other marks: another name ("discovery+" beside
			// "Discovery") or a typo ("Warner Bros. (US" for "Warner Bros.
			// (US)") - two things to check, never one spelling to merge
			if fa == fb {
				if ra, rb := find(a), find(b); ra != rb {
					parent[ra] = rb
				}
				marked[a], marked[b] = true, true

				continue
			}
			// otherwise values marked apart are two names: "discovery+" is
			// neither "Discovery Channel" cut short nor a typo of it
			if marksOf(a) != marksOf(b) {
				continue
			}
			short, long := a, b
			if len(fa) > len(fb) {
				short, long = b, a
			}
			switch {
			case field == fieldStudios && spell.TruncationOf(folded(short), folded(long)):
				contains = append(contains, pair{short, long})
			case spell.TypoApart(fa, fb):
				if ra, rb := find(a), find(b); ra != rb {
					parent[ra] = rb
				}
			}
		}
	}
	clusters := map[string][]string{}
	for _, k := range keys {
		if parent[k] != "" || slices.ContainsFunc(keys, func(o string) bool { return parent[o] == k }) {
			clusters[find(k)] = append(clusters[find(k)], k)
		}
	}
	roots := make([]string, 0, len(clusters))
	for r := range clusters {
		roots = append(roots, r)
	}
	slices.Sort(roots)
	for _, r := range roots {
		var sp []spelling
		note := ""
		for _, k := range clusters[r] {
			sp = append(sp, c.spellingsOf(field, k)...)
			if marked[k] {
				note = "some of these differ only by a mark - a plus, a bang, a bracket - which can make another name ('discovery+' beside 'Discovery') or be a typo ('Warner Bros. (US' for 'Warner Bros. (US)'): check before merging"
			}
		}
		sortSpellings(sp)
		out = append(out, vocabGroup{Field: field, Kind: "near", Keep: sp[0].Value, Spellings: sp, Note: note})
	}

	for _, p := range contains {
		sp := append(c.spellingsOf(field, p.short), c.spellingsOf(field, p.long)...)
		sortSpellings(sp)
		g := vocabGroup{Field: field, Kind: "contains", Keep: sp[0].Value, Spellings: sp}
		// the shorter name beginning other studios too is the plainest sign
		// it names a parent company rather than this one cut short
		var others []string
		for _, q := range contains {
			if q.short == p.short && q.long != p.long {
				others = append(others, c.spellingsOf(field, q.long)[0].Value)
			}
		}
		if len(others) > 0 {
			g.Note = fmt.Sprintf("%q also begins %s: it may name their parent company rather than %q cut short", c.spellingsOf(field, p.short)[0].Value, quotedList(others), c.spellingsOf(field, p.long)[0].Value)
		}
		out = append(out, g)
	}

	return out
}

// quotedList is names as a sentence reads them: "A", "A" and "B", or "A",
// "B" and "C".
func quotedList(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, strconv.Quote(n))
	}
	if len(quoted) == 1 {
		return quoted[0]
	}

	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

// The names in a music library's tags: an album's, compared among its album
// artist's albums, and an artist's. A tagger leaves them spelled one way on
// some tracks and another on the rest ("Wish You Where Here" on one track of
// Wish You Were Here, "The Pink Floyd" on another), and each spelling becomes
// an album or an artist of its own, or a track filed under a name its album
// does not carry. They are the files' tags, so they are corrected in the
// files, not on the server, and metadata_rename does not take them.
const (
	fieldAlbums  = "albums"
	fieldArtists = "artists"
)

var nameFields = []string{fieldAlbums, fieldArtists}

// spellingFieldsAll is every field audit_spelling reads.
var spellingFieldsAll = append(slices.Clone(vocabFields), nameFields...)

// nameField maps what a caller wrote (album, Artists...) onto the name in
// nameFields, or returns "" for anything else.
func nameField(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s != "" && !strings.HasSuffix(s, "s") {
		s += "s"
	}
	if slices.Contains(nameFields, s) {
		return s
	}

	return ""
}

// spellingFields resolves audit_spelling's field argument.
func spellingFields(field string) ([]string, error) {
	f := strings.ToLower(strings.TrimSpace(field))
	if f == "" || f == "all" {
		return spellingFieldsAll, nil
	}
	if v := cmp.Or(vocabField(f), nameField(f)); v != "" {
		return []string{v}, nil
	}

	return nil, fmt.Errorf("unknown field %q; choose one of: %s", field, strings.Join(spellingFieldsAll, ", "))
}

// artistKey is how an artist's name is compared: normalised, with a
// leading "The" set aside - "The Pink Floyd" is Pink Floyd, as "The Beatles"
// and "Beatles" are one band - unless it is the whole name.
func artistKey(v string) string {
	k := spell.Key(v)
	if rest, ok := strings.CutPrefix(k, "the "); ok && rest != "" {
		return rest
	}

	return k
}

// albumsField is the field an album's spellings are counted under: one per
// album artist, so two artists' albums are never compared.
func albumsField(artist string) string { return fieldAlbums + albumSep + artistKey(artist) }

// albumSep parts albumsField's album artist from the field's name.
const albumSep = "\x01"

// albumArtistOf is the artist a track's album is filed under: its album
// artist, else its first artist.
func albumArtistOf(it *embyfin.Item) string {
	if it.AlbumArtist != "" || len(it.Artists) == 0 {
		return it.AlbumArtist
	}

	return it.Artists[0]
}

// spellingResult is what the spelling sweep gathered: the counts, how much
// it read, whether the album names could be read, and the note on the
// library changing under it.
type spellingResult struct {
	counts spellingCounts
	// albumArtists is each album artist's spellings, by the key its albums
	// are counted under, for naming a group's artist
	albumArtists map[string]map[string]int
	items        int
	tracks       int
	// albumsUnread says album names were asked for and tracks were read,
	// but no track came back with its album: nothing was compared, which
	// is not the same as nothing spelled two ways
	albumsUnread string
	note         string
}

// groups is everything the sweep has to say about the fields, in order.
func (r *spellingResult) groups(fields []string) []vocabGroup {
	var out []vocabGroup
	for _, f := range fields {
		if f != fieldAlbums {
			out = append(out, r.counts.report(f)...)
			continue
		}
		var scopes []string
		for k := range r.counts {
			if strings.HasPrefix(k, fieldAlbums+albumSep) {
				scopes = append(scopes, k)
			}
		}
		slices.Sort(scopes)
		for _, scope := range scopes {
			artist := ""
			if sp := r.artistSpellings(strings.TrimPrefix(scope, fieldAlbums+albumSep)); len(sp) > 0 {
				artist = sp[0].Value
			}
			for _, g := range r.counts.report(scope) {
				g.Field, g.AlbumArtist = fieldAlbums, artist
				out = append(out, g)
			}
		}
	}

	return out
}

// artistSpellings is an album artist's spellings, most used first.
func (r *spellingResult) artistSpellings(key string) []spelling {
	out := make([]spelling, 0, len(r.albumArtists[key]))
	for v, n := range r.albumArtists[key] {
		out = append(out, spelling{Value: v, Items: n})
	}
	sortSpellings(out)

	return out
}

// addAlbum counts one album name under its album artist.
func (r *spellingResult) addAlbum(album, artist string) {
	field := albumsField(artist)
	if r.counts[field] == nil {
		r.counts[field] = map[string]map[string]int{}
	}
	r.counts.addValue(field, album)
	key := artistKey(artist)
	if r.albumArtists[key] == nil {
		r.albumArtists[key] = map[string]int{}
	}
	if a := strings.TrimSpace(artist); a != "" {
		r.albumArtists[key][a]++
	}
}

// spellingAudit sweeps a library's items and gathers the fields' spellings:
// genres, tags and studios off the items of types, and album and artist
// names off the library's tracks, whatever types says - or, for albums, off
// its album entries when the server gives no track's album in a list.
func spellingAudit(ctx context.Context, client *embyfin.Client, library, types string, fields []string) (*spellingResult, error) {
	var vocab, names []string
	for _, f := range fields {
		if slices.Contains(nameFields, f) {
			names = append(names, f)
		} else {
			vocab = append(vocab, f)
		}
	}
	r := &spellingResult{counts: newSpellingCounts(vocab), albumArtists: map[string]map[string]int{}}
	var notes []string
	if len(vocab) > 0 {
		opts, err := sweepOptions(ctx, client, library, types, vocabularyTypes, embyfin.FieldsVocabulary)
		if err != nil {
			return nil, err
		}
		read, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
			for i := range items {
				r.items++
				r.counts.add(&items[i])
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		notes = append(notes, read.Changed())
	}
	r.note = joinNotes(notes...)
	if len(names) == 0 {
		return r, nil
	}
	// the names are music's: a library that holds none is not read for them
	if music, err := holdsMusic(ctx, client, library); err != nil || !music {
		return r, err
	}

	artists, albums := slices.Contains(names, fieldArtists), slices.Contains(names, fieldAlbums)
	if artists {
		r.counts[fieldArtists] = map[string]map[string]int{}
	}
	opts, err := sweepOptions(ctx, client, library, "Audio", "Audio", "Path")
	if err != nil {
		return nil, err
	}
	withAlbum := 0
	read, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			r.tracks++
			if artists {
				// each name once a track, whether it is the track's artist,
				// its album artist or both
				seen := map[string]bool{}
				for _, a := range append(slices.Clone(it.Artists), it.AlbumArtist) {
					if a = strings.TrimSpace(a); a != "" && !seen[a] {
						seen[a] = true
						r.addArtist(a)
					}
				}
			}
			if albums && strings.TrimSpace(it.Album) != "" {
				withAlbum++
				r.addAlbum(it.Album, albumArtistOf(it))
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	notes = append(notes, read.Changed())
	if albums && withAlbum == 0 && r.tracks > 0 {
		r.albumsUnread = fmt.Sprintf("none of the %d tracks read came back with an album name, so album names were not compared: whether any is spelled two ways is not known", r.tracks)
	}
	r.note = joinNotes(slices.Compact(notes)...)

	return r, nil
}

// holdsMusic says whether a library, or any library when none is named, can
// hold music: a music library, or one of mixed content, which on both
// servers has no collection type.
func holdsMusic(ctx context.Context, client *embyfin.Client, library string) (bool, error) {
	music := func(f *embyfin.VirtualFolder) bool {
		return f.CollectionType == "music" || f.CollectionType == "" || f.CollectionType == "mixed"
	}
	if library != "" {
		folder, err := client.ResolveLibrary(ctx, library)
		if err != nil || folder == nil {
			return folder == nil && err == nil, err
		}

		return music(folder), nil
	}
	folders, err := client.VirtualFolders(ctx)
	if err != nil {
		return false, err
	}

	return slices.ContainsFunc(folders, func(f embyfin.VirtualFolder) bool { return music(&f) }), nil
}

// addArtist counts one artist's name, a leading "The" set aside in the key.
func (r *spellingResult) addArtist(v string) {
	k := artistKey(v)
	if k == "" {
		return
	}
	if marks := spell.Marks(v); marks != "" {
		k += markSep + marks
	}
	if r.counts[fieldArtists][k] == nil {
		r.counts[fieldArtists][k] = map[string]int{}
	}
	r.counts[fieldArtists][k][strings.TrimSpace(v)]++
}

func registerSpellingTools(r *registry) {
	client := r.client

	type auditIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Field   string `json:"field,omitempty"   jsonschema:"genres, tags, studios, albums or artists; default all five"`
		Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types whose genres, tags and studios are read; default Movie,Series. Albums and artists are read off the tracks whatever this says"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups to return, default 50"`
	}
	type auditOut struct {
		Scanned    int          `json:"items_scanned"         jsonschema:"items whose genres, tags and studios were read"`
		Tracks     int          `json:"tracks_scanned"        jsonschema:"tracks whose album and artist names were read; 0 when neither was asked for, or the library holds no music"`
		AlbumsNote string       `json:"albums_note,omitempty" jsonschema:"set when album names were asked for and no track came back with one: none was compared, which says nothing of whether any is spelled two ways"`
		Found      int          `json:"total_findings"        jsonschema:"groups of every kind, before limit"`
		Groups     []vocabGroup `json:"groups"`
		Note       string       `json:"note,omitempty"        jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing, or counted though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_spelling",
		Description: "Find genres, tags and studios that mean the same thing but are spelled differently: 'Sci-Fi' and 'Sci Fi', 'Science-Fiction' and 'Science Fiction', a letter apart ('Superhero' and 'Superhreo'), or a studio named by another's first words ('Warner Bros.' and 'Warner Bros. Pictures'). Each group says which kind it is and which spelling is most used. " +
			"In a music library it reads album and artist names too, off the tracks' tags: an album spelled two ways by one album artist ('Wish You Were Here' and 'Wish You Where Here'), compared only among that artist's albums, and an artist spelled two ways across the library, a leading 'The' counting as a spelling ('The Pink Floyd' and 'Pink Floyd'). Those are names in the files' tags, which a rename on the server does not change and a scan brings back: correct them in the files and scan, not with metadata_rename. Each spelling counts the tracks that carry it; when no track comes back with its album, albums_note says album names were not compared. " +
			"Merge a group with metadata_rename, passing the field and the spellings as reported here. A near or a contains group can be two different things - 'Paramount' and 'Paramount Television' are two companies - so read both first. A studio and a longer name it begins are a pair of their own, never joined to a third through it, and a note says when the shorter name begins others too, which makes it likelier a parent company than either one cut short. Values a whole short word apart ('teenage life', 'teenage love') are no near group, though two letters swapped in one ('Film Nior') are. Values only a mark apart ('discovery+' beside 'Discovery', 'Yahoo!' beside 'Yahoo') are never one spelling: a near group, with a note, as the mark can make another name or be a typo. " +
			"Studio names mostly come from the metadata provider, so a merge of them does not last: an item_refresh with replace_all puts the provider's names back on both servers, and on Jellyfin a plain refresh adds the provider's name back beside the merged one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		fields, err := spellingFields(in.Field)
		if err != nil {
			return nil, auditOut{}, err
		}
		sweep, err := spellingAudit(ctx, client, in.Library, in.Types, fields)
		if err != nil {
			return nil, auditOut{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		out := auditOut{Scanned: sweep.items, Tracks: sweep.tracks, AlbumsNote: sweep.albumsUnread, Groups: []vocabGroup{}, Note: sweep.note}
		for _, g := range sweep.groups(fields) {
			out.Found++
			if len(out.Groups) < limit {
				out.Groups = append(out.Groups, g)
			}
		}

		return nil, out, nil
	})

	type renameIn struct {
		Field   string `json:"field"             jsonschema:"genres, tags or studios, as audit_spelling reports it"`
		From    string `json:"from"              jsonschema:"the value to replace, exactly as it is spelled now"`
		To      string `json:"to,omitempty"      jsonschema:"the value to keep; renaming onto one an item already carries merges the two"`
		Remove  bool   `json:"remove,omitempty"  jsonschema:"instead of renaming: take the value off every item that carries it"`
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
	}
	type renameOut struct {
		Field        string   `json:"field"`
		ItemsUpdated int      `json:"updated"`
		IDs          []string `json:"ids"                    jsonschema:"every item changed, by id: what item_edit takes to put the old value back on them"`
		Merged       []string `json:"merged,omitempty"       jsonschema:"of those, the ones that already carried the new value, and so only lost the old one"`
		Items        []string `json:"items"                  jsonschema:"the titles changed, the first 50"`
		StillListed  []string `json:"still_listed,omitempty" jsonschema:"items changed that the server still showed with the old value ten seconds after - to its filter for it, or on their rows as item_get and the lists read them - though each one's own record has it no more: until the server catches up, its lists, audits and item_get can show the old value on them"`
		Note         string   `json:"note,omitempty"         jsonschema:"set when the library was seen to change while the items carrying the value were read: one added meanwhile may not have been renamed, so run again to be sure. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read. A read that cannot be sure fails instead, and nothing is changed"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "metadata_rename",
		Description: "Rename a genre, tag or studio on every item that carries it - in every library, unless library names one - or with remove take it off them all. Renaming onto a value an item already carries merges the two, which is how a group from audit_spelling is fixed ('Sci-Fi' into 'Science Fiction'). " +
			"from is matched exactly as spelled, so one spelling can be merged into another that differs only in case or punctuation. Items carrying the value are found with the server's own filter, then edited one by one. " +
			"A merge or a remove cannot be undone by renaming back: the items that carried both values, or the one removed, are no longer told apart. ids in the answer lists every item changed and merged the ones that already carried the new value, which is what item_edit's add_* and remove_* need to put the old value back. " +
			"Each item is edited as item_edit edits it: in a library that saves nfos the server writes the item's nfo beside its media, over the one there, and a refresh with replace_all or an identify can bring back the value the provider gives. An edit that fails part way names the items already changed, and running the same call again finishes it. " +
			"Afterwards it reads the items changed again - through the server's own filter for the old value, and each by id as item_get reads it - until none shows the old value (Emby shows an album or an artist with its tracks' old genre too for a moment after they change), ten seconds at most; still_listed names any the server still shows it on, and an item whose own record has the old value again is an error.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in renameIn) (*mcp.CallToolResult, renameOut, error) {
		field := vocabField(in.Field)
		if f := nameField(in.Field); field == "" && f != "" {
			return nil, renameOut{}, fmt.Errorf("%s are names in the tracks' own tags, which a rename on the server does not change and the next scan reads back: correct them in the files and scan", f)
		}
		if field == "" {
			return nil, renameOut{}, fmt.Errorf("unknown field %q; choose one of: %s", in.Field, strings.Join(vocabFields, ", "))
		}
		from, to := strings.TrimSpace(in.From), strings.TrimSpace(in.To)
		switch {
		case from == "":
			return nil, renameOut{}, errors.New("from is required")
		case in.Remove && to != "":
			return nil, renameOut{}, errors.New("pass either to or remove, not both")
		case !in.Remove && to == "":
			return nil, renameOut{}, errors.New("to is required unless remove is set")
		case from == to:
			return nil, renameOut{}, errors.New("from and to are the same spelling")
		}

		opts := embyfin.SearchOptions{ExcludeItemTypes: "Folder,CollectionFolder,UserRootFolder,AggregateFolder", Fields: embyfin.FieldsVocabulary}
		switch field {
		case fieldGenres:
			opts.Genres = []string{from}
		case fieldTags:
			opts.Tags = []string{from}
		case fieldStudios:
			opts.Studios = []string{from}
		}
		folder, err := client.ResolveLibrary(ctx, in.Library)
		if err != nil {
			return nil, renameOut{}, err
		}
		if folder != nil {
			opts.ParentID = folder.ItemID
		}

		// collect first: editing while paging would move the pages
		var ids []string
		// the items to edit: a read that cannot be sure of them fails, and
		// nothing is renamed
		carrying, err := client.ReadAll(ctx, opts, embyfin.ToAct, func(items []embyfin.Item) bool {
			for i := range items {
				if slices.Contains(valuesOf(field, &items[i]), from) {
					ids = append(ids, items[i].ID)
				}
			}
			return true
		})
		if err != nil {
			return nil, renameOut{}, err
		}

		admin, err := client.ResolveUser(ctx, "")
		if err != nil {
			return nil, renameOut{}, err
		}
		out := renameOut{Field: field, IDs: []string{}, Items: []string{}, Note: carrying.Changed()}
		var done []memberRow
		for _, id := range ids {
			changed, merged := false, false
			full, err := client.EditItem(ctx, admin.ID, id, func(full map[string]any) (bool, error) {
				current := vocabularyOf(full, field)
				merged = to != "" && slices.Contains(current, to)
				var values []string
				if values, changed = renameValue(current, from, to); changed {
					setVocabulary(full, field, values)
				}
				return changed, nil
			})
			if err != nil {
				return nil, renameOut{}, partlyDone(id, err, done, len(ids))
			}
			if !changed {
				continue
			}
			out.ItemsUpdated++
			out.IDs = append(out.IDs, id)
			if merged {
				out.Merged = append(out.Merged, id)
			}
			done = append(done, memberRow{ID: id, Name: itemName(full)})
			if len(out.Items) < 50 {
				out.Items = append(out.Items, itemName(full))
			}
		}
		if len(out.IDs) == 0 {
			return nil, out, nil
		}

		still, serr := r.settleRename(ctx, opts, admin.ID, field, from, out.IDs)
		if serr != nil {
			return nil, renameOut{}, fmt.Errorf("%w; the %d items edited were: %s", serr, len(done), membersSaid(done))
		}
		out.StillListed = still

		return nil, out, nil
	})
}

// settleWait is how many settle intervals a rename waits for the server to
// stop showing the old value: ten seconds at the default interval, for a
// server busy with other work (the album's genres cleared within two seconds
// on an idle one).
const settleWait = 40

// settleRename reads the items changed again after a rename until none shows
// the old value - neither to the server's own filter for it nor on its own
// row, read by id, which is what item_get and a list show - settleWait
// intervals at most. Emby lists an album, and at times its artist, with both
// the old and the new genre for a moment after its tracks are renamed, the
// genres it gives them from their tracks (seen on 4.10, clearing within two
// seconds). An item still showing it then is read on its own: one that has
// the old value there too is an error, and the others are returned, still
// shown with the old value though their own record has it no more. A read
// of the filter the library changed under cannot say none shows it: the
// wait goes on, and ends in an error if no read is sure.
func (r *registry) settleRename(ctx context.Context, opts embyfin.SearchOptions, userID, field, from string, changed []string) ([]string, error) {
	isChanged := make(map[string]bool, len(changed))
	for _, id := range changed {
		isChanged[id] = true
	}
	var still []string
	unsure := ""
	for try := range settleWait {
		if try > 0 {
			if err := r.pause(ctx); err != nil {
				return nil, err
			}
		}
		showing := map[string]bool{}
		read, err := r.client.ReadAll(ctx, opts, embyfin.ToAct, func(items []embyfin.Item) bool {
			for i := range items {
				if isChanged[items[i].ID] && slices.Contains(valuesOf(field, &items[i]), from) {
					showing[items[i].ID] = true
				}
			}
			return true
		})
		if err != nil {
			return nil, fmt.Errorf("renamed, but reading the server's filter for %s again afterwards failed, so whether its lists have caught up is not known: %w", from, err)
		}
		unsure = read.Changed()
		for start := 0; start < len(changed); start += orphanBatch {
			batch := changed[start:min(start+orphanBatch, len(changed))]
			items, _, err := r.client.Search(ctx, embyfin.SearchOptions{IDs: strings.Join(batch, ","), Fields: embyfin.FieldsVocabulary, Limit: len(batch)})
			if err != nil {
				return nil, fmt.Errorf("renamed, but reading the items changed back failed, so whether the server still shows %s on them is not known: %w", from, err)
			}
			for i := range items {
				if isChanged[items[i].ID] && slices.Contains(valuesOf(field, &items[i]), from) {
					showing[items[i].ID] = true
				}
			}
		}
		still = slices.Sorted(maps.Keys(showing))
		if len(still) == 0 && unsure == "" {
			return nil, nil
		}
	}
	if len(still) == 0 {
		return nil, fmt.Errorf("renamed, but the library changed each time the server's filter for %s was read again, so whether its lists have caught up is not known: %s", from, unsure)
	}
	var kept []string
	for _, id := range still {
		full, err := r.client.FullItem(ctx, userID, id)
		if err != nil {
			return nil, fmt.Errorf("renamed, but the server still listed %s under %s a few seconds after, and reading it failed: %w", id, from, err)
		}
		if slices.Contains(vocabularyOf(full, field), from) {
			kept = append(kept, itemName(full)+" ("+id+")")
		}
	}
	if len(kept) > 0 {
		return nil, fmt.Errorf("renamed, but a few seconds after, the server has %s on %s again: it kept the old value or put it back", from, strings.Join(kept, ", "))
	}

	return still, nil
}

// renameValue replaces from with to in a list, in place, or drops from when
// to is empty or already in the list; both matched exactly.
func renameValue(values []string, from, to string) ([]string, bool) {
	i := slices.Index(values, from)
	if i < 0 {
		return values, false
	}
	if to == "" || slices.Contains(values, to) {
		return slices.Delete(slices.Clone(values), i, i+1), true
	}
	out := slices.Clone(values)
	out[i] = to

	return out, true
}

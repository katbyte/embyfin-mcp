package tools

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The spelling audit: the same genre, tag or studio spelled several ways
// across a library, found by normalising every value and grouping the ones
// that meet ("Sci-Fi" and "sci fi"), then pairing the ones a typo apart
// ("Romance" and "Romances") or, for studios, one cut short of the other
// ("Warner Bros." and "Warner Bros. Pictures"). Detection is code; which
// spelling to keep is the caller's to decide, and metadata_rename merges.
// The detectors are abs-mcp's, which found them in a real library first.

// norm lowercases a value, folds accented letters to plain ones, turns the
// separators people type interchangeably into spaces, and drops the rest of
// the punctuation: "Sci-Fi" and "sci fi" meet at "sci fi". A letter or digit
// of any other script is kept as it is, lowercased.
func norm(s string) string {
	var b strings.Builder
	latin := false // the last rune written was plain ASCII
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ' ':
			b.WriteRune(r)
			latin = r != ' '
		case r == '_' || r == '-' || r == '.' || r == '/':
			b.WriteRune(' ')
			latin = false
		case r > 127 && unicode.IsSpace(r):
			// an ideographic or a non-breaking space is still a space
			b.WriteRune(' ')
			latin = false
		case r > 127:
			// Amélie is Amelie, not Amlie; 進撃の巨人 is itself, not nothing
			if spelling, word := wordRune(r, latin); word && spelling != "" {
				b.WriteString(spelling)
				latin = spelling[0] < utf8.RuneSelf
			}
		}
	}

	return strings.Join(strings.Fields(b.String()), " ")
}

// wordRune is how the folds for comparing names (norm here, folderKey for
// folders) read a rune past ASCII. An accented Latin letter is its plain
// spelling, and any other letter or digit is itself, whatever its script:
// dropping those made every name written in Japanese, Greek or Cyrillic
// fold to nothing, so unrelated ones all met. A combining mark belongs to
// the letter before it: on a plain Latin letter it is an accent written
// apart (e and U+0301 for é), folded away as the composed letter's is, and
// in a script that writes its vowels as marks it is part of the word and
// kept. word is false for punctuation, symbols and spaces.
func wordRune(r rune, afterLatin bool) (spelling string, word bool) {
	if folded := foldLetter(r); folded != "" {
		return folded, true
	}
	switch {
	case unicode.IsLetter(r), unicode.IsDigit(r):
		return string(r), true
	case unicode.IsMark(r) && afterLatin:
		return "", true
	case unicode.IsMark(r):
		return string(r), true
	}

	return "", false
}

// foldLetter is the plain-ASCII spelling of an accented Latin letter, or
// nothing for a rune that is not one.
func foldLetter(r rune) string {
	for ascii, accented := range foldTable {
		if strings.ContainsRune(accented, r) {
			return ascii
		}
	}

	return ""
}

var foldTable = map[string]string{
	"a": "àáâãäåāąă", "ae": "æ", "c": "çćčċ", "d": "ďđð", "e": "èéêëēęěė", "g": "ğģ",
	"i": "ìíîïīıį", "l": "łļľ", "n": "ñńňņ", "o": "òóôõöøōőœ", "r": "řŗ", "s": "šşśș", "ss": "ß",
	"t": "ťţț", "th": "þ", "u": "ùúûüūůűų", "y": "ýÿ", "z": "žźż",
}

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
	k := norm(v)
	if k == "" {
		return
	}
	// the marks go into the key: folded away, "discovery+" was a spelling of
	// "Discovery" and "Idea(L)" one of "Ideal"
	if marks := nameMarks(v); marks != "" {
		k += markSep + marks
	}
	if c[field][k] == nil {
		c[field][k] = map[string]int{}
	}
	c[field][k][v]++
}

// nameMarks are the marks in a value that make another name of it, in the
// order they come: a plus, a bang, a bracket - "discovery+" is a streaming
// brand and "Discovery" a channel, "Idea(L)" a company and "Ideal" another.
// Case, spacing, accents, separators, an apostrophe or an ampersand make no
// other name ("Action & Adventure" is "Action Adventure").
func nameMarks(v string) string {
	var b strings.Builder
	for _, r := range v {
		if strings.ContainsRune("+!()[]{}#@*$%=~|<>^", r) {
			b.WriteRune(r)
		}
	}

	return b.String()
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
	Field     string     `json:"field"          jsonschema:"pass to metadata_rename"`
	Kind      string     `json:"kind"           jsonschema:"spelling: one value spelled several ways; near: a letter or two apart, or only a mark apart (note says), a typo or two different things; contains: two studios, one named by the other's first words - the same company cut short, or two companies (Paramount and Paramount Television), so check before merging"`
	Keep      string     `json:"keep"           jsonschema:"the most used spelling, which a merge would keep; for near and contains, merge only once both are known to be one thing"`
	Spellings []spelling `json:"spellings"      jsonschema:"every spelling involved, the most used first"`
	Note      string     `json:"note,omitempty" jsonschema:"contains: the other studios the shorter name begins, when there are any - it may then be their parent company rather than either one cut short. near: when values differ only by a mark (+, !, brackets), which can make another name or be a typo"`
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
			case field == fieldStudios && truncationOf(folded(short), folded(long)):
				contains = append(contains, pair{short, long})
			case typoApart(fa, fb):
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

// truncationOf reports whether short is long cut off ("warner bros" in
// "warner bros pictures"): at least six letters, and whole words. Both are
// normalised.
func truncationOf(short, long string) bool {
	if short == long || letters(strings.ReplaceAll(short, " ", "")) < 6 {
		return false
	}

	return strings.HasPrefix(long, short+" ")
}

// letters is how long a normalised value is, in letters rather than bytes:
// an ideograph takes three bytes, and counted that way a two-letter name
// passed for a six-letter one and was judged a typo apart from every other
// two-letter name sharing one of its letters.
func letters(s string) int { return utf8.RuneCountInString(s) }

// typoApart reports whether two normalised values differ by a slip of the
// keyboard: one edit for anything six letters or longer, two for twelve or
// longer when they start with the same word - and, between values of as
// many words, every word that differs is a slip itself (see slipsInWords).
func typoApart(a, b string) bool {
	if a == b || letters(a) < 6 || letters(b) < 6 {
		return false
	}
	switch typoDistance(a, b, 2) {
	case 0, 1:
	case 2:
		if letters(a) < 12 || letters(b) < 12 || strings.Fields(a)[0] != strings.Fields(b)[0] {
			return false
		}
	default:
		return false
	}

	return slipsInWords(a, b)
}

// slipsInWords says whether two values of as many words differ only by
// slips within words: each word that differs is five letters or more and
// one edit from the other, or a shorter one with two letters swapped ("Nior"
// for "Noir", "Flim" for "Film") or letters dropped from its middle ("Nr").
// A shorter word otherwise an edit or two apart is as often another word as
// a slip - "teenage life" and "teenage love", "coming of age" and "coming of
// rage" - and the values two things. Values of more or fewer words differ by
// a space added or dropped, which is a slip.
func slipsInWords(a, b string) bool {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) != len(wb) {
		return true
	}
	for i := range wa {
		switch {
		case wa[i] == wb[i]:
		case letters(wa[i]) >= 5 && letters(wb[i]) >= 5:
			if typoDistance(wa[i], wb[i], 1) > 1 {
				return false
			}
		case !swapped(wa[i], wb[i]) && !droppedFromMiddle(wa[i], wb[i]) && !droppedFromMiddle(wb[i], wa[i]):
			return false
		}
	}

	return true
}

// swapped says whether two words are one with two letters next to each other
// swapped: "nior" and "noir".
func swapped(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) != len(rb) || len(ra) < 3 {
		return false
	}
	at := -1
	for i := range ra {
		if ra[i] != rb[i] {
			at = i

			break
		}
	}

	return at >= 0 && at+1 < len(ra) && ra[at] == rb[at+1] && ra[at+1] == rb[at] && string(ra[at+2:]) == string(rb[at+2:])
}

// droppedFromMiddle says whether short is long with letters dropped from
// between its first and its last: "nr" from "noir". A letter added or lost
// at either end ("age", "rage") makes another word as often as not, and so
// does one changed ("life", "lift").
func droppedFromMiddle(short, long string) bool {
	s, l := []rune(short), []rune(long)
	if len(s) < 2 || len(s) >= len(l) || s[0] != l[0] || s[len(s)-1] != l[len(l)-1] {
		return false
	}
	at := 0
	for _, r := range l {
		if at < len(s) && r == s[at] {
			at++
		}
	}

	return at == len(s)
}

// typoDistance is the Damerau-Levenshtein distance (optimal string alignment,
// so a transposition is one edit), capped: anything past limit comes back as
// limit+1, and strings whose lengths differ by more than limit are not walked.
func typoDistance(a, b string, limit int) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > limit || -d > limit {
		return limit + 1
	}
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
			best = min(best, cur[j])
		}
		if best > limit {
			return limit + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	if prev[len(rb)] > limit {
		return limit + 1
	}

	return prev[len(rb)]
}

// spellingFields resolves audit_spelling's field argument.
func spellingFields(field string) ([]string, error) {
	f := strings.ToLower(strings.TrimSpace(field))
	if f == "" || f == "all" {
		return vocabFields, nil
	}
	if f = vocabField(f); f == "" {
		return nil, fmt.Errorf("unknown field %q; choose one of: %s", field, strings.Join(vocabFields, ", "))
	}

	return []string{f}, nil
}

// spellingAudit sweeps a library's items and gathers the fields' spellings,
// with the note on the library changing under the sweep.
func spellingAudit(ctx context.Context, client *embyfin.Client, library, types string, fields []string) (spellings spellingCounts, items int, note string, err error) {
	opts, err := sweepOptions(ctx, client, library, types, vocabularyTypes, embyfin.FieldsVocabulary)
	if err != nil {
		return nil, 0, "", err
	}
	counts := newSpellingCounts(fields)
	scanned := 0
	read, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			scanned++
			counts.add(&items[i])
		}
		return true
	})
	if err != nil {
		return nil, 0, "", err
	}

	return counts, scanned, read.Changed(), nil
}

func registerSpellingTools(r *registry) {
	client := r.client

	type auditIn struct {
		Library string `json:"library,omitempty" jsonschema:"library name or id; default every library"`
		Field   string `json:"field,omitempty"   jsonschema:"genres, tags or studios; default all three"`
		Types   string `json:"types,omitempty"   jsonschema:"comma-separated item types to read; default Movie,Series"`
		Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups to return, default 50"`
	}
	type auditOut struct {
		Scanned int          `json:"items_scanned"`
		Found   int          `json:"total_findings" jsonschema:"groups of every kind, before limit"`
		Groups  []vocabGroup `json:"groups"`
		Note    string       `json:"note,omitempty" jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing, or counted though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_spelling",
		Description: "Find genres, tags and studios that mean the same thing but are spelled differently: 'Sci-Fi' and 'Sci Fi', 'Science-Fiction' and 'Science Fiction', a letter apart ('Superhero' and 'Superhreo'), or a studio named by another's first words ('Warner Bros.' and 'Warner Bros. Pictures'). Each group says which kind it is and which spelling is most used. " +
			"Merge a group with metadata_rename, passing the field and the spellings as reported here. A near or a contains group can be two different things - 'Paramount' and 'Paramount Television' are two companies - so read both first. A studio and a longer name it begins are a pair of their own, never joined to a third through it, and a note says when the shorter name begins others too, which makes it likelier a parent company than either one cut short. Values a whole short word apart ('teenage life', 'teenage love') are no near group, though two letters swapped in one ('Film Nior') are. Values only a mark apart ('discovery+' beside 'Discovery', 'Yahoo!' beside 'Yahoo') are never one spelling: a near group, with a note, as the mark can make another name or be a typo. " +
			"Studio names mostly come from the metadata provider, so a merge of them does not last: an item_refresh with replace_all puts the provider's names back on both servers, and on Jellyfin a plain refresh adds the provider's name back beside the merged one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		fields, err := spellingFields(in.Field)
		if err != nil {
			return nil, auditOut{}, err
		}
		counts, scanned, note, err := spellingAudit(ctx, client, in.Library, in.Types, fields)
		if err != nil {
			return nil, auditOut{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		out := auditOut{Scanned: scanned, Groups: []vocabGroup{}, Note: note}
		for _, f := range fields {
			for _, g := range counts.report(f) {
				out.Found++
				if len(out.Groups) < limit {
					out.Groups = append(out.Groups, g)
				}
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
		folder, err := resolveLibrary(ctx, client, in.Library)
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

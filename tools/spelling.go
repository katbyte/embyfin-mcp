package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	if c[field][k] == nil {
		c[field][k] = map[string]int{}
	}
	c[field][k][v]++
}

type spelling struct {
	Value string `json:"value"`
	Items int    `json:"items" jsonschema:"how many items carry this exact spelling"`
}

type vocabGroup struct {
	Field     string     `json:"field"     jsonschema:"pass to metadata_rename"`
	Kind      string     `json:"kind"      jsonschema:"spelling: one value spelled several ways; near: a letter or two apart, a typo or two different things; contains: a studio name that is another cut short"`
	Keep      string     `json:"keep"      jsonschema:"the spelling to merge into: the most used"`
	Spellings []spelling `json:"spellings" jsonschema:"every spelling involved, the one to keep first"`
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
// spelled more than one way, then clusters of keys a typo apart or, for
// studios, one another cut short. Stable order, so two runs read the same.
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

	// pairs that are a typo apart or one cut short of the other, joined into
	// clusters so that Superhero, Superheroes and Superhreo are one
	// group rather than three overlapping ones
	parent := map[string]string{}
	find := func(k string) string {
		for parent[k] != "" && parent[k] != k {
			k = parent[k]
		}
		return k
	}
	kinds := map[string]string{}
	for i, a := range keys {
		for _, b := range keys[i+1:] {
			short, long := a, b
			if len(short) > len(long) {
				short, long = b, a
			}
			var kind string
			switch {
			case field == fieldStudios && truncationOf(short, long):
				kind = "contains"
			case typoApart(a, b):
				kind = "near"
			default:
				continue
			}
			if ra, rb := find(a), find(b); ra != rb {
				parent[ra] = rb
			}
			if root := find(a); kinds[root] == "" || kind == "contains" {
				kinds[root] = kind
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
		kind := kinds[r]
		var sp []spelling
		for _, k := range clusters[r] {
			if kinds[k] == "contains" { // recorded under a member merged in later
				kind = "contains"
			}
			sp = append(sp, c.spellingsOf(field, k)...)
		}
		sortSpellings(sp)
		out = append(out, vocabGroup{Field: field, Kind: kind, Keep: sp[0].Value, Spellings: sp})
	}

	return out
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
// longer when they start with the same word.
func typoApart(a, b string) bool {
	if a == b || letters(a) < 6 || letters(b) < 6 {
		return false
	}
	switch typoDistance(a, b, 2) {
	case 0, 1:
		return true
	case 2:
		if letters(a) < 12 || letters(b) < 12 {
			return false
		}
		return strings.Fields(a)[0] == strings.Fields(b)[0]
	}

	return false
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

// spellingAudit sweeps a library's items and gathers the fields' spellings.
func spellingAudit(ctx context.Context, client *embyfin.Client, library, types string, fields []string) (spellingCounts, int, error) {
	opts, err := sweepOptions(ctx, client, library, types, vocabularyTypes, embyfin.FieldsVocabulary)
	if err != nil {
		return nil, 0, err
	}
	counts := newSpellingCounts(fields)
	scanned := 0
	err = client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			scanned++
			counts.add(&items[i])
		}
		return true
	})

	return counts, scanned, err
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
	}
	add(r, readTool, &mcp.Tool{
		Name: "audit_spelling",
		Description: "Find genres, tags and studios that mean the same thing but are spelled differently: 'Sci-Fi' and 'Sci Fi', 'Science-Fiction' and 'Science Fiction', a letter apart ('Superhero' and 'Superhreo'), or a studio cut short ('Warner Bros.' and 'Warner Bros. Pictures'). Each group says which kind it is and which spelling is most used. " +
			"Merge a group with metadata_rename, passing the field and the spellings as reported here; a near group can be two different things, so read both first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		fields, err := spellingFields(in.Field)
		if err != nil {
			return nil, auditOut{}, err
		}
		counts, scanned, err := spellingAudit(ctx, client, in.Library, in.Types, fields)
		if err != nil {
			return nil, auditOut{}, err
		}

		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		out := auditOut{Scanned: scanned, Groups: []vocabGroup{}}
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
		Items        []string `json:"items"   jsonschema:"the titles changed, capped at 50"`
	}
	add(r, writeTool, &mcp.Tool{
		Name: "metadata_rename",
		Description: "Rename a genre, tag or studio on every item that carries it, or with remove take it off them all. Renaming onto a value an item already carries merges the two, which is how a group from audit_spelling is fixed ('Sci-Fi' into 'Science Fiction'). " +
			"from is matched exactly as spelled, so one spelling can be merged into another that differs only in case or punctuation. Items carrying the value are found with the server's own filter, then edited one by one. Changes server state.",
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
		if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
			for i := range items {
				if slices.Contains(valuesOf(field, &items[i]), from) {
					ids = append(ids, items[i].ID)
				}
			}
			return true
		}); err != nil {
			return nil, renameOut{}, err
		}

		admin, err := client.ResolveUser(ctx, "")
		if err != nil {
			return nil, renameOut{}, err
		}
		out := renameOut{Field: field, Items: []string{}}
		for _, id := range ids {
			changed := false
			full, err := client.EditItem(ctx, admin.ID, id, func(full map[string]any) (bool, error) {
				var values []string
				if values, changed = renameValue(vocabularyOf(full, field), from, to); changed {
					setVocabulary(full, field, values)
				}
				return changed, nil
			})
			if err != nil {
				return nil, out, fmt.Errorf("%s: %w (%d items were updated before it)", id, err, out.ItemsUpdated)
			}
			if !changed {
				continue
			}
			out.ItemsUpdated++
			if len(out.Items) < 50 {
				out.Items = append(out.Items, itemName(full))
			}
		}

		return nil, out, nil
	})
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

package tools

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/naming"
	"github.com/katbyte/embyfin-mcp/lib/tmdb"
)

// versionRow is one file an item is shown in.
type versionRow struct {
	ID       string `json:"id,omitempty"        jsonschema:"the item this version is held as: on Emby every version is an item of its own, and this is the id to identify, edit or delete that file by"`
	Label    string `json:"label,omitempty"     jsonschema:"the server's name for the version"`
	Path     string `json:"path"`
	RuntimeS int    `json:"runtime_s,omitempty" jsonschema:"this file's own runtime in seconds"`
	qualityFacts
}

// Versions that are not the same film.
//
// A server makes files one item's versions by what it holds for them, not by
// what they are: Emby joins copies in other folders that carry the same
// provider id, so a file matched to another film's ids is shown as a version
// of that film, and every read of the item answers with its name. A caller
// comparing the versions then keeps the better copy and deletes a film. The
// path is the one thing the server did not write, so a version whose file or
// folder names another title, or a year more than one off, is probably
// another film. Runtimes far apart say the same more weakly - a director's cut
// runs longer too - so they strengthen a name that disagrees and never stand
// alone.

// knownTitles are the titles an item goes by (naming.KnownTitles): its name,
// original title and sort name.
func knownTitles(it *embyfin.Item) []naming.KnownTitle {
	return naming.KnownTitles(it.Name, it.OriginalTitle, it.SortName)
}

// heldTitles are the titles an item goes by (naming.KnownTitles), bare.
func heldTitles(it *embyfin.Item) []string {
	known := knownTitles(it)
	out := make([]string, 0, len(known))
	for _, k := range known {
		out = append(out, k.Title)
	}

	return out
}

// otherFilm says whether the file at path names a different film than the
// item it is held under: a title unlike every name the item goes by, or a
// year more than one off its own (a film released either side of new year is
// dated both ways). why says what the path names, "" when nothing disagrees.
// It reads the item's own titles alone; titleCheck.otherFilm asks TMDB too.
func otherFilm(it *embyfin.Item, path string) (why string, other bool) {
	why, _, other = filmClaim(it, path)

	return why, other
}

// filmClaim is otherFilm, with what the path claims.
func filmClaim(it *embyfin.Item, path string) (why string, c naming.Claim, other bool) {
	if path == "" || (it.Type != "" && it.Type != typeMovie) {
		return "", naming.Claim{}, false
	}
	c, ok := naming.ClaimOf(path, heldTitles(it))
	if !ok {
		return "", c, false
	}
	titleOff := c.Title != "" && c.Score < seriesConfident
	yearOff := it.ProductionYear > 0 && abs(c.Year-it.ProductionYear) > 1
	if !titleOff && !yearOff {
		return "", c, false
	}
	held := it.Name
	if it.ProductionYear > 0 {
		held = fmt.Sprintf("%s (%d)", it.Name, it.ProductionYear)
	}
	named := cmp.Or(c.Title, c.Segment)
	if titleOff {
		named = c.Named(heldTitles(it))
	}

	return fmt.Sprintf("%q is named for %q (%d), not %s", c.Segment, named, c.Year, held), c, true
}

// titleCheck reads a film's file against every title the film goes by, the
// way audit_file_path's title check reads its path: its name, original title
// and sort name, and with a TMDB token every title TMDB lists for its id and
// what TMDB's search finds by the file's title and year, a title of the film
// in one of TMDB's translations counting when the search answers with the
// film by it. The version and the duplicate warnings read files through it,
// so a file named for a film's title in another language is no warning of
// another film merged in. A nil check, or one with no token, reads the
// item's own titles alone.
type titleCheck struct {
	titles *tmdb.Facts
}

// newTitleCheck is a titleCheck asking TMDB with the configured token, when
// there is one.
func newTitleCheck(opts Options) *titleCheck {
	return &titleCheck{titles: newProviderTitles(opts)}
}

// titleSays is what a title check says of one file's or folder's name.
type titleSays int

const (
	// nothing disagrees, or nothing more can be told
	saysNothing titleSays = iota
	// another film or series: a year two or more off, or TMDB giving the
	// title to another
	saysAnother
	// a title the item does not go by, which may be one of its titles no
	// list holds (a translation)
	saysUnknown
	// a title TMDB lists as an entry of its own whose title begins with the
	// item's, which TMDB does for an edition ("Zzyzx: Ultimate Edition") as
	// for a sequel: an edition of the film, or another film
	saysOwnEntry
	// a title TMDB had to be asked about, and could not be
	saysUnchecked
	// the file's title is the film's and its year another, and TMDB's search
	// by the file's title and year finds this very film: the year the item
	// holds is the one to check, and the file no other film
	saysItemYear
	// the file's title is the film's and its year another, and nothing says
	// which is right: another film, or the item's year is wrong
	saysYearDoubt
)

// fileVerdict is a title check's say on one file or folder: what it names
// ("" when nothing is said), what that says of it, and TMDB failing to
// answer.
type fileVerdict struct {
	why  string
	says titleSays
	err  error
}

// sumVerdicts is what a warning says of verdicts: every file's or folder's
// say, whether any says another title for sure, whether any is a title the
// entry does not go by alone, and the first TMDB failure.
func sumVerdicts(verdicts []fileVerdict) (odd []string, sure, doubt bool, err error) {
	for _, v := range verdicts {
		if v.says == saysNothing {
			continue
		}
		if v.why != "" {
			odd = append(odd, v.why)
		}
		sure = sure || v.says == saysAnother
		doubt = doubt || v.says == saysUnknown
		if err == nil {
			err = v.err
		}
	}

	return odd, sure, doubt, err
}

// onlyItemYear says whether every file that says anything says only that
// the item's year is the one to check (saysItemYear): no other film at all.
func onlyItemYear(verdicts []fileVerdict) bool {
	said := false
	for _, v := range verdicts {
		switch v.says {
		case saysNothing:
		case saysItemYear:
			said = true
		default:
			return false
		}
	}

	return said
}

// otherFilm is otherFilm, clearing a file whose title TMDB knows the film by
// (knownToTMDB), asked by the claim's terms. A year two or more off is
// another film, and not asked about: TMDB speaks to the title, and the year
// is the item's or the file's to check. A title the film does not go by is
// another film when TMDB gives it to one - an entry of its own whose title
// begins with the film's may be an edition of it - and otherwise unknown.
//
// A file cleared by the words before its year alone - "Dune (2021) Part Two"
// held as Dune - is asked about too (wholeFilm), and TMDB failing to answer
// then leaves it unchecked rather than cleared. Without a token it is not
// asked about: a version's label and a title after the year cannot be told
// apart.
func (c *titleCheck) otherFilm(ctx context.Context, it *embyfin.Item, path string) fileVerdict {
	why, claim, other := filmClaim(it, path)
	id := providerID(it, "tmdb")
	yearOff := other && it.ProductionYear > 0 && abs(claim.Year-it.ProductionYear) > 1
	titleOff := claim.Title != "" && claim.Score < seriesConfident
	switch {
	case yearOff && titleOff:
		return fileVerdict{why: why, says: saysAnother}
	case yearOff:
		return c.yearVerdict(ctx, it, why, claim)
	case c == nil || c.titles == nil || id == "" || claim.Title == "":
		if other {
			return fileVerdict{why: why, says: saysUnknown}
		}

		return fileVerdict{}
	case !other:
		return c.wholeVerdict(ctx, it, path)
	}
	known, another, err := knownToTMDB(ctx, c.titles, "movie", id, claim.Terms(heldTitles(it)), claim.Year)
	switch {
	case err != nil:
		return fileVerdict{why: why, says: saysUnknown, err: err}
	case known:
		return fileVerdict{}
	case another == nil:
		return fileVerdict{why: why, says: saysUnknown}
	}
	h := *another
	kind, number, err := entryOf(ctx, c.titles, it, h)
	switch kind {
	case entryNumbered:
		why = fmt.Sprintf("%s: TMDB lists %d %s (%d), numbered %s, as a film of its own", why, h.ID, h.Title, h.Year, number)
	case entrySeries:
		why = fmt.Sprintf("%s: TMDB gives the title to %d %s (%d), another film of the series in TMDB's collection of %s", why, h.ID, h.Title, h.Year, it.Name)
	case entryEdition:
		return fileVerdict{why: fmt.Sprintf("%s, which TMDB lists as an entry of its own, %d %s (%d): an edition of this film, or another film", why, h.ID, h.Title, h.Year), says: saysOwnEntry, err: err}
	case entryAnother:
		why = fmt.Sprintf("%s: TMDB gives the title to its film %d %s (%d)", why, h.ID, h.Title, h.Year)
	}

	return fileVerdict{why: why, says: saysAnother}
}

// yearVerdict is the title check's say on a file whose title is the film's
// and whose year is two or more off: TMDB's search by the file's title and
// year, as audit_file_path asks it (searchPath), finding this very film says
// the year the item holds is the one to check; finding another film says it
// is probably that film. Anything else - no token, no answer either way, or
// TMDB failing - is another film, or the item's year wrong.
func (c *titleCheck) yearVerdict(ctx context.Context, it *embyfin.Item, why string, claim naming.Claim) fileVerdict {
	doubt := fileVerdict{why: why + ": another film, or the item's year is wrong", says: saysYearDoubt}
	id := providerID(it, "tmdb")
	if c == nil || c.titles == nil || id == "" || claim.Title == "" {
		return doubt
	}
	found, err := searchPath(ctx, c.titles, "movie", id, claim.Terms(heldTitles(it)), claim.Year)
	switch {
	case err != nil:
		doubt.err = err

		return doubt
	case found.named:
		return fileVerdict{why: fmt.Sprintf("%s: TMDB's search finds this very film, TMDB %s, by the path's title and year, so the year the item holds is the one to check", why, id), says: saysItemYear}
	case found.another != nil && homonymOf(it, *found.another):
		// another film of exactly the film's name, in the file's year: the
		// name can't tell the two apart, and audit_file_path's row says so
		// unless the file's runtime backs one (homonym)
		h := found.another
		doubt.why = fmt.Sprintf("%s: TMDB gives the path's title and year to its film %d %s (%d), another film of the same name - that film, or the item's year is wrong: can't tell which", why, h.ID, h.Title, h.Year)

		return doubt
	case found.another != nil:
		h := found.another

		return fileVerdict{why: fmt.Sprintf("%s: TMDB gives the path's title and year to its film %d %s (%d)", why, h.ID, h.Title, h.Year), says: saysAnother}
	}

	return doubt
}

// wholeVerdict is the title check's say on a file cleared by the words before
// its year alone (wholeClaim): what TMDB names by the words read whole
// (wholeFilm), and unchecked when TMDB could not be asked.
func (c *titleCheck) wholeVerdict(ctx context.Context, it *embyfin.Item, path string) fileVerdict {
	claim, ok := wholeClaim(it, path)
	if !ok {
		return fileVerdict{}
	}
	found, err := wholeFilm(ctx, c.titles, it, claim)
	switch {
	case found != nil:
		v := found.verdict(it, claim)
		v.err = err

		return v
	case err != nil:
		return fileVerdict{why: fmt.Sprintf("%q names %q read whole, and TMDB could not be asked whether that is another film", claim.Segment, claim.Whole), says: saysUnchecked, err: err}
	}

	return fileVerdict{}
}

// wholeClaim is a film's file whose title is left to read whole: words follow
// its (year), and the words before it are one of the film's own titles, so
// the file reads as the film by them alone - "Dune (2021) Part Two" as Dune.
// Its year is the film's or one either side; a file whose title or year
// disagrees is read by otherFilm instead. Words after the year that are an
// edition's alone ("Directors Cut", "Ultimate Edition") are none to ask
// about: the film's own title, its year and an edition's words can only be
// the film, or an edition TMDB lists apart.
func wholeClaim(it *embyfin.Item, path string) (naming.Claim, bool) {
	if path == "" || it.Type != typeMovie {
		return naming.Claim{}, false
	}
	held := heldTitles(it)
	claim, ok := naming.ClaimOf(path, held)
	if !ok || claim.Whole == "" || claim.Title == "" || claim.Score < seriesConfident || !naming.Like(claim.Title, claim.Before) ||
		!slices.ContainsFunc(held, func(h string) bool { return naming.Like(claim.Before, h) }) ||
		it.ProductionYear > 0 && abs(claim.Year-it.ProductionYear) > 1 ||
		editionRest(strings.Fields(naming.Normalise(claim.After()))) {
		return naming.Claim{}, false
	}

	return claim, true
}

// entryKind is what an entry of TMDB's of another id, named by a film's
// file, is to the film.
type entryKind int

const (
	// another film: its title does not begin with the film's, or goes on
	// past it in words of a title ("Up in the Air" beside Up)
	entryAnother entryKind = iota
	// a film of its own whose title is the film's and a number alone:
	// "Zzyzx: Part Two", "Zzyzx 2", "Zzyzx II"
	entryNumbered
	// another film of the film's TMDB collection: its series
	entrySeries
	// an entry whose title is the film's and an edition's words alone: an
	// edition TMDB lists apart ("Zzyzx: Ultimate Edition"), or another film
	entryEdition
)

// entryOf says what TMDB's entry h, of another id, is to the film it. Its
// title read past the film's (titleRest) says most: a number alone makes it a
// numbered film of its own, and edition's words alone an entry that may be
// an edition; any other words - "Up in the Air" beside Up - make it another
// film, of the film's series when TMDB's collection of it holds the entry.
// The collection is asked only then, for the wording, and err is TMDB
// failing to answer it, which leaves it another film all the same.
func entryOf(ctx context.Context, titles *tmdb.Facts, it *embyfin.Item, h tmdb.Hit) (kind entryKind, number string, err error) {
	rest, ok := titleRest(it, h)
	if !ok {
		return entryAnother, "", nil
	}
	if n := restNumber(rest); n != "" {
		return entryNumbered, n, nil
	}
	if editionRest(rest) {
		return entryEdition, "", nil
	}
	parts, err := titles.CollectionParts(ctx, providerID(it, "tmdb"))
	if err != nil {
		return entryAnother, "", err
	}
	if slices.ContainsFunc(parts, func(p tmdb.Hit) bool { return p.ID == h.ID }) {
		return entrySeries, "", nil
	}

	return entryAnother, "", nil
}

// wholeFinding is a film TMDB names by a file's title read whole, found by
// its search or in the film's own collection, and what it is to the film.
type wholeFinding struct {
	hit    tmdb.Hit
	kind   entryKind
	number string // an entryNumbered's number
	// found in the film's collection by the words after the year
	collection bool
}

// wholeFilm is the film of another id TMDB names by a file's title read whole
// (wholeClaim), nil when it names none. The whole title is searched in no
// year - the year filter hides "Dune: Part Two" (2024) when asked for 2021 -
// and a film of another id whose title is like it is TMDB naming another
// film (entryOf says what it is to the film). Failing that, the words after
// the year are held against the films of the film's own TMDB collection:
// "Alien (1979) - Aliens" names Aliens, where the search for "Alien Aliens"
// finds nothing. A version's label ("Final Cut", "Criterion") names neither,
// and is no finding. Each is asked once. err is TMDB failing to answer; a
// finding beside it is what could be told without the answer.
func wholeFilm(ctx context.Context, titles *tmdb.Facts, it *embyfin.Item, claim naming.Claim) (*wholeFinding, error) {
	id := providerID(it, "tmdb")
	hits, err := titles.Search(ctx, "movie", claim.Whole, 0)
	if err != nil {
		return nil, err
	}
	for _, h := range hits {
		if strconv.Itoa(h.ID) != id && (naming.Like(claim.Whole, h.Title) || naming.Like(claim.Whole, h.Original)) {
			kind, number, kerr := entryOf(ctx, titles, it, h)

			return &wholeFinding{hit: h, kind: kind, number: number}, kerr
		}
	}
	after := claim.After()
	if id == "" || !strings.ContainsFunc(after, unicode.IsLetter) {
		return nil, nil
	}
	parts, err := titles.CollectionParts(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, h := range parts {
		if strconv.Itoa(h.ID) == id {
			continue
		}
		for _, t := range []string{h.Title, h.Original} {
			if naming.Like(after, t) || naming.Like(claim.Whole, t) {
				return &wholeFinding{hit: h, kind: entrySeries, collection: true}, nil
			}
		}
	}

	return nil, nil
}

// verdict is what a wholeFinding says of the file: another film - outright,
// numbered as a film of its own, or of the film's series - or an entry TMDB
// lists of its own that may be an edition of this one.
func (f *wholeFinding) verdict(it *embyfin.Item, claim naming.Claim) fileVerdict {
	h := f.hit
	named := claim.Whole
	if f.collection {
		named = claim.After()
	}
	var why string
	switch f.kind {
	case entryNumbered:
		why = fmt.Sprintf("%q names %q: TMDB lists %d %s (%d), numbered %s, as a film of its own, not %s", claim.Segment, named, h.ID, h.Title, h.Year, f.number, it.Name)
	case entrySeries:
		why = fmt.Sprintf("%q names %q, TMDB's film %d %s (%d), another film of the series in TMDB's collection of %s", claim.Segment, named, h.ID, h.Title, h.Year, it.Name)
	case entryEdition:
		return fileVerdict{why: fmt.Sprintf("%q names %q, which TMDB lists as an entry of its own, %d %s (%d): an edition of this film, or another film", claim.Segment, named, h.ID, h.Title, h.Year), says: saysOwnEntry}
	case entryAnother:
		why = fmt.Sprintf("%q names %q, TMDB's film %d %s (%d), not %s", claim.Segment, named, h.ID, h.Title, h.Year, it.Name)
	}

	return fileVerdict{why: why, says: saysAnother}
}

// titleRest is a film's title read past one the item goes by, as folded
// words - "part two" of "Zzyzx: Part Two" beside Zzyzx - and whether its
// title begins with one at all. TMDB lists some editions as entries of their
// own, so an entry beginning with the film's title is read by what follows.
func titleRest(it *embyfin.Item, h tmdb.Hit) ([]string, bool) {
	for _, held := range heldTitles(it) {
		n := naming.Normalise(held)
		if n == "" {
			continue
		}
		for _, t := range []string{h.Title, h.Original} {
			if rest, ok := strings.CutPrefix(naming.Normalise(t), n+" "); ok {
				return strings.Fields(rest), true
			}
		}
	}

	return nil, false
}

// entryNumber is the number a film's title carries past the item's own (see
// restNumber), "" for none or a title that does not begin with the item's.
func entryNumber(it *embyfin.Item, h tmdb.Hit) string {
	rest, _ := titleRest(it, h)

	return restNumber(rest)
}

// restNumber is the number a title's words past the item's own are, when
// they are a number alone, as it reads: "Two" of "Part Two", "2", "II",
// "Two" of "Chapter Two", "3" of "Vol. 3". Digits (never four, which are a
// year: "2049"), a Roman numeral, or a number's word, from two to twelve
// (sequelNumber); one, I, V and X only after a word that numbers, as alone
// they are a pronoun, a letter or "versus". A word or a numeral reads as the
// title check reads it (naming.NumberOf): a numeral no higher than romanMax. Any
// word beside the number - "Two Towers", "Six Feet Under" - and it is no
// number of the film's but a title.
func restNumber(rest []string) string {
	marked := len(rest) == 2 && slices.Contains([]string{"part", "chapter", "episode", "volume", "vol", "book"}, rest[0])
	if marked {
		rest = rest[1:]
	}
	if len(rest) != 1 || rest[0] == "" {
		return ""
	}
	w := rest[0]
	if strings.Trim(w, "0123456789") == "" {
		if len(w) != 4 && (marked || sequelNumber(w)) {
			return w
		}

		return ""
	}
	if d, ok := naming.NumberWord(w); ok {
		if sequelNumber(d) || d == "1" && marked {
			return strings.ToUpper(w[:1]) + w[1:]
		}

		return ""
	}
	if naming.IsRomanPart(w) && (len(w) > 1 || marked) {
		if _, ok := naming.NumberOf(w, true); ok {
			return strings.ToUpper(w)
		}
	}

	return ""
}

// sequelNumber says whether digits are a number a sequel is called by, two
// to twelve: "Apollo 13" beside Apollo is a title, not its thirteenth film.
func sequelNumber(digits string) bool {
	n, err := strconv.Atoi(digits)

	return err == nil && n >= 2 && n <= 12
}

// editionRest says whether a title's words past the item's own are an
// edition's alone: "Ultimate Edition", "The Director's Cut", "25th
// Anniversary Edition", "Final Cut".
func editionRest(rest []string) bool {
	return len(rest) > 0 && !slices.ContainsFunc(rest, func(w string) bool {
		return !editionRestWords[w] && !ordinal.MatchString(w)
	})
}

// editionRestWords are the words an edition's name is made of, after a
// film's title: "Ultimate Edition", "The Director's Cut", "Final Cut".
var editionRestWords = map[string]bool{
	"edition": true, "cut": true, "version": true, "extended": true, "ultimate": true, "special": true,
	"director": true, "directors": true, "theatrical": true, "unrated": true, "uncut": true, "remastered": true,
	"restored": true, "anniversary": true, "imax": true, "redux": true, "final": true, "criterion": true, "the": true,
}

// ordinal is an ordinal number in figures: 25th, 2nd.
var ordinal = regexp.MustCompile(`^\d+(?:st|nd|rd|th)$`)

// otherSeries is seriesClaim's say on a series' folder, clearing one whose
// title TMDB knows the series by - a romanised or translated title of an
// anime ("Shingeki no Kyojin" beside "Attack on Titan") is one it lists -
// asked by the claim's terms. Another series when the folder's year is two
// or more off, or TMDB gives the title to another series; a title the series
// does not go by alone is unknown, as it could be one of its titles no list
// holds.
func (c *titleCheck) otherSeries(ctx context.Context, it *embyfin.Item) fileVerdict {
	why, claim, other := seriesClaim(it)
	id := providerID(it, "tmdb")
	switch {
	case !other:
		return fileVerdict{}
	case it.ProductionYear > 0 && abs(claim.Year-it.ProductionYear) > 1:
		return fileVerdict{why: why, says: saysAnother}
	case c == nil || c.titles == nil || id == "" || claim.Title == "":
		return fileVerdict{why: why, says: saysUnknown}
	}
	known, another, err := knownToTMDB(ctx, c.titles, "tv", id, claim.Terms(heldTitles(it)), claim.Year)
	switch {
	case err != nil:
		return fileVerdict{why: why, says: saysUnknown, err: err}
	case known:
		return fileVerdict{}
	case another != nil:
		return fileVerdict{why: fmt.Sprintf("%s: TMDB gives the title to its series %d %s (%d)", why, another.ID, another.Title, another.Year), says: saysAnother}
	}

	return fileVerdict{why: why, says: saysUnknown}
}

// knownToTMDB says whether TMDB knows the film or the series of id by one of
// terms: a title it lists for the id, or its search answering with the id
// under one of them (searchPath). another is a film or series of another id
// the search gives the term to, nil when none.
func knownToTMDB(ctx context.Context, titles *tmdb.Facts, kind, id string, terms []string, year int) (known bool, another *tmdb.Hit, err error) {
	alts, err := titles.AlternativeTitles(ctx, kind, id)
	if err != nil {
		return false, nil, err
	}
	for i, term := range terms {
		if slices.ContainsFunc(alts, func(alt string) bool { return termLike(i, term, alt) }) {
			return true, nil, nil
		}
	}
	found, err := searchPath(ctx, titles, kind, id, terms, year)
	if err != nil || found.named {
		return found.named, nil, err
	}

	return false, found.another, nil
}

// pathSearch is what TMDB's search makes of a path's title and year.
type pathSearch struct {
	// the term the search answered, of those asked
	term string
	// the item's own id among the answers, in the path's year or one either
	// side
	own *tmdb.Hit
	// the own answer is under a title like the term, or TMDB lists the term
	// for the item or titles it so in one of its translations
	named bool
	// a film or series of another id the answers give the term to, when the
	// own answer is not named
	another *tmdb.Hit
	// what the search answered for the term, for a caller to read further
	// (another part of a film, a title numbered otherwise)
	hits []tmdb.Hit
}

// searchPath asks TMDB's search for a path's terms in turn (naming.Claim.terms),
// in year when given, until one answers: audit_file_path's diagnosis and the
// title check's read of a file's year both ask it, so the two never tell one
// file two ways. The item's own id answering settles the path as the item's
// only under a title like the term (a later term held to the same title,
// termLike), one TMDB lists for it, or its title in one of TMDB's
// translations (knownByTranslation): the search matches loosely, and by a
// word a dozen films begin with it finds the one the item is matched to.
func searchPath(ctx context.Context, titles *tmdb.Facts, kind, own string, terms []string, year int) (pathSearch, error) {
	var hits []tmdb.Hit
	at := 0
	for i, term := range terms {
		found, err := titles.Search(ctx, kind, term, year)
		if err != nil {
			return pathSearch{}, err
		}
		if hits, at = found, i; len(hits) > 0 {
			break
		}
	}
	out := pathSearch{term: terms[at], hits: hits}
	like := func(t string) bool { return termLike(at, out.term, t) }
	if i := slices.IndexFunc(hits, func(h tmdb.Hit) bool {
		return own != "" && strconv.Itoa(h.ID) == own && (year == 0 || h.Year == 0 || abs(h.Year-year) <= 1)
	}); i >= 0 {
		out.own = &hits[i]
		out.named = ownHitBy(hits[i], own, year, like)
		if !out.named {
			alts, err := titles.AlternativeTitles(ctx, kind, own)
			if err != nil {
				return pathSearch{}, err
			}
			out.named = slices.ContainsFunc(alts, like)
		}
		if !out.named {
			translated, err := knownByTranslation(ctx, titles, kind, own, year, hits, like)
			if err != nil {
				return pathSearch{}, err
			}
			out.named = translated
		}
	}
	if hit, found := bestHit(hits, out.term, year); !out.named && found && strconv.Itoa(hit.ID) != own && (like(hit.Title) || like(hit.Original)) {
		out.another = &hit
	}

	return out, nil
}

// termLike is naming.Like for the first of a claim's terms (naming.Claim.terms),
// and for a later one - the words before the year, asked after the whole
// title - nothing short of the same title, its article aside: by the prefix
// rule a word a dozen films begin with is like each of them ("Alien" and
// "Alien 2" score 0.91), and a film matched wrong to one of them would be
// cleared by it.
func termLike(i int, term, title string) bool {
	if i == 0 {
		return naming.Like(term, title)
	}
	score, _ := naming.Score(term, title)

	return naming.Like(term, title) && score >= 0.97
}

// knownByTranslation says whether TMDB's search, answering hits, answered
// with the film or the series of id - in year, when given - under a title
// unlike the one asked because that one is its title in one of TMDB's
// translations of it (like says which are): "La llegada" finds Arrival, and
// Arrival's Spanish translation is titled La llegada, which its alternative
// titles leave out. The translations are asked only when the search answers
// with the id.
func knownByTranslation(ctx context.Context, titles *tmdb.Facts, kind, id string, year int, hits []tmdb.Hit, like func(string) bool) (bool, error) {
	if !slices.ContainsFunc(hits, func(h tmdb.Hit) bool {
		return strconv.Itoa(h.ID) == id && (year == 0 || h.Year == 0 || abs(h.Year-year) <= 1)
	}) {
		return false, nil
	}
	translated, err := titles.Translations(ctx, kind, id)
	if err != nil {
		return false, err
	}

	return slices.ContainsFunc(translated, like), nil
}

// ownHitBy says whether a search answer is the film or series of id under a
// title like says is the one asked, in the year asked or one either side.
func ownHitBy(h tmdb.Hit, id string, year int, like func(string) bool) bool {
	return strconv.Itoa(h.ID) == id && (year == 0 || h.Year == 0 || abs(h.Year-year) <= 1) && (like(h.Title) || like(h.Original))
}

// unasked is what a warning adds when TMDB could not be asked about a file's
// title: the warning stands on what could be read without it.
func unasked(err error) string {
	if err == nil {
		return ""
	}

	return fmt.Sprintf(" (TMDB could not be asked: %v)", err)
}

// runtimesApart says whether two files run far enough apart to be two films
// rather than two encodes of one: more than a tenth of the longer and more
// than a minute. A director's cut does too, which is why it is only a tell.
func runtimesApart(a, b int64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	longer, gap := max(a, b), a-b
	if gap < 0 {
		gap = -gap
	}

	return gap*10 > longer && gap > 60*ticksPerSecond
}

// versionWarning is what to say about an item's versions when one of them may
// be another film, "" when none is: which file names what, and how far the
// runtimes are apart when they are. "probably not one film" when a year or
// TMDB says another film; "may not" when a file only names a title the film
// does not go by, one TMDB lists as an entry of its own (an edition, or
// another film), or one TMDB could not be asked about. A single version is
// judged the same way, because Jellyfin keeps a film matched to another's ids
// as a separate item rather than merging it. Files are read as check reads
// them.
func versionWarning(ctx context.Context, check *titleCheck, it *embyfin.Item) string {
	var verdicts []fileVerdict
	for i := range it.MediaSources {
		verdicts = append(verdicts, check.otherFilm(ctx, it, it.MediaSources[i].Path))
	}
	if len(it.MediaSources) == 0 {
		verdicts = append(verdicts, check.otherFilm(ctx, it, it.Path))
	}
	odd, sure, doubt, tmdbErr := sumVerdicts(verdicts)
	if len(odd) == 0 {
		return ""
	}
	if onlyItemYear(verdicts) {
		return fmt.Sprintf("the year the film holds is the one to check, not its file: %s%s", strings.Join(odd, "; "), unasked(tmdbErr))
	}

	apart := ""
	for i := range it.MediaSources {
		for j := i + 1; j < len(it.MediaSources); j++ {
			a, b := it.MediaSources[i].RunTimeTicks, it.MediaSources[j].RunTimeTicks
			if apart == "" && runtimesApart(a, b) {
				apart = fmt.Sprintf(", and its versions run %s and %s", runtimeText(a), runtimeText(b))
			}
		}
	}
	switch {
	case len(it.MediaSources) > 1 && sure:
		return fmt.Sprintf("probably not one film: %s%s. A file matched to this film's ids is shown as a version of it; compare the files before keeping one over another, and identify the wrong one (item_identify on the version's own id) to split them%s", strings.Join(odd, "; "), apart, unasked(tmdbErr))
	case len(it.MediaSources) > 1:
		rest := "Compare the files before keeping one over another"
		if doubt {
			rest = titleDoubt("The file", "film", check.asks([]embyfin.Item{*it}), tmdbErr) + ": a translation or other title of this film, or another film merged in - compare the files before keeping one over another"
		}

		return fmt.Sprintf("may not be one film: %s%s. %s%s", strings.Join(odd, "; "), apart, rest, unasked(tmdbErr))
	case sure:
		return fmt.Sprintf("probably a different film matched to this one's ids: %s. Compare the file before treating it as a copy of this film, and identify it (item_identify) if it is another%s", strings.Join(odd, "; "), unasked(tmdbErr))
	}

	return fmt.Sprintf("may be a different film matched to this one's ids: %s. Compare the file before treating it as a copy of this film%s", strings.Join(odd, "; "), unasked(tmdbErr))
}

// asks says whether the check asks TMDB about every one of items: with a
// token, of an item carrying a TMDB id.
func (c *titleCheck) asks(items []embyfin.Item) bool {
	if c == nil || c.titles == nil {
		return false
	}

	return !slices.ContainsFunc(items, func(it embyfin.Item) bool { return providerID(&it, "tmdb") == "" })
}

// titleDoubt says why a title an entry does not go by leaves it in doubt,
// rather than another title for sure: what was asked of it, and what was not.
// who names what holds the title ("The file"), and kind is film or series.
func titleDoubt(who, kind string, asked bool, err error) string {
	switch {
	case err != nil:
		// unasked says why TMDB was not asked
		return fmt.Sprintf("%s names a title the %s does not go by here", who, kind)
	case asked:
		return fmt.Sprintf("%s names a title the %s goes by neither here nor among the titles TMDB lists for it, TMDB's search finding it by none, and gives to no other %s", who, kind, kind)
	}

	return fmt.Sprintf("%s names a title the %s does not go by here, and TMDB was not asked what else it is called (EMBYFIN_TMDB_TOKEN unset, or no TMDB id)", who, kind)
}

// runtimeText is a runtime in ticks as a person reads it.
func runtimeText(ticks int64) string {
	seconds := ticks / ticksPerSecond
	if seconds < 120 {
		return fmt.Sprintf("%ds", seconds)
	}

	return fmt.Sprintf("%d min", seconds/60)
}

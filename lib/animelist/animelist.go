// Package animelist reads Anime-Lists (github.com/Anime-Lists/anime-lists),
// the community mapping of every AniDB entry to where TVDB and TMDB hold it.
//
// AniDB gives an OVA, a film or a special an entry of its own. TVDB and TMDB
// often hold the same thing as the specials of another series, and the list
// records both, which is what lets a library tell a thing that belongs in a
// series' specials from one that was folded in there.
package animelist

import (
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultSource is the list as its maintainers publish it.
const DefaultSource = "https://raw.githubusercontent.com/Anime-Lists/anime-lists/master/anime-list-master.xml"

// Entry is one AniDB entry and where TVDB and TMDB hold it.
type Entry struct {
	AniDB string
	// Name is AniDB's main title, usually romanised Japanese.
	Name string
	// TVDB and TMDBTV are the series holding it, and TVDBSeason and
	// TMDBSeason the season it sits in: "0" is the specials. TVDB is empty
	// where TVDB has no series for it; the list writes movie, OVA or hentai
	// there instead of an id.
	TVDB, TVDBSeason   string
	TMDBTV, TMDBSeason string
	// TVDBSpecials and TMDBSpecials are the specials it is, by number, where
	// the list maps its episodes one by one. Otherwise its first episode is
	// the special after TVDBOffset or TMDBOffset, and the list does not say
	// where it ends.
	TVDBSpecials, TMDBSpecials []int
	TVDBOffset, TMDBOffset     int
	TMDBMovie, IMDB            string

	// tmdbPlaced is whether the list says where among TMDB's specials the
	// entry sits. An empty TVDB offset has always meant none; the TMDB
	// columns are newer, and many entries name the series without a place.
	tmdbPlaced bool
}

// Specials is the series each provider holds the entry in as its specials:
// "" for a provider that gives it a place of its own, or does not hold it.
func (e *Entry) Specials() (tvdb, tmdb string) {
	if e.TVDBSeason == "0" {
		tvdb = e.TVDB
	}
	if e.TMDBSeason == "0" {
		tmdb = e.TMDBTV
	}

	return tvdb, tmdb
}

// Place is where the entry sits among a provider's specials, provider tvdb
// or tmdb: the first special it is, and all of them where the list maps its
// episodes one by one (nil where it gives only the first). known is false
// where the list does not say.
func (e *Entry) Place(provider string) (first int, numbers []int, known bool) {
	numbers, offset, known := e.TMDBSpecials, e.TMDBOffset, e.tmdbPlaced
	if provider == "tvdb" {
		numbers, offset, known = e.TVDBSpecials, e.TVDBOffset, true
	}
	if len(numbers) > 0 {
		return slices.Min(numbers), numbers, true
	}

	return offset + 1, nil, known
}

// List is the mapping, indexed by AniDB entry and by the series whose
// specials hold entries of their own.
type List struct {
	entries  map[string]*Entry
	specials map[string][]*Entry
	// claimed are the specials, by series, the list gives by number: a
	// show's own specials and the entries it maps one by one
	claimed map[string]map[int]bool
}

// Len is how many AniDB entries the list maps.
func (l *List) Len() int { return len(l.entries) }

// Entry is the list's entry for an AniDB id, or nil.
func (l *List) Entry(anidb string) *Entry { return l.entries[anidb] }

// SpecialsIn is the entries a provider's series holds as specials, provider
// tvdb or tmdb, in the order they sit there.
func (l *List) SpecialsIn(provider, id string) []*Entry { return l.specials[provider+":"+id] }

// Claimed says whether the list gives special n of a provider's series to
// something by number: one of the show's own specials, or an entry it maps
// one by one. An entry the list places only by where it starts does not
// reach past one of these.
func (l *List) Claimed(provider, id string, n int) bool { return l.claimed[provider+":"+id][n] }

func (l *List) claim(key string, numbers []int) {
	if len(numbers) == 0 {
		return
	}
	if l.claimed[key] == nil {
		l.claimed[key] = map[int]bool{}
	}
	for _, n := range numbers {
		l.claimed[key][n] = true
	}
}

type xmlMapping struct {
	AniDBSeason string `xml:"anidbseason,attr"`
	TVDBSeason  string `xml:"tvdbseason,attr"`
	TMDBSeason  string `xml:"tmdbseason,attr"`
	Start       string `xml:"start,attr"`
	End         string `xml:"end,attr"`
	Offset      string `xml:"offset,attr"`
	Pairs       string `xml:",chardata"`
}

type xmlAnime struct {
	AniDB      string       `xml:"anidbid,attr"`
	TVDB       string       `xml:"tvdbid,attr"`
	TVDBSeason string       `xml:"defaulttvdbseason,attr"`
	TVDBOffset string       `xml:"episodeoffset,attr"`
	TMDBTV     string       `xml:"tmdbtv,attr"`
	TMDBSeason string       `xml:"tmdbseason,attr"`
	TMDBOffset string       `xml:"tmdboffset,attr"`
	TMDBMovie  string       `xml:"tmdbid,attr"`
	IMDB       string       `xml:"imdbid,attr"`
	Name       string       `xml:"name"`
	Mappings   []xmlMapping `xml:"mapping-list>mapping"`
}

// episodes are the provider's episode numbers a mapping names: pairs like
// ;1-5;2-6+7; (an episode can be two of theirs), or a start and end with an
// offset. A 0 is an episode the provider does not have.
func (m xmlMapping) episodes() []int {
	var out []int
	if m.Start != "" {
		start, end, offset := number(m.Start), number(m.End), number(m.Offset)
		for ep := max(start, 1); ep <= end; ep++ {
			if n := ep + offset; n > 0 {
				out = append(out, n)
			}
		}
	}
	for pair := range strings.SplitSeq(m.Pairs, ";") {
		_, theirs, ok := strings.Cut(strings.TrimSpace(pair), "-")
		if !ok {
			continue
		}
		for ep := range strings.SplitSeq(theirs, "+") {
			if n := number(ep); n > 0 {
				out = append(out, n)
			}
		}
	}

	return out
}

func number(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))

	return n
}

// id is s when it is a provider id; the list writes words like movie or OVA
// where a provider has none.
func id(s string) string {
	if _, err := strconv.Atoi(s); err != nil {
		return ""
	}

	return s
}

// Parse reads the list.
func Parse(r io.Reader) (*List, error) {
	var raw struct {
		Anime []xmlAnime `xml:"anime"`
	}
	if err := xml.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("reading the anime list: %w", err)
	}

	l := &List{entries: make(map[string]*Entry, len(raw.Anime)), specials: map[string][]*Entry{}, claimed: map[string]map[int]bool{}}
	for _, a := range raw.Anime {
		if id(a.AniDB) == "" {
			continue
		}
		e := &Entry{
			AniDB: a.AniDB, Name: strings.TrimSpace(a.Name),
			TVDB: id(a.TVDB), TVDBSeason: a.TVDBSeason, TVDBOffset: number(a.TVDBOffset),
			TMDBTV: id(a.TMDBTV), TMDBSeason: a.TMDBSeason, TMDBOffset: number(a.TMDBOffset),
			TMDBMovie: id(a.TMDBMovie), IMDB: a.IMDB,
			tmdbPlaced: strings.TrimSpace(a.TMDBOffset) != "",
		}
		for _, m := range a.Mappings {
			// AniDB season 1 is the entry's episodes, which say where it
			// sits; season 0 its own specials, which only take numbers
			switch {
			case m.AniDBSeason == "1" && m.TVDBSeason == "0":
				e.TVDBSpecials = append(e.TVDBSpecials, m.episodes()...)
			case m.AniDBSeason == "1" && m.TMDBSeason == "0":
				e.TMDBSpecials = append(e.TMDBSpecials, m.episodes()...)
			case m.AniDBSeason == "0" && m.TVDBSeason == "0" && e.TVDB != "":
				l.claim("tvdb:"+e.TVDB, m.episodes())
			case m.AniDBSeason == "0" && m.TMDBSeason == "0" && e.TMDBTV != "":
				l.claim("tmdb:"+e.TMDBTV, m.episodes())
			}
		}
		l.entries[e.AniDB] = e

		tvdb, tmdb := e.Specials()
		if tvdb != "" {
			l.specials["tvdb:"+tvdb] = append(l.specials["tvdb:"+tvdb], e)
			l.claim("tvdb:"+tvdb, e.TVDBSpecials)
		}
		if tmdb != "" {
			l.specials["tmdb:"+tmdb] = append(l.specials["tmdb:"+tmdb], e)
			l.claim("tmdb:"+tmdb, e.TMDBSpecials)
		}
	}
	for key, entries := range l.specials {
		provider, _, _ := strings.Cut(key, ":")
		// an entry with no known place goes last, where no neighbour
		// takes its first special for an end
		rank := func(e *Entry) int {
			first, _, known := e.Place(provider)
			if !known {
				return math.MaxInt
			}

			return first
		}
		slices.SortFunc(entries, func(a, b *Entry) int {
			return cmp.Or(cmp.Compare(rank(a), rank(b)), strings.Compare(a.AniDB, b.AniDB))
		})
	}

	return l, nil
}

// Loader reads the list from a URL or a file, and keeps what it read for a
// day: the list changes a few times a week and runs to megabytes.
type Loader struct {
	source string
	http   *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu   sync.Mutex
	list *List
	at   time.Time
}

// NewLoader reads from source, a URL or a file path; "" is DefaultSource. A
// URL is fetched through rt, nil for the default transport.
func NewLoader(source string, rt http.RoundTripper) *Loader {
	return &Loader{
		source: cmp.Or(source, DefaultSource),
		http:   &http.Client{Timeout: time.Minute, Transport: rt},
		ttl:    24 * time.Hour,
		now:    time.Now,
	}
}

// Source is where the list is read from.
func (l *Loader) Source() string { return l.source }

// Load is the list, read again once what it holds is a day old. A list that
// cannot be read again gives way to the one read before: the mapping
// changes slowly, and an old copy answers better than an error.
func (l *Loader) Load(ctx context.Context) (*List, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.list != nil && l.now().Sub(l.at) < l.ttl {
		return l.list, nil
	}
	list, err := l.read(ctx)
	switch {
	case err == nil:
		l.list, l.at = list, l.now()
	case l.list == nil:
		return nil, err
	}

	return l.list, nil
}

func (l *Loader) read(ctx context.Context) (*List, error) {
	if !strings.HasPrefix(l.source, "http://") && !strings.HasPrefix(l.source, "https://") {
		f, err := os.Open(l.source)
		if err != nil {
			return nil, fmt.Errorf("reading the anime list: %w", err)
		}
		defer func() { _ = f.Close() }()

		return Parse(f)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.source, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching the anime list from %s: %w", l.source, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching the anime list from %s: %s", l.source, resp.Status)
	}

	return Parse(resp.Body)
}

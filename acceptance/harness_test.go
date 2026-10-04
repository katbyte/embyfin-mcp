//go:build integration

// The harness: scripts/testenv.sh brings the container up and exports
// EMBYFIN_BACKEND, EMBYFIN_SERVER, EMBYFIN_TOKEN and EMBYFIN_TEST_*, and
// everything here drives it through the MCP tools rather than the HTTP API,
// so building the fixtures is itself a test of library_create, library_scan,
// item_edit and the rest.
package acceptance

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acc "github.com/katbyte/embyfin-mcp/lib/acceptance"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/testenv"
	"github.com/katbyte/embyfin-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// libraryFixture is one of the seeded libraries. The catalogue is shaped so
// every audit has both something to find and something it must leave alone.
type libraryFixture struct {
	Name, Type, Folder string
	// Providers leaves the server's metadata fetchers on, so the scan fills
	// the library from TMDB and TheTVDB through the provider proxy. The messy
	// libraries keep them off, so what the nfo sidecars say is what the
	// server knows and the defects survive the scan.
	Providers bool
	// Items is the count of the library's primary type the scan settles on:
	// movies in a movie library, series in a show library. The recursive
	// total is not used because the servers count differently (Jellyfin adds
	// the folder itself, a provider adds virtual episodes). It is a function
	// because the two servers do not agree on what a folder holds: see
	// messyMovies.
	Items func() int
	// Episodes is the number of episodes a show library's scan settles on:
	// its episode files, and on Emby the extras it takes for episodes.
	Episodes func() int
}

// libraries are created by library_create and filled by library_scan. The
// folders are the container-side paths scripts/testenv.sh lays out.
var libraries = []libraryFixture{
	{Name: "Movies", Type: "movies", Folder: "/media/movies", Providers: true, Items: func() int { return len(movies) }},
	{Name: "Shows", Type: "tvshows", Folder: "/media/shows", Providers: true, Items: func() int { return len(shows) }, Episodes: showEpisodes},
	{Name: "Messy Movies", Type: "movies", Folder: "/media/messy-movies", Items: messyMovies},
	{Name: "Messy Shows", Type: "tvshows", Folder: "/media/messy-shows", Items: func() int { return messySeries }, Episodes: messyEpisodes},
	{Name: "Music", Type: "music", Folder: "/media/music", Items: musicAlbums},
}

// messyMovies is how many films the messy library stores: thirteen folders, but
// Jellyfin folds the two Blade Runner files into one entry with two versions
// while Emby keeps them as two items. Emby does merge them - by file name,
// and the two Aliens by their shared TMDB id - but only in what it shows
// people, which the versions and duplicates audits read
// (TestAuditMultipleVersions); every other count is of what it stores.
// Interstellar's trailers and extras are no film on either.
func messyMovies() int {
	if isJellyfin() {
		return 13
	}

	return 14
}

// The messy show library: Severance, Star Trek The Next Generation, Star Trek:
// Deep Space Nine, Andor, the A Knight of the Seven Kingdoms pair,
// .hack//Liminality, .hack//SIGN, The Wire split across two folders, the
// Asterix & Obelix series holding a film's ids and Red Dwarf from its third
// season, holding thirty episode files between them (scripts/testenv.sh
// says what is wrong with each).
const (
	messySeries       = 12
	messyEpisodeFiles = 30
)

// messyEpisodes is how many episodes the messy show library stores: its
// episode files, and on Emby the featurette in the messy Severance's
// season Extras folder, which Emby takes for an episode with no number and
// Jellyfin keeps as the season's extra. The audits that judge episodes leave
// it out (messyEpisodeFiles); the rest read what the server stores.
func messyEpisodes() int {
	if isJellyfin() {
		return messyEpisodeFiles
	}

	return messyEpisodeFiles + 1
}

// messyEpisodesShown is how many messy episodes the audits that read the
// library as people are shown it read: what the server stores, but on Emby
// the two copies of The Wire's second episode, one in each of the show's
// folders, are one episode's two versions.
func messyEpisodesShown() int {
	if isJellyfin() {
		return messyEpisodes()
	}

	return messyEpisodes() - 1
}

// messyEpisodesJudged is how many of those the audits that judge an
// episode's file judge: Emby's featurette, which it takes for an episode, is
// no episode to replace or to hold to a season's.
func messyEpisodesJudged() int {
	if isJellyfin() {
		return messyEpisodesShown()
	}

	return messyEpisodesShown() - 1
}

// showEpisodes is how many episode files the clean show library holds.
func showEpisodes() int {
	n := 0
	for _, s := range shows {
		for _, eps := range s.Episodes {
			n += len(eps)
		}
	}

	return n
}

// versionsMerged reports whether the server stores the two Blade Runner
// files as one entry, so a sweep of its items sees one film. Emby stores two
// and merges them only in what it shows people (audit_multiple_versions).
func versionsMerged() bool { return isJellyfin() }

// movieFixture is a film in the clean Movies library, as its nfo describes it.
// The nfo's runtime is left out: the file's own is what both servers hold, a
// second for every film but Limitless, whose file runs its real 106 minutes.
type movieFixture struct {
	Title    string
	Year     int
	TMDB     string
	IMDB     string
	Genre    string
	Director string
}

var movies = []movieFixture{
	{"Alien", 1979, "348", "tt0078748", "Horror", "Ridley Scott"},
	{"Aliens", 1986, "679", "tt0090605", "Action", "James Cameron"},
	{"Blade Runner", 1982, "78", "tt0083658", "Science Fiction", "Ridley Scott"},
	{"Dune", 2021, "438631", "tt1160419", "Science Fiction", "Denis Villeneuve"},
	{"Dune: Part Two", 2024, "693134", "tt15239678", "Science Fiction", "Denis Villeneuve"},
	{"Princess Mononoke", 1997, "128", "tt0119698", "Animation", "Hayao Miyazaki"},
	{"Arrival", 2016, "329865", "tt2543164", "Drama", "Denis Villeneuve"},
	{"The Thirteenth Floor", 1999, "1090", "tt0139809", "Science Fiction", "Josef Rusnak"},
	{"Brüno", 2009, "18480", "tt0889583", "Comedy", "Larry Charles"},
	{"Limitless", 2011, "51876", "tt1219289", "Thriller", "Neil Burger"},
}

// showFixture is a series in the clean Shows library.
type showFixture struct {
	Title    string
	Year     int
	TMDB     string
	TVDB     string
	Episodes map[int][]int // season -> episode numbers on disk
}

var shows = []showFixture{
	{"Severance", 2022, "95396", "371980", map[int][]int{1: {1, 2}, 2: {1, 2}}},
	{"Breaking Bad", 2008, "1396", "81189", map[int][]int{1: {1, 2, 3}}},
	{"The Expanse", 2015, "63639", "280619", map[int][]int{0: {1}, 1: {1, 2}}},
	{"Limitless", 2015, "62687", "295743", map[int][]int{1: {1}}},
}

// albumFixture is an album in the Music library, a folder of its tracks, as
// the tags on them describe it: the library's fetchers are off, so nothing
// else can have changed them.
type albumFixture struct {
	Artist string
	Album  string
	Year   int
	Genre  string
	Tracks []string
	// Cover is false for the one album with no art, which is what
	// audit_missing_metadata's poster problem looks for in a music library.
	Cover bool
	// the MusicBrainz ids the tracks' tags carry, which come through as
	// their provider ids; none on the rip nothing was looked up for
	MBArtist, MBAlbum string
}

var albums = []albumFixture{
	{"Battle Tapes", "Polygon", 2015, "Electronic", []string{"Belgrade", "Valkyrie", "Solid Gold", "Private Dancer"}, true, "82178603-e97b-4d60-b521-82582545a0a8", "0e715a3a-8461-4620-8843-6c4324c64d49"},
	// no MusicBrainz ids, and its third track numbered 2 and its fourth not
	// at all
	{"Coyote Kisses", "Thundercolor", 2013, "Electronic", []string{"Diving At Night", "Stay With You", "This Is How You Know", "Changing Guard"}, false, "", ""},
	{"Pink Floyd", "The Dark Side of the Moon", 1973, "Progressive Rock", []string{"Speak to Me", "Breathe", "On the Run", "Time"}, true, "83d91898-7763-47d7-b03b-b92132375c47", "b84ee12a-09ef-421b-82de-0441a926375b"},
	{"Pink Floyd", "Wish You Were Here", 1975, "Progressive Rock", []string{"Shine On You Crazy Diamond, Parts I-V", "Welcome to the Machine", "Have a Cigar", "Wish You Were Here"}, true, "83d91898-7763-47d7-b03b-b92132375c47", "f4a8aa35-da90-33d8-9307-c630d38a2bed"},
	// tagged a letter apart from the other electronic acts, which is what
	// audit_spelling looks for
	{"SirensCeol", "Afterworld", 2016, "Electronica", []string{"Welcome to the Afterworld", "The Future We Built", "Afterworld", "A Grand Illusion"}, true, "621dac65-5eac-4ad3-a630-05f282bbe4e2", "00cc6656-b7c3-4c33-8df3-5909e46979b2"},
}

// The tags' defects beyond each album's own (scripts/testenv.sh): Polygon's
// album artist is Various Artists over Battle Tapes' tracks, Have a Cigar's
// artist is spelled The Pink Floyd, and Welcome to the Machine's album is
// spelled Wish You Where Here. The Dark Side of the Moon is two discs.
const (
	variousArtists = "Various Artists"
	pinkFloydAgain = "The Pink Floyd"
	misspeltAlbum  = "Wish You Where Here"
)

// musicAlbums is how many albums the music library holds: an album a
// folder on Jellyfin, and on Emby, which builds albums from the tags alone,
// one more for the track whose album tag is misspelt.
func musicAlbums() int {
	if isJellyfin() {
		return len(albums)
	}

	return len(albums) + 1
}

// songs is how many audio files the music library holds.
func songs() int {
	n := 0
	for _, a := range albums {
		n += len(a.Tracks)
	}

	return n
}

// artists is how many distinct artists the music library holds: each
// album's, and the two the tags name besides - Polygon's album artist and
// the other spelling of Pink Floyd - which both servers make artists of.
func artists() int {
	seen := map[string]bool{variousArtists: true, pinkFloydAgain: true}
	for _, a := range albums {
		seen[a.Artist] = true
	}

	return len(seen)
}

// The messy movies, by folder, and the defect each carries.
const (
	messyMononoke     = "Princess Mononoke (1997)"   // no nfo: unmatched, no overview, no poster; Japanese then English audio
	messyArrival      = "Arrival (2016)"             // ids but no plot, no poster
	messyDune         = "Dune (2021)"                // folder 2021, metadata 1984
	messyAlien        = "Alien (1979)"               // tmdb 348, twice
	messyAlienCut     = "Alien (1979) Directors Cut" // tmdb 348, twice
	messyBladeRunner  = "Blade Runner (1982)"        // two files: 1080p and 2160p
	messyInterstellar = "Interstellar (2014)"        // a trailer, and Trailers and Extras folders
	messyLooseDVD     = "Coyote vs. Acme (2026)"     // a DVD's VOB loose in the folder, no nfo
	messyKeptBluRay   = "Cube (1997)"                // a Blu-ray kept whole, BDMV/STREAM, no nfo
	messyKeptDVD      = "Moon (2009)"                // a DVD kept whole, VIDEO_TS, its nfo inside
	messyCrossed      = "Memento (2000)"             // Breaking Bad's IMDb id and no TMDB one

	// a fan restoration of Star Wars: a TMDB id TMDB has no film for, the
	// genre spelled Science-Fiction, and German audio alone, tagged ger
	messyDespecialized = "Star Wars Episode IV - A New Hope Despecialized Edition (1977)"
	// and the title its nfo gives it
	despecialized = "Star Wars: Episode IV - A New Hope (Despecialized Edition)"
)

// messyUnmatched are the messy films no nfo names: the one with none, and the
// two discs whose folders hold nothing but the disc. Moon, the DVD kept whole,
// carries its nfo inside VIDEO_TS.
var messyUnmatched = []string{"Coyote vs. Acme", "Cube", "Princess Mononoke"}

// messyShowFixture is a messy series as its folder and tvshow.nfo lay it out:
// the ids its nfo carries and the episode files on disk.
type messyShowFixture struct {
	Folder, Title    string
	TMDB, TVDB, IMDB string
	Episodes         map[int][]int // season -> episode numbers on disk
}

// messyShows are the messy series laid out for the show tools and the missing
// episode audit: The Wire split across two folders by a rename, the Asterix
// series holding the 1989 film's ids, and Red Dwarf from its third season.
var messyShows = []messyShowFixture{
	{"The Wire", "The Wire", "1438", "79126", "tt0306414", map[int][]int{1: {1, 2}}},
	{"The Wire (2002)", "The Wire", "1438", "79126", "tt0306414", map[int][]int{1: {2, 3}}},
	{"Asterix & Obelix - The Big Fight (2025)", "Asterix & Obelix: The Big Fight", "11625", "", "tt0096842", map[int][]int{1: {1, 2}}},
	{"Red Dwarf", "Red Dwarf", "326", "71326", "tt0094535", map[int][]int{3: {1, 2, 3}}},
}

var (
	ctx     context.Context
	session *mcp.ClientSession
	proxy   *testenv.Proxy
	backend embyfin.Backend
	// suite drives the tools through session; until start connects it, every
	// call skips
	suite = &acc.Suite{NotReady: "EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set; run: eval \"$(EMBYFIN_TEST_BACKEND=jellyfin scripts/testenv.sh up)\""}
)

// isJellyfin lets a test say where the two servers legitimately differ;
// everything else is asserted the same way for both.
func isJellyfin() bool { return backend == embyfin.Jellyfin }

// testMain connects, starts the provider proxy, seeds the fixtures, and runs.
func testMain(m *testing.M) {
	if !testenv.Configured() {
		os.Exit(m.Run()) // every test skips
	}
	backend = embyfin.Backend(testenv.Backend())
	p, err := testenv.StartProxy(context.Background(), testenv.CassetteDir(string(backend)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		os.Exit(1)
	}
	proxy = p
	if err := start(); err != nil {
		_ = proxy.Stop()
		fmt.Fprintln(os.Stderr, "acceptance setup:", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := proxy.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	// a replay miss, or an answer that changed shape, is said loudly even
	// when the assertions happened to survive it
	if report := proxy.Report(); report != "" {
		fmt.Fprint(os.Stderr, report)
		if code == 0 {
			code = 1
		}
	}

	// every registered tool must have answered something above, not only
	// refused it. Only a whole-suite run can say that, so a -run filter skips
	// the check.
	if f := flag.Lookup("test.run"); f == nil || f.Value.String() == "" {
		never, onlyRefused, err := suite.Uncovered()
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "\ntool coverage: could not list tools:", err)
			code = 1
		case len(never)+len(onlyRefused) > 0:
			if len(never) > 0 {
				fmt.Fprintf(os.Stderr, "\n%d registered tool(s) are never called by this suite:\n", len(never))
				for _, name := range never {
					fmt.Fprintln(os.Stderr, "  "+name)
				}
			}
			if len(onlyRefused) > 0 {
				fmt.Fprintf(os.Stderr, "\n%d registered tool(s) only ever failed in this suite, so nothing shows they work:\n", len(onlyRefused))
				for _, name := range onlyRefused {
					fmt.Fprintf(os.Stderr, "  %s (%d failed calls)\n", name, suite.Calls(name).Failed)
				}
			}
			fmt.Fprintln(os.Stderr, "every tool needs a test that it answers; add one or remove the tool")
			code = 1
		}
	}

	os.Exit(code)
}

// cannotAnswer are the tools a throwaway server gives nothing to answer
// with, so a call that fails is the only one a test can make, and why (see
// acc.Suite.Uncovered).
var cannotAnswer = map[string]string{
	"item_subtitle_download": "no subtitle provider is installed on a test server, so nothing is ever offered to download",
}

// providerTransport routes embyfin-mcp's own provider calls (TMDB, which
// audit_provider, audit_missing_episodes, audit_file_path and show_missing
// ask) through the proxy, trusting its CA, so they replay like everything
// else.
func providerTransport() (http.RoundTripper, error) {
	proxyURL, err := url.Parse("http://" + proxy.Addr())
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if ca := os.Getenv("EMBYFIN_TEST_PROXY_CA"); ca != "" {
		pem, err := os.ReadFile(filepath.Join(ca, "ca.pem")) //nolint:gosec // the test harness's own CA
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s/ca.pem holds no certificate", ca)
		}
	}

	return &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}, nil
}

// tmdbKey is the TMDB token the tools are given: audit_provider checks films'
// ids and runtimes with it, and show_missing reads a series' run. Recording
// needs a real one; replay matches with any, because the proxy redacts
// api_key.
func tmdbKey() string {
	if k := cmp.Or(os.Getenv("EMBYFIN_TMDB_TOKEN"), os.Getenv("EMBYFIN_TMDB_KEY")); k != "" {
		return k
	}

	return "replay"
}

// animeList is the list audit_anime_ids reads here: a few entries in
// Anime-Lists' shape, so the suite never fetches the real one.
var animeList = filepath.Join("testdata", "anime-list.xml")

func start() error {
	client, err := embyfin.New(backend, os.Getenv("EMBYFIN_SERVER"), os.Getenv("EMBYFIN_TOKEN"))
	if err != nil {
		return err
	}
	rt, err := providerTransport()
	if err != nil {
		return err
	}

	ctx = context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "embyfin-mcp", Version: "test"}, nil)
	if _, err := tools.RegisterAll(srv, client, tools.Options{EnableDelete: true, TMDBKey: tmdbKey(), ProviderTransport: rt, AnimeList: animeList}); err != nil {
		return err
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		return err
	}
	if session, err = mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil); err != nil {
		return err
	}
	suite.Ctx, suite.Session, suite.Ready, suite.CannotAnswer = ctx, session, true, cannotAnswer

	return seed()
}

// seed builds the fixtures through the tools. It is idempotent: a library that
// already exists is left alone, so a suite can be re-run against a container
// that is still up.
func seed() error {
	existing, err := suite.Invoke("library_list", nil)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, row := range acc.RowsOf(existing["libraries"]) {
		if name, ok := row["name"].(string); ok {
			have[name] = true
		}
	}

	var created bool
	for _, l := range libraries {
		if have[l.Name] {
			continue
		}
		args := map[string]any{"name": l.Name, "type": l.Type, "paths": []any{l.Folder}, "providers": l.Providers}
		if _, err := suite.Invoke("library_create", args); err != nil {
			return err
		}
		created = true
	}
	if !created {
		return nil // already seeded
	}

	if _, err := suite.Invoke("library_scan", nil); err != nil {
		return err
	}
	for _, l := range libraries {
		if err := waitForItems(l.Name, l.Items()); err != nil {
			return err
		}
	}

	return suite.WaitForScan()
}

// primaryType is the item type a library is counted by.
func primaryType(library string) string {
	for _, l := range libraries {
		if l.Name != library {
			continue
		}
		switch l.Type {
		case "tvshows":
			return "Series"
		case "music":
			return "MusicAlbum"
		}
	}

	return "Movie"
}

// waitForItems polls library_get until a scan has settled on want items of
// the library's primary type (see primaryType).
func waitForItems(library string, want int) error {
	return suite.WaitForItems(library, primaryType(library), want, acc.ScanPatience)
}

// scanUntil asks for a library scan and waits for the library to hold want
// items of its kind (acc.Suite.ScanUntil).
func scanUntil(library string, want int) error {
	return suite.ScanUntil(library, primaryType(library), want)
}

// findItem searches a library for a title and returns its id, failing when
// it is not exactly one item.
func findItem(t *testing.T, library, types, title string) string {
	t.Helper()

	out := suite.Call(t, "library_items", map[string]any{"library": library, "types": types, "query": title, "limit": 50})
	var ids []string
	for _, row := range acc.Rows(t, out["items"], "items") {
		// an unmatched film keeps its "(2001)" on Jellyfin and loses it on Emby
		if strings.EqualFold(yearSuffix.ReplaceAllString(acc.Str(row["name"]), ""), title) {
			ids = append(ids, acc.Str(row["id"]))
		}
	}
	if len(ids) != 1 {
		t.Fatalf("%d items titled %q in %s: %v", len(ids), title, library, out["items"])
	}

	return ids[0]
}

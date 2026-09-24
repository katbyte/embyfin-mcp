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
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/katbyte/embyfin-mcp/lib/providerproxy"
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
	// Episodes is the number of episode files in a show library.
	Episodes int
}

// libraries are created by library_create and filled by library_scan. The
// folders are the container-side paths scripts/testenv.sh lays out.
var libraries = []libraryFixture{
	{Name: "Movies", Type: "movies", Folder: "/media/movies", Providers: true, Items: func() int { return 8 }},
	{Name: "Shows", Type: "tvshows", Folder: "/media/shows", Providers: true, Items: func() int { return 3 }, Episodes: 9},
	{Name: "Messy Movies", Type: "movies", Folder: "/media/messy-movies", Items: messyMovies},
	{Name: "Messy Shows", Type: "tvshows", Folder: "/media/messy-shows", Items: func() int { return messySeries }, Episodes: messyEpisodes},
	{Name: "Music", Type: "music", Folder: "/media/music", Items: func() int { return len(albums) }},
}

// messyMovies is how many films the messy library stores: eleven folders, but
// Jellyfin folds the two Blade Runner files into one entry with two versions
// while Emby keeps them as two items. Emby does merge them - by file name,
// and the two Aliens by their shared TMDB id - but only in what it shows
// people, which the versions and duplicates audits read
// (TestAuditMultipleVersions); every other count is of what it stores.
func messyMovies() int {
	if isJellyfin() {
		return 11
	}

	return 12
}

// The messy show library: Severance, Star Trek The Next Generation, Star Trek:
// Deep Space Nine, Andor, the A Knight of the Seven Kingdoms pair and
// .hack//Liminality, holding eighteen episode files between them
// (scripts/testenv.sh says what is wrong with each).
const (
	messySeries   = 7
	messyEpisodes = 18
)

// versionsMerged reports whether the server stores the two Blade Runner
// files as one entry, so a sweep of its items sees one film. Emby stores two
// and merges them only in what it shows people (audit_multiple_versions).
func versionsMerged() bool { return isJellyfin() }

// movieFixture is a film in the clean Movies library, as its nfo describes it.
type movieFixture struct {
	Title    string
	Year     int
	TMDB     string
	IMDB     string
	Runtime  int
	Genre    string
	Director string
}

var movies = []movieFixture{
	{"Alien", 1979, "348", "tt0078748", 117, "Horror", "Ridley Scott"},
	{"Aliens", 1986, "679", "tt0090605", 137, "Action", "James Cameron"},
	{"Blade Runner", 1982, "78", "tt0083658", 117, "Science Fiction", "Ridley Scott"},
	{"Dune", 2021, "438631", "tt1160419", 155, "Science Fiction", "Denis Villeneuve"},
	{"Dune: Part Two", 2024, "693134", "tt15239678", 167, "Science Fiction", "Denis Villeneuve"},
	{"Princess Mononoke", 1997, "128", "tt0119698", 134, "Animation", "Hayao Miyazaki"},
	{"Arrival", 2016, "329865", "tt2543164", 116, "Drama", "Denis Villeneuve"},
	{"The Thirteenth Floor", 1999, "1090", "tt0139809", 100, "Science Fiction", "Josef Rusnak"},
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
	{"The Expanse", 2015, "63639", "280619", map[int][]int{1: {1, 2}}},
}

// albumFixture is an album in the Music library, as the tags on its tracks
// describe it: the library's fetchers are off, so nothing else can have
// changed them.
type albumFixture struct {
	Artist string
	Album  string
	Year   int
	Genre  string
	Tracks []string
	// Cover is false for the one album with no art, which is what
	// audit_missing_poster looks for in a music library.
	Cover bool
}

var albums = []albumFixture{
	{"Battle Tapes", "Polygon", 2015, "Electronic", []string{"Belgrade", "Valkyrie", "Solid Gold", "Private Dancer"}, true},
	{"Coyote Kisses", "Thundercolor", 2013, "Electronic", []string{"Diving At Night", "Stay With You", "This Is How You Know", "Changing Guard"}, false},
	{"Pink Floyd", "The Dark Side of the Moon", 1973, "Progressive Rock", []string{"Speak to Me", "Breathe", "On the Run", "Time"}, true},
	{"Pink Floyd", "Wish You Were Here", 1975, "Progressive Rock", []string{"Shine On You Crazy Diamond, Parts I-V", "Welcome to the Machine", "Have a Cigar", "Wish You Were Here"}, true},
	// tagged a letter apart from the other electronic acts, which is what
	// audit_spelling looks for
	{"SirensCeol", "Afterworld", 2016, "Electronica", []string{"Welcome to the Afterworld", "The Future We Built", "Afterworld", "A Grand Illusion"}, true},
}

// songs is how many audio files the music library holds.
func songs() int {
	n := 0
	for _, a := range albums {
		n += len(a.Tracks)
	}

	return n
}

// artists is how many distinct artists the music library holds.
func artists() int {
	seen := map[string]bool{}
	for _, a := range albums {
		seen[a.Artist] = true
	}

	return len(seen)
}

// The messy movies, by folder, and the defect each carries.
const (
	messyMononoke     = "Princess Mononoke (1997)"   // no nfo: unmatched, no overview, no poster
	messyArrival      = "Arrival (2016)"             // ids but no plot, no poster
	messyDune         = "Dune (2021)"                // folder 2021, metadata 1984
	messyAlien        = "Alien (1979)"               // tmdb 348, twice
	messyAlienCut     = "Alien (1979) Directors Cut" // tmdb 348, twice
	messyBladeRunner  = "Blade Runner (1982)"        // two files: 1080p and 2160p
	messyInterstellar = "Interstellar (2014)"        // nfo says 169 minutes, the file runs one second
	messyLooseDVD     = "Coyote vs. Acme (2026)"     // a DVD's VOB loose in the folder, no nfo
	messyKeptBluRay   = "Cube (1997)"                // a Blu-ray kept whole, BDMV/STREAM, no nfo
	messyCrossed      = "Memento (2000)"             // Breaking Bad's IMDb id and no TMDB one

	// a fan restoration of Star Wars: a TMDB id TMDB has no film for, and the
	// genre spelled Science-Fiction
	messyDespecialized = "Star Wars Episode IV - A New Hope Despecialized Edition (1977)"
)

// messyUnmatched are the messy films no nfo names: the one with none, and the
// two discs, whose folders hold nothing but the disc.
var messyUnmatched = []string{"Coyote vs. Acme", "Cube", "Princess Mononoke"}

var (
	ctx     context.Context
	session *mcp.ClientSession
	ready   bool
	proxy   *providerproxy.Proxy
	backend embyfin.Backend
)

// recording reports whether this run may call the real providers.
// EMBYFIN_TEST_RECORD=1 fills in only the answers a cassette lacks, replaying
// the rest as recorded; EMBYFIN_TEST_RECORD=all fetches every answer afresh
// (make record). Either needs a TMDB token.
func recording() bool { return os.Getenv("EMBYFIN_TEST_RECORD") != "" }

// verifying reports whether to check the cassettes against the live providers
// without rewriting them.
func verifying() bool { return os.Getenv("EMBYFIN_TEST_VERIFY") != "" }

// configured reports whether the container environment is present.
func configured() bool {
	return os.Getenv("EMBYFIN_SERVER") != "" && os.Getenv("EMBYFIN_TOKEN") != "" && os.Getenv("EMBYFIN_BACKEND") != ""
}

// dataDir is the host path the container's /media is bind-mounted from, so a
// test can add or remove files and rescan.
func dataDir() string { return filepath.Join(os.Getenv("EMBYFIN_TEST_DATA"), "media") }

// isJellyfin lets a test say where the two servers legitimately differ;
// everything else is asserted the same way for both.
func isJellyfin() bool { return backend == embyfin.Jellyfin }

// testMain connects, starts the provider proxy, seeds the fixtures, and runs.
func testMain(m *testing.M) {
	if !configured() {
		os.Exit(m.Run()) // every test skips
	}
	backend = embyfin.Backend(os.Getenv("EMBYFIN_BACKEND"))
	if err := startProxy(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		os.Exit(1)
	}
	if err := start(); err != nil {
		stopProxy()
		fmt.Fprintln(os.Stderr, "acceptance setup:", err)
		os.Exit(1)
	}

	code := m.Run()
	stopProxy()

	// a replay miss means a test ran against a 502 rather than a recording, so
	// say so loudly even when the assertions happened to survive it
	if misses := proxyMisses; len(misses) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d request(s) had no recording:\n", len(misses))
		for _, m := range misses {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
		fmt.Fprintln(os.Stderr, "run `make record` to capture them")
		if code == 0 {
			code = 1
		}
	}

	// every registered tool must have been called by something above. Only a
	// whole-suite run can say that, so a -run filter skips the check.
	if f := flag.Lookup("test.run"); f == nil || f.Value.String() == "" {
		missing, err := uncovered()
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "\ntool coverage: could not list tools:", err)
			code = 1
		case len(missing) > 0:
			fmt.Fprintf(os.Stderr, "\n%d registered tool(s) are never called by this suite:\n", len(missing))
			for _, name := range missing {
				fmt.Fprintln(os.Stderr, "  "+name)
			}
			fmt.Fprintln(os.Stderr, "every tool needs a test; add one or remove the tool")
			code = 1
		}
	}

	// drift is only collected under EMBYFIN_TEST_VERIFY: the providers still
	// answer, but no longer in the shape the server decodes
	if drifts := proxyDrifts; len(drifts) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d response(s) changed shape since recording:\n", len(drifts))
		for _, d := range drifts {
			fmt.Fprintln(os.Stderr, "  "+d.String())
		}
		fmt.Fprintln(os.Stderr, "\nreview the changes, then run `make record` to accept them")
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

var (
	proxyMisses []string
	proxyDrifts []providerproxy.Drift
)

var (
	calledMu sync.Mutex
	called   = map[string]bool{}
)

// uncovered names the registered tools no test called. A tool that is only
// listed is not tested, so adding one without a test fails the suite rather
// than quietly widening the untested surface.
func uncovered() ([]string, error) {
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}

	calledMu.Lock()
	defer calledMu.Unlock()

	var missing []string
	for _, tool := range res.Tools {
		if !called[tool.Name] {
			missing = append(missing, tool.Name)
		}
	}
	slices.Sort(missing)

	return missing, nil
}

// cassetteDir is where this backend's recordings live: Emby and Jellyfin ask
// the providers different questions, so each has its own set.
func cassetteDir() string {
	return filepath.Join("testdata", "cassettes", string(backend))
}

// startProxy brings up the record/replay proxy the container's HTTPS_PROXY
// already points at, signing with the CA scripts/testenv.sh minted and
// mounted into the container.
func startProxy() error {
	port := 18080
	if v := os.Getenv("EMBYFIN_TEST_PROXY_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("EMBYFIN_TEST_PROXY_PORT=%q: %w", v, err)
		}
		port = n
	}

	mode := providerproxy.Replay
	switch {
	case recording():
		mode = providerproxy.Record
	case verifying():
		mode = providerproxy.Verify
	}

	opts := providerproxy.Options{
		Mode:        mode,
		CassetteDir: cassetteDir(),
		// every interface and both stacks: the container reaches this through
		// host.docker.internal, which docker maps to the host gateway, and a
		// runner that hands the container an IPv6 route as well would find
		// nothing listening on an IPv4-only socket
		Addr: ":" + strconv.Itoa(port),
		// no API key may decide a cassette match or be committed with it:
		// an operator's own (TMDB's api_key) or the one a media server
		// carries for a provider (OMDb's apikey, which is Emby's and
		// Jellyfin's to rotate, not ours to publish)
		RedactQuery: []string{"api_key", "apikey"},
		// a provider's login answers with a bearer token for the media
		// server's own account; replay never needs one
		RedactBodyFields: []string{"token"},
		// the media server reaching itself is not provider traffic
		// Emby asks ipify, then its own service, for its public address on
		// startup, which is nobody's business and not part of any recording
		IgnoreHosts: append(containerAddresses(), "api.ipify.org", "api64.ipify.org", "connect.emby.media"),
	}
	if ca := os.Getenv("EMBYFIN_TEST_PROXY_CA"); ca != "" {
		opts.CACert, opts.CAKey = filepath.Join(ca, "ca.pem"), filepath.Join(ca, "ca.key")
	}
	p, err := providerproxy.New(opts)
	if err != nil {
		return err
	}
	proxy = p

	return checkProxyReachable(port)
}

func stopProxy() {
	if proxy == nil {
		return
	}
	proxyMisses = proxy.Misses()
	proxyDrifts = proxy.Drifts()
	if err := proxy.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy close:", err)
	}
	proxy = nil
}

// providerTransport routes embyfin-mcp's own provider calls (the TMDB runtime
// lookup) through the proxy, trusting its CA, so the movie runtime audit
// replays like everything else.
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

// tmdbKey is what audit_runtime uses for movies. Recording needs a real one;
// replay matches with any, because the proxy redacts api_key.
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
	ready = true

	return seed()
}

// seed builds the fixtures through the tools. It is idempotent: a library that
// already exists is left alone, so a suite can be re-run against a container
// that is still up.
func seed() error {
	existing, err := invoke("library_list", nil)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, row := range rowsOf(existing["libraries"]) {
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
		if _, err := invoke("library_create", args); err != nil {
			return err
		}
		created = true
	}
	if !created {
		return nil // already seeded
	}

	if _, err := invoke("library_scan", nil); err != nil {
		return err
	}
	for _, l := range libraries {
		if err := waitForItems(l.Name, l.Items()); err != nil {
			return err
		}
	}

	return waitForScan()
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

// rowsOf pulls a list of objects out of a decoded JSON field, tolerating a
// missing one.
func rowsOf(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if row, ok := e.(map[string]any); ok {
			out = append(out, row)
		}
	}

	return out
}

// scanPatience is how long a library scan is given: quick on a quiet
// machine, and not always quick on a runner sharing itself with three other
// suites. Every wait on a scan uses it, so none gives up before the scan
// can have finished and leaks the scan into the next test.
const scanPatience = 6 * time.Minute

// waitForItems polls library_get until a scan has settled on want items of
// the library's primary type (see primaryType).
func waitForItems(library string, want int) error {
	return waitForItemsFor(library, want, scanPatience)
}

// waitForItemsFor is waitForItems with its own patience.
func waitForItemsFor(library string, want int, patience time.Duration) error {
	kind := primaryType(library)
	var last string
	for range int(patience / (2 * time.Second)) {
		out, err := invoke("library_get", map[string]any{"library": library})
		switch {
		case err != nil:
			last = err.Error()
		default:
			counts, _ := out["type_counts"].(map[string]any)
			n, _ := counts[kind].(float64)
			if int(n) == want {
				return nil
			}
			last = fmt.Sprintf("at %d %s items", int(n), kind)
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("library %s never reached %d %s items (%s)", library, want, kind, last)
}

// scanUntil asks for a library scan and waits for the library to hold want
// items of its kind, asking again whenever the scan goes idle short of the
// count. A scan already running when the ask comes passes folders written
// since it started, on both servers, and the ask itself is dropped by
// Jellyfin, so one ask is not enough on a busy server (CI's runners are).
func scanUntil(library string, want int) error {
	deadline := time.Now().Add(scanPatience)
	for {
		// idle before asking, so the ask starts a scan rather than joining one
		if err := waitForScan(); err != nil {
			return err
		}
		if _, err := invoke("library_scan", nil); err != nil {
			return err
		}
		if err := waitForItemsFor(library, want, 45*time.Second); err == nil {
			return waitForScan()
		} else if time.Now().After(deadline) {
			return err
		}
	}
}

// waitForScan waits for the library scan task to go idle, so the provider
// lookups the scan triggers have finished before a test looks at their
// results. A task_list that fails once (a server busy with the scan) is
// asked again rather than ending the wait.
func waitForScan() error {
	failures := 0
	for range int(scanPatience / (2 * time.Second)) {
		idle, err := scanIdle()
		switch {
		case err != nil:
			if failures++; failures > 5 {
				return err
			}
		case idle:
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	return errors.New("the library scan never went idle")
}

// scanIdle says whether the library scan task is idle.
func scanIdle() (bool, error) {
	out, err := invoke("task_list", nil)
	if err != nil {
		return false, err
	}
	for _, row := range rowsOf(out["tasks"]) {
		name, _ := row["name"].(string)
		state, _ := row["state"].(string)
		if strings.Contains(strings.ToLower(name), "scan media library") && state != "Idle" {
			return false, nil
		}
	}

	return true, nil
}

// waitForExpectedScan is waitForScan for a scan a change should have
// started: a library made, deleted or given a folder on Jellyfin. It gives
// the scan a moment to show up in the task list (Jellyfin starts the task
// off the request thread) and, if it never does, starts one itself, so a
// dropped scan costs a wait rather than a test. Emby's scans start on the
// request, so there it is a plain waitForScan.
func waitForExpectedScan() error {
	if !isJellyfin() {
		return waitForScan()
	}
	started := false
	for range 10 {
		idle, err := scanIdle()
		if err == nil && !idle {
			started = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !started {
		if _, err := invoke("task_run", map[string]any{"task": "scan media library"}); err != nil {
			return fmt.Errorf("the change started no scan, and starting one failed: %w", err)
		}
	}

	return waitForScan()
}

// invoke calls a tool and returns its structured result. Every tool call in
// the suite comes through here, so this is also where coverage is recorded.
func invoke(name string, args map[string]any) (map[string]any, error) {
	calledMu.Lock()
	called[name] = true
	calledMu.Unlock()

	if args == nil {
		args = map[string]any{}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		return nil, fmt.Errorf("%s: %s", name, strings.Join(msgs, "; "))
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: structured content is %T", name, res.StructuredContent)
	}

	return out, nil
}

// call invokes a tool, skipping the test when the container is not configured
// and failing it when the tool errors.
func call(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()

	if !ready {
		t.Skip("EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set; run: eval \"$(EMBYFIN_TEST_BACKEND=jellyfin scripts/testenv.sh up)\"")
	}
	out, err := invoke(name, args)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// callErr invokes a tool expecting it to fail, and returns the error message.
func callErr(t *testing.T, name string, args map[string]any) string {
	t.Helper()

	if !ready {
		t.Skip("EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set")
	}
	out, err := invoke(name, args)
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded: %v", name, out)
	}

	return err.Error()
}

// toolNames lists every tool the server registered, so a test can assert that
// a family is complete rather than only that the tools it knows about work.
func toolNames(t *testing.T) []string {
	t.Helper()

	if !ready {
		t.Skip("EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set")
	}
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}

	return out
}

// strs pulls a []string out of a decoded JSON field.
func strs(t *testing.T, v any, field string) []string {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", field, v)
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("%s contains %T, want strings", field, e)
		}
		out = append(out, s)
	}

	return out
}

// rows pulls a list of objects out of a decoded JSON field.
func rows(t *testing.T, v any, field string) []map[string]any {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", field, v)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		row, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%s contains %T, want objects", field, e)
		}
		out = append(out, row)
	}

	return out
}

// num pulls a JSON number out of a decoded field.
// decimal reads a fractional number: a ratio, a margin or a frame rate, where
// rounding to an int would pass a check that should fail.
func decimal(t *testing.T, v any, field string) float64 {
	t.Helper()

	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s is %T (%v), want a number", field, v, v)
	}

	return f
}

// object reads a nested object.
func object(t *testing.T, v any, field string) map[string]any {
	t.Helper()

	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T (%v), want an object", field, v, v)
	}

	return m
}

func num(t *testing.T, v any, field string) int {
	t.Helper()

	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s is %T, want a number", field, v)
	}

	return int(f)
}

// str pulls a string out of a decoded field, "" when absent.
func str(v any) string {
	s, _ := v.(string)
	return s
}

// findItem searches a library for a title and returns its id, failing when
// it is not exactly one item.
func findItem(t *testing.T, library, types, title string) string {
	t.Helper()

	out := call(t, "library_items", map[string]any{"library": library, "types": types, "query": title, "limit": 50})
	var ids []string
	for _, row := range rows(t, out["items"], "items") {
		// an unmatched film keeps its "(2001)" on Jellyfin and loses it on Emby
		if strings.EqualFold(yearSuffix.ReplaceAllString(str(row["name"]), ""), title) {
			ids = append(ids, str(row["id"]))
		}
	}
	if len(ids) != 1 {
		t.Fatalf("%d items titled %q in %s: %v", len(ids), title, library, out["items"])
	}

	return ids[0]
}

// movieCount is how many films a library holds right now.
func movieCount(t *testing.T, library string) int {
	t.Helper()

	out := call(t, "library_get", map[string]any{"library": library})
	counts, _ := out["type_counts"].(map[string]any)
	n, _ := counts["Movie"].(float64)

	return int(n)
}

// numOr0 pulls a JSON number out of a decoded field, 0 when it was omitted.
func numOr0(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// mediaMkdir makes a directory under the bind-mounted media tree that the
// media server's own user can write in, and mediaWrite writes a file there.
// The mode asked of MkdirAll and WriteFile is filtered by the process umask,
// which on Linux leaves a directory nobody but the test can write to - so the
// server (uid 2 in Emby's image, root in Jellyfin's) cannot delete a file the
// test laid out, and item_delete fails. chmod is not filtered by the umask,
// so the mode asked for is the mode applied. Docker Desktop hides this by
// mapping every file to the container's user, which is why it only bites in
// CI.
func mediaMkdir(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	root := dataDir()
	for p := dir; strings.HasPrefix(p, root) && p != root; p = filepath.Dir(p) {
		if err := os.Chmod(p, 0o777); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
	}
}

func mediaWrite(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o666); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil { //nolint:gosec // same
		t.Fatal(err)
	}
}

// containerAddresses are the addresses the media server reaches itself on:
// Emby pings its own container address at startup, which goes through the
// proxy because NO_PROXY is set before docker hands the container an address.
// The proxy answers those without a cassette (Options.IgnoreHosts).
func containerAddresses() []string {
	name := os.Getenv("EMBYFIN_TEST_CONTAINER")
	if name == "" {
		return nil
	}
	out, err := exec.Command("docker", "inspect", "-f",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}{{.Config.Hostname}}", name).Output()
	if err != nil {
		return nil // not our container to ask about
	}

	return strings.Fields(string(out))
}

// checkProxyReachable proves, from inside the container, that the media server
// can reach the provider proxy. A server that cannot fails every provider
// lookup with a timeout of its own, which reads as dozens of unrelated
// assertion failures rather than the one plumbing problem it is - so say it
// plainly, once, before the suite runs.
func checkProxyReachable(port int) error {
	name := os.Getenv("EMBYFIN_TEST_CONTAINER")
	if name == "" {
		return nil // not a container this suite started
	}
	// exit 3 says the image has no probe tool, which is not a failure. The
	// hosts entries come too: a container handed an IPv6 route to the host
	// gateway can reach the proxy with one address and not the other.
	script := fmt.Sprintf(
		"grep -i host.docker.internal /etc/hosts; echo \"proxy env: ${HTTPS_PROXY:-unset}\"; "+
			"command -v nc >/dev/null || exit 3; nc -z -w 5 host.docker.internal %d", port)
	out, err := exec.Command("docker", "exec", name, "sh", "-c", script).CombinedOutput()
	fmt.Fprintf(os.Stderr, "container network: %s\n", strings.TrimSpace(string(out)))
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "exit status 3"):
		return nil
	default:
		return fmt.Errorf("%s cannot reach the provider proxy on host.docker.internal:%d, so every provider lookup will time out: %w: %s",
			name, port, err, out)
	}
}

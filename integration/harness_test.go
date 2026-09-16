//go:build integration

// The harness: scripts/testenv.sh brings the container up and exports
// EMBYFIN_BACKEND, EMBYFIN_SERVER, EMBYFIN_TOKEN and EMBYFIN_TEST_*; runSuite
// builds the SDK client for that backend, starts the provider proxy the
// container's HTTPS_PROXY points at, runs the suite, and removes the
// libraries the suite created.
//
// What is deliberately not exercised, and why:
//
//   - Live TV, DVR, tuners, guide data and recordings: the container has no
//     tuner and no listings provider; the static reads that work without one
//     (tuner host types, the service info) are covered.
//   - DLNA and UPnP: nothing on the docker network answers SSDP.
//   - Media streaming and transcoding (Videos/{id}/stream, HLS, universal
//     audio, trickplay tiles, subtitle delivery): needs a playback session
//     and ffmpeg time for a byte stream the tests would only discard;
//     PlaybackInfo, which reports the sources without starting anything, is
//     covered instead.
//   - SyncPlay groups: needs a second websocket-connected client to join.
//   - Emby Sync (offline downloads), Emby Connect, Games and the Kodi
//     companion endpoints: the reads that answer on a bare server are
//     covered; the writes need a target device or an Emby Connect account.
//   - Jellyfin QuickConnect authorization: the flow needs a second device
//     polling for a code; only the enabled flag is read.
//   - Backup restore: restarts the server mid-run.
//   - Plugin install, update and uninstall, and server restart/shutdown:
//     they would take the container out from under the rest of the suite.
//   - Image upload and delete for users and items: the item image endpoints
//     are covered through the remote-image download; user images have no
//     fixture to upload.
//   - Websocket messages: the SDKs are HTTP only.
//   - Cameras and photo libraries: no fixture images.
package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
	"github.com/katbyte/embyfin-mcp/lib/providerproxy"
)

// libraryFixture is one of the libraries the suite creates. The folders are
// the container-side paths scripts/testenv.sh lays out.
type libraryFixture struct {
	Name, CollectionType, Folder string
	// Providers leaves the server's internet fetchers on, so the scan fills
	// the library from TMDB (and, on Emby, TheTVDB and OMDb) through the
	// provider proxy. Off, the nfo sidecars are all the server knows.
	Providers bool
	// Movies, Series and Episodes are the counts a finished scan settles on,
	// Albums and Songs the same for a music library.
	Movies, Series, Episodes int
	Albums, Songs            int
}

// The libraries. Only the movie library has providers on: it is what the
// remote-image, remote-search and refresh tests need, and each library with
// providers on costs a cassette full of provider traffic.
var (
	sdkMovies = libraryFixture{Name: "SDK Movies", CollectionType: "movies", Folder: "/media/movies", Providers: true, Movies: 8}
	sdkShows  = libraryFixture{Name: "SDK Shows", CollectionType: "tvshows", Folder: "/media/shows", Series: 3, Episodes: 9}
	// sdkScratch sits over a folder the destructive test lays out itself,
	// so the delete has something to remove that nothing else relies on
	sdkScratch = libraryFixture{Name: "SDK Scratch", CollectionType: "movies", Folder: "/media/sdk-scratch", Movies: 1}
	// sdkMusic is what the artist, album, song and music genre routes have to
	// read: without it a fifth of each server's item surface cannot be called
	sdkMusic = libraryFixture{Name: "SDK Music", CollectionType: "music", Folder: "/media/music", Albums: 5, Songs: 20}
)

// movieFixture is a film in the clean movies folder, as its nfo describes it.
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
}

// showFixture is a series in the clean shows folder.
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

// The fixture facts the assertions lean on.
const (
	alien       = "Alien"
	aliens      = "Aliens"
	bladeRunner = "Blade Runner"
	severance   = "Severance"
	ridleyScott = "Ridley Scott"
	// lyricTrack is the one fixture track with an .lrc sidecar beside it
	lyricTrack = "The Future We Built"
	// scratchTitle is the throwaway movie the destructive test lays out
	scratchTitle = "SDK Disposable"
	// sdkApp is the AppName of the API key the suite creates and revokes
	sdkApp = "sdk-integration"
	// deviceID is what both SDK clients identify themselves as, and so the
	// DeviceId of the suite's own session
	deviceID = "embyfin-mcp"
)

var (
	backend string
	// exactly one of these is set, by backend
	embyc *emby.Client
	jfc   *jf.Client

	adminID, aliceID, password string
	proxy                      *providerproxy.Proxy
	proxyMisses                []string
	proxyDrifts                []providerproxy.Drift
)

// recording reports whether this run should call the real providers and
// refresh the cassettes, rather than replay them.
func recording() bool { return os.Getenv("EMBYFIN_TEST_RECORD") != "" }

// verifying reports whether to check the cassettes against the live
// providers without rewriting them.
func verifying() bool { return os.Getenv("EMBYFIN_TEST_VERIFY") != "" }

// configured reports whether the container environment is present.
func configured() bool {
	return os.Getenv("EMBYFIN_SERVER") != "" && os.Getenv("EMBYFIN_TOKEN") != "" && os.Getenv("EMBYFIN_BACKEND") != ""
}

// dataDir is the host path the container's /media is bind-mounted from, so
// a test can add files and rescan.
func dataDir() string { return filepath.Join(os.Getenv("EMBYFIN_TEST_DATA"), "media") }

// cassetteDir is where this backend's recordings live: Emby and Jellyfin ask
// the providers different questions, so each has its own set.
func cassetteDir() string { return filepath.Join("testdata", "cassettes", backend) }

// runSuite connects, starts the provider proxy, runs the tests, removes the
// libraries they created, and returns the exit code.
func runSuite(m *testing.M) int {
	if !configured() {
		return m.Run() // every test skips
	}
	backend = os.Getenv("EMBYFIN_BACKEND")
	adminID, aliceID, password = os.Getenv("EMBYFIN_TEST_ADMIN_ID"), os.Getenv("EMBYFIN_TEST_USER_ID"), os.Getenv("EMBYFIN_TEST_PASSWORD")

	var err error
	switch backend {
	case "emby":
		embyc, err = emby.New(os.Getenv("EMBYFIN_SERVER"), os.Getenv("EMBYFIN_TOKEN"))
	case "jellyfin":
		jfc, err = jf.New(os.Getenv("EMBYFIN_SERVER"), os.Getenv("EMBYFIN_TOKEN"))
	default:
		err = fmt.Errorf("EMBYFIN_BACKEND=%q: want emby or jellyfin", backend)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdk client:", err)
		return 1
	}
	if err := startProxy(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		return 1
	}

	code := m.Run()
	removeLibraries()
	stopProxy()

	// a replay miss means a test ran against a 502 rather than a recording,
	// so say so loudly even when the assertions happened to survive it
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

	return code
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
		IgnoreHosts: containerAddresses(),
	}
	if ca := os.Getenv("EMBYFIN_TEST_PROXY_CA"); ca != "" {
		opts.CACert, opts.CAKey = filepath.Join(ca, "ca.pem"), filepath.Join(ca, "ca.key")
	}
	p, err := providerproxy.New(opts)
	if err != nil {
		return err
	}
	proxy = p

	return nil
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

// removeLibraries deletes every library the suite created, after the last
// test. The fixtures are created lazily by whichever test runs first and
// shared by the rest, so a t.Cleanup on the creator would pull them out from
// under the others.
func removeLibraries() {
	ctx := context.Background()
	for _, l := range []libraryFixture{sdkMovies, sdkShows, sdkScratch} {
		var err error
		switch backend {
		case "emby":
			err = embyDeleteVirtualFolder(ctx, l.Name)
		case "jellyfin":
			_, err = jfc.RemoveVirtualFolder(ctx, jf.RemoveVirtualFolderOperationOptions{Name: l.Name, RefreshLibrary: new(false)})
		}
		if err != nil && !client.IsNotFound(err) {
			fmt.Fprintf(os.Stderr, "removing %s: %v\n", l.Name, err)
		}
	}
}

// embyLibraryID resolves a library name to its ItemId, "" when absent. Emby
// 4.10 identifies a library by Id and answers 500, "Unrecognized Guid
// format", to a Name.
func embyLibraryID(ctx context.Context, name string) (string, error) {
	folders, err := embyc.GetLibraryVirtualFoldersQuery(ctx, emby.GetLibraryVirtualFoldersQueryOperationOptions{})
	if err != nil {
		return "", err
	}
	for _, f := range folders.Model.Items {
		if f.Name == name {
			return f.ItemId, nil
		}
	}

	return "", nil
}

// embyDeleteVirtualFolder removes a library by name, a no-op when it is
// already gone.
func embyDeleteVirtualFolder(ctx context.Context, name string) error {
	id, err := embyLibraryID(ctx, name)
	if err != nil || id == "" {
		return err
	}
	_, err = embyc.PostLibraryVirtualFoldersDelete(ctx, emby.LibraryRemoveVirtualFolder{Id: id})

	return err
}

// skipUnlessEmby skips unless the Emby container is up, and returns the
// test's context.
func skipUnlessEmby(t *testing.T) context.Context {
	t.Helper()

	if embyc == nil {
		t.Skip("not an Emby run; run: eval \"$(EMBYFIN_TEST_BACKEND=emby scripts/testenv.sh up)\"")
	}

	return t.Context()
}

// skipUnlessJellyfin skips unless the Jellyfin container is up.
func skipUnlessJellyfin(t *testing.T) context.Context {
	t.Helper()

	if jfc == nil {
		t.Skip("not a Jellyfin run; run: eval \"$(EMBYFIN_TEST_BACKEND=jellyfin scripts/testenv.sh up)\"")
	}

	return t.Context()
}

// must unwraps a call that must not fail. Go only allows a multi-value call
// as a function's sole argument, so this cannot also take *testing.T - it
// panics instead, which the test framework reports as a failure. That is the
// right severity here: if the server will not answer, nothing downstream is
// meaningful.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}

// poll calls f every two seconds until it returns true or the deadline
// passes, and reports whether it did.
func poll(timeout time.Duration, f func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if f() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Second)
	}
}

// embyRetry500 runs a write again, two seconds apart, while the server
// answers 500: Emby saves items with a delete-and-insert inside a
// transaction, and a playlist or collection write that lands while a
// background refresh is rewriting one of its members fails a FOREIGN KEY
// constraint rather than waiting. Any other outcome is returned as is.
func embyRetry500(f func() error) error {
	var err error
	for range 15 {
		if err = f(); client.StatusCode(err) != 500 {
			return err
		}
		time.Sleep(2 * time.Second)
	}

	return err
}

// scratchDir lays out the throwaway movie the destructive test deletes: a
// copy of one fixture file under a folder of its own, no nfo, so the scan
// titles it from the folder and no provider is asked about it.
func scratchDir(t *testing.T) string {
	t.Helper()

	data := dataDir()
	if data == filepath.Join("", "media") {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	source := filepath.Join(data, "movies", "Princess Mononoke (1997)", "Princess Mononoke (1997).mp4")
	video, err := os.ReadFile(source) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatalf("reading a fixture to copy: %v", err)
	}
	dir := filepath.Join(data, "sdk-scratch", scratchTitle+" (1995)")
	// the mode MkdirAll and WriteFile are asked for is filtered by the process
	// umask, which on Linux leaves a directory the media server's own user
	// (uid 2 in Emby's image) cannot delete from, so the delete under test
	// fails; chmod is not filtered. Docker Desktop maps every file to the
	// container's user, which is why this only bites in CI.
	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	for p := dir; strings.HasPrefix(p, data) && p != data; p = filepath.Dir(p) {
		if err := os.Chmod(p, 0o777); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, scratchTitle+" (1995).mp4")
	if err := os.WriteFile(file, video, 0o666); err != nil { //nolint:gosec // same
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o666); err != nil { //nolint:gosec // same
		t.Fatal(err)
	}

	return filepath.Join(data, "sdk-scratch")
}

// isScanTask picks the library scan out of the task list on either server.
func isScanTask(key, name string) bool {
	return key == "RefreshLibrary" || strings.Contains(strings.ToLower(name), "scan media library")
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

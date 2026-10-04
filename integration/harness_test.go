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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/embyfin-mcp/lib/jf"
	"github.com/katbyte/embyfin-mcp/lib/testenv"
)

// scanPatience is how long a library scan is given to settle. Three minutes
// is plenty on a quiet machine and not always enough on a CI runner sharing
// itself with three other suites, where a music scan has come in just over.
const scanPatience = 6 * time.Minute

// editPatience is how long a change to an item is given to show up in
// what the server answers. Jellyfin applies a collection change a moment
// after it answers the request, so a read straight after it can miss it.
const editPatience = 30 * time.Second

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
	// Versions is how many of the films are a second file beside another in
	// its folder, which Jellyfin folds into that film and Emby lists as a
	// film of its own
	Versions int
	// TagAlbums is how many more albums Emby builds from the tags than there
	// are album folders (a track tagged with its album misspelt), and
	// ExtraEpisodes how many extras it takes for episodes (a featurette in a
	// season's Extras folder); Jellyfin builds albums from folders and keeps
	// a season's extras as extras
	TagAlbums, ExtraEpisodes int
}

// films is how many films a finished scan of the library lists on this
// backend.
func (l libraryFixture) films() int {
	if backend == "jellyfin" {
		return l.Movies - l.Versions
	}

	return l.Movies
}

// albums is how many albums a finished scan of the library lists on this
// backend.
func (l libraryFixture) albums() int {
	if backend == "jellyfin" {
		return l.Albums
	}

	return l.Albums + l.TagAlbums
}

// episodes is how many episodes a finished scan of the library lists on
// this backend.
func (l libraryFixture) episodes() int {
	if backend == "jellyfin" {
		return l.Episodes
	}

	return l.Episodes + l.ExtraEpisodes
}

// The libraries. Only the movie library has providers on: it is what the
// remote-image, remote-search and refresh tests need, and each library with
// providers on costs a cassette full of provider traffic.
var (
	sdkMovies = libraryFixture{Name: "SDK Movies", CollectionType: "movies", Folder: "/media/movies", Providers: true, Movies: 10}
	sdkShows  = libraryFixture{Name: "SDK Shows", CollectionType: "tvshows", Folder: "/media/shows", Series: 4, Episodes: 11}
	// sdkScratch sits over a folder the destructive test lays out itself,
	// so the delete has something to remove that nothing else relies on
	sdkScratch = libraryFixture{Name: "SDK Scratch", CollectionType: "movies", Folder: "/media/sdk-scratch", Movies: 1}
	// sdkMusic is what the artist, album, song and music genre routes have to
	// read: without it a fifth of each server's item surface cannot be called
	sdkMusic = libraryFixture{Name: "SDK Music", CollectionType: "music", Folder: "/media/music", Albums: 5, TagAlbums: 1, Songs: 20}
	// sdkMessyMovies and sdkMessyShows sit over the messy folders with the
	// fetchers off, for what the servers read off the files themselves: a
	// legacy codec, a language, a film held as two files, a disc kept whole,
	// a file holding two episodes
	sdkMessyMovies = libraryFixture{Name: "SDK Messy Movies", CollectionType: "movies", Folder: "/media/messy-movies", Movies: 14, Versions: 1}
	sdkMessyShows  = libraryFixture{Name: "SDK Messy Shows", CollectionType: "tvshows", Folder: "/media/messy-shows", Series: 12, Episodes: 30, ExtraEpisodes: 1}
	// sdkBulk sits over a folder the bulk delete test lays out itself
	sdkBulk = libraryFixture{Name: "SDK Bulk Delete", CollectionType: "movies", Folder: "/media/sdk-bulk", Movies: len(bulkTitles)}
)

// bulkTitles are the films the bulk delete test lays out, copies of one
// fixture file under the names of messy films the clean library does not hold
var bulkTitles = []string{"Interstellar (2014)", "Memento (2000)", "Cube (1997)", "Coyote vs. Acme (2026)"}

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
	{"Brüno", 2009, "18480", "tt0889583", "Comedy", "Larry Charles"},
	{"Limitless", 2011, "51876", "tt1219289", "Thriller", "Neil Burger"},
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
	{"The Expanse", 2015, "63639", "280619", map[int][]int{0: {1}, 1: {1, 2}}},
	{"Limitless", 2015, "62687", "295743", map[int][]int{1: {1}}},
}

// The fixture facts the assertions lean on.
const (
	alien       = "Alien"
	aliens      = "Aliens"
	bladeRunner = "Blade Runner"
	severance   = "Severance"
	ridleyScott = "Ridley Scott"
	// thirteenthFloor is the one clean film with a subtitle beside it, an
	// English .srt
	thirteenthFloor = "The Thirteenth Floor"
	// lyricTrack is the one fixture track with an .lrc sidecar beside it
	lyricTrack = "The Future We Built"
	// darkSide is an album whose artist has another in the fixtures, and
	// progressiveRock the genre the two share and no other artist has
	darkSide        = "The Dark Side of the Moon"
	progressiveRock = "Progressive Rock"
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
	proxy                      *testenv.Proxy
)

// runSuite connects, starts the provider proxy, runs the tests, removes the
// libraries they created, and returns the exit code.
func runSuite(m *testing.M) int {
	if !testenv.Configured() {
		return m.Run() // every test skips
	}
	backend = testenv.Backend()
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
	proxy, err = testenv.StartProxy(context.Background(), testenv.CassetteDir(backend))
	if err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		return 1
	}

	code := m.Run()
	removeLibraries()
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

	return code
}

// removeLibraries deletes every library the suite created, after the last
// test. The fixtures are created lazily by whichever test runs first and
// shared by the rest, so a t.Cleanup on the creator would pull them out from
// under the others. EMBYFIN_TEST_KEEP=1 leaves them in place, to look at what
// a run left behind.
func removeLibraries() {
	if os.Getenv("EMBYFIN_TEST_KEEP") != "" {
		return
	}
	ctx := context.Background()
	for _, l := range []libraryFixture{sdkMovies, sdkShows, sdkScratch, sdkMusic, sdkMessyMovies, sdkMessyShows, sdkBulk} {
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

// undoLater runs a call that puts back what a test changed once the test
// ends, and reports one that fails rather than dropping it: the change would
// be left for the tests after.
func undoLater(t *testing.T, what string, undo func() error) {
	t.Helper()
	t.Cleanup(func() {
		if err := undo(); err != nil {
			t.Errorf("cleaning up after the test, %s: %v", what, err)
		}
	})
}

// removeLater removes what a test made once the test ends, as undoLater
// does; what is already gone (a 404: a server can drop an emptied
// collection itself) needs no removing.
func removeLater(t *testing.T, what string, remove func() error) {
	t.Helper()
	undoLater(t, what, func() error {
		if err := remove(); err != nil && !client.IsNotFound(err) {
			return err
		}

		return nil
	})
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

// number is a season or episode number the SDK holds by pointer, or -1 when
// the server sent none: no number is -1, so a missing one fails every check
// of one rather than passing one of 0.
func number(n *int) int {
	if n == nil {
		return -1
	}

	return *n
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

	return layOut(t, "sdk-scratch", scratchTitle+" (1995)")
}

// layOut copies one fixture file into a folder of its own for each of the
// films named ("Title (Year)"), under a folder of the media tree the
// container sees as /media/<under>, and returns that folder on the host.
func layOut(t *testing.T, under string, films ...string) string {
	t.Helper()

	data := testenv.DataDir()
	if data == filepath.Join("", "media") {
		t.Skip("EMBYFIN_TEST_DATA is not set")
	}
	source := filepath.Join(data, "movies", "Princess Mononoke (1997)", "Princess Mononoke (1997).mp4")
	video, err := os.ReadFile(source) //nolint:gosec // a fixture under the test data dir
	if err != nil {
		t.Fatalf("reading a fixture to copy: %v", err)
	}
	for _, film := range films {
		dir := filepath.Join(data, under, film)
		// the mode MkdirAll and WriteFile are asked for is filtered by the
		// process umask, which on Linux leaves a directory the media server's
		// own user (uid 2 in Emby's image) cannot delete from, so a delete
		// under test fails; chmod is not filtered. Docker Desktop maps every
		// file to the container's user, which is why this only bites in CI.
		if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container reads it as another user
			t.Fatal(err)
		}
		for p := dir; strings.HasPrefix(p, data) && p != data; p = filepath.Dir(p) {
			if err := os.Chmod(p, 0o777); err != nil { //nolint:gosec // same
				t.Fatal(err)
			}
		}
		file := filepath.Join(dir, film+".mp4")
		if err := os.WriteFile(file, video, 0o666); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
		if err := os.Chmod(file, 0o666); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
	}

	return filepath.Join(data, under)
}

// isScanTask picks the library scan out of the task list on either server.
func isScanTask(key, name string) bool {
	return key == "RefreshLibrary" || strings.Contains(strings.ToLower(name), "scan media library")
}

package animelist

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A list in the shape Anime-Lists publishes, with invented ids: one series,
// the OVA and the film TVDB and TMDB fold into its specials, and an entry
// with a place of its own.
const sample = `<?xml version="1.0" encoding="utf-8"?>
<anime-list>
  <anime anidbid="9001" tvdbid="70001" defaulttvdbseason="1" episodeoffset="" tmdbtv="80001" tmdbseason="1" tmdboffset="" tmdbid="" imdbid="">
    <name>Zzyzx Senki</name>
    <mapping-list>
      <mapping anidbseason="0" tvdbseason="0">;1-1;2-2;</mapping>
    </mapping-list>
  </anime>
  <anime anidbid="9002" tvdbid="70001" defaulttvdbseason="0" episodeoffset="" tmdbtv="80001" tmdbseason="0" tmdboffset="4" tmdbid="" imdbid="">
    <name>Zzyzx Senki OVA</name>
    <mapping-list>
      <mapping anidbseason="1" tvdbseason="0">;1-5;2-6+7;3-0;</mapping>
    </mapping-list>
  </anime>
  <anime anidbid="9003" tvdbid="70001" defaulttvdbseason="0" episodeoffset="2" tmdbtv="80001" tmdbseason="0" tmdboffset="2" tmdbid="" imdbid="tt9000003">
    <name>Zzyzx Senki: The Movie</name>
  </anime>
  <anime anidbid="9004" tvdbid="movie" defaulttvdbseason="" episodeoffset="" tmdbtv="" tmdbseason="" tmdboffset="" tmdbid="12345" imdbid="">
    <name>Zzyzx Gaiden</name>
  </anime>
  <anime anidbid="9006" tvdbid="70005" defaulttvdbseason="0" episodeoffset="" tmdbtv="80005" tmdbseason="0" tmdboffset="" tmdbid="" imdbid="">
    <name>Zzyzx Special</name>
  </anime>
  <anime anidbid="9005" tvdbid="70005" defaulttvdbseason="0" episodeoffset="" tmdbtv="" tmdbseason="" tmdboffset="" tmdbid="" imdbid="">
    <name>Zzyzx Recap</name>
    <mapping-list>
      <mapping anidbseason="1" tvdbseason="0" start="1" end="3" offset="10"/>
    </mapping-list>
  </anime>
</anime-list>`

func TestParse(t *testing.T) {
	t.Parallel()

	l, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if l.Len() != 6 {
		t.Fatalf("len = %d", l.Len())
	}

	// a series of its own is in nobody's specials
	if tvdb, tmdb := l.Entry("9001").Specials(); tvdb != "" || tmdb != "" {
		t.Errorf("the series is held as specials of %q, %q", tvdb, tmdb)
	}
	// the OVA is, on both, and its episodes map to TVDB's specials one by
	// one: one of them is two of TVDB's, and one TVDB does not have
	ova := l.Entry("9002")
	if tvdb, tmdb := ova.Specials(); tvdb != "70001" || tmdb != "80001" {
		t.Errorf("the OVA is held as specials of %q, %q", tvdb, tmdb)
	}
	if first, numbers, known := ova.Place("tvdb"); first != 5 || !slices.Equal(numbers, []int{5, 6, 7}) || !known {
		t.Errorf("the OVA on TVDB = %d, %v, %v", first, numbers, known)
	}
	// TMDB gives only where it starts
	if first, numbers, known := ova.Place("tmdb"); first != 5 || numbers != nil || !known {
		t.Errorf("the OVA on TMDB = %d, %v, %v", first, numbers, known)
	}
	// a range mapping, and an offset
	if first, numbers, _ := l.Entry("9005").Place("tvdb"); first != 11 || !slices.Equal(numbers, []int{11, 12, 13}) {
		t.Errorf("the recap on TVDB = %d, %v", first, numbers)
	}
	if first, _, _ := l.Entry("9003").Place("tvdb"); first != 3 {
		t.Errorf("the film starts at special %d on TVDB, want 3", first)
	}
	// an empty TVDB offset is none, but an empty TMDB one is not knowing
	if first, _, known := l.Entry("9006").Place("tvdb"); first != 1 || !known {
		t.Errorf("an empty TVDB offset = %d, %v", first, known)
	}
	if _, _, known := l.Entry("9006").Place("tmdb"); known {
		t.Error("an empty TMDB offset was taken for a place")
	}

	// the series' specials hold the film and the OVA, in the order they sit
	var got []string
	for _, e := range l.SpecialsIn("tvdb", "70001") {
		got = append(got, e.AniDB)
	}
	if !slices.Equal(got, []string{"9003", "9002"}) {
		t.Errorf("TVDB 70001's specials hold %v", got)
	}
	if n := len(l.SpecialsIn("tmdb", "80001")); n != 2 {
		t.Errorf("TMDB 80001's specials hold %d entries", n)
	}

	// the show's own specials and the OVA's take numbers; the film's, given
	// only by where it starts, do not
	for n, want := range map[int]bool{1: true, 2: true, 3: false, 5: true, 7: true, 8: false} {
		if got := l.Claimed("tvdb", "70001", n); got != want {
			t.Errorf("TVDB 70001 special %d claimed = %v, want %v", n, got, want)
		}
	}

	// a word where the list has no TVDB series is not an id
	if gaiden := l.Entry("9004"); gaiden.TVDB != "" || gaiden.TMDBMovie != "12345" || gaiden.Name != "Zzyzx Gaiden" {
		t.Errorf("the film with no TVDB series = %+v", gaiden)
	}
	if l.Entry("404") != nil {
		t.Error("an entry the list does not have")
	}

	if _, err := Parse(strings.NewReader("<anime-list><anime")); err == nil {
		t.Error("a broken list read without complaint")
	}
}

func TestLoaderReadsAFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "anime-list.xml")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := NewLoader(path, nil).Load(context.Background())
	if err != nil || l.Len() != 6 {
		t.Fatalf("len %v, err %v", l, err)
	}

	if _, err := NewLoader(filepath.Join(t.TempDir(), "missing.xml"), nil).Load(context.Background()); err == nil {
		t.Error("a missing file loaded")
	}
	if got := NewLoader("", nil).Source(); got != DefaultSource {
		t.Errorf("default source = %q", got)
	}
}

// The list is megabytes and changes a few times a week: it is fetched once
// a day, and an old copy stands in when a new one cannot be fetched.
func TestLoaderKeepsTheListForADay(t *testing.T) {
	t.Parallel()

	var fetches atomic.Int32
	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		if failing.Load() {
			http.Error(w, "down", http.StatusBadGateway)

			return
		}
		_, _ = w.Write([]byte(sample))
	}))
	t.Cleanup(srv.Close)

	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	l := NewLoader(srv.URL, nil)
	l.now = func() time.Time { return now }
	ctx := context.Background()

	for range 3 {
		if list, err := l.Load(ctx); err != nil || list.Len() != 6 {
			t.Fatalf("load: %v", err)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("fetched %d times in an hour, want once", n)
	}

	// a day on, it is fetched again; the source failing leaves the old copy
	now = now.Add(25 * time.Hour)
	failing.Store(true)
	if list, err := l.Load(ctx); err != nil || list.Len() != 6 {
		t.Errorf("with the source down: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Errorf("fetched %d times, want a second try after a day", n)
	}

	// and with no old copy, the failure is the answer
	if _, err := NewLoader(srv.URL, nil).Load(ctx); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("a failed first fetch = %v", err)
	}
}

// clock is a time a test moves on by hand, safe to read from any goroutine.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.at
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.at = c.at.Add(d)
}

// A read that fails is remembered: until a retry is due, calls answer the
// list read before at once rather than each waiting on the source again (a
// minute apiece when it hangs).
func TestLoaderBacksOffAfterAFailedRead(t *testing.T) {
	t.Parallel()

	var fetches atomic.Int32
	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		if failing.Load() {
			http.Error(w, "down", http.StatusBadGateway)

			return
		}
		_, _ = w.Write([]byte(sample))
	}))
	t.Cleanup(srv.Close)

	clk := &clock{at: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	l := NewLoader(srv.URL, nil)
	l.now = clk.now
	ctx := context.Background()
	load := func(wantFetches int32) {
		t.Helper()
		if list, err := l.Load(ctx); err != nil || list.Len() != 6 {
			t.Fatalf("load: %v", err)
		}
		if n := fetches.Load(); n != wantFetches {
			t.Errorf("fetched %d times, want %d", n, wantFetches)
		}
	}

	load(1)
	// a day on, the source is down: one try, then the old list meanwhile
	clk.add(25 * time.Hour)
	failing.Store(true)
	load(2)
	load(2)
	clk.add(30 * time.Minute)
	load(2)
	// the retry comes due, fails again, and waits again
	clk.add(31 * time.Minute)
	load(3)
	load(3)
	// the source back, the next retry reads the new list, which then lasts a day
	failing.Store(false)
	clk.add(61 * time.Minute)
	load(4)
	clk.add(23 * time.Hour)
	load(4)
}

// While a list already held is read again, calls meanwhile answer it rather
// than queue behind the read.
func TestLoaderServesTheOldListDuringARead(t *testing.T) {
	t.Parallel()

	var fetches atomic.Int32
	var slow atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		if slow.Load() {
			close(entered)
			<-release
		}
		_, _ = w.Write([]byte(sample))
	}))
	t.Cleanup(srv.Close)

	clk := &clock{at: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	l := NewLoader(srv.URL, nil)
	l.now = clk.now
	ctx := context.Background()
	if _, err := l.Load(ctx); err != nil {
		t.Fatal(err)
	}

	clk.add(25 * time.Hour)
	slow.Store(true)
	reading := make(chan error, 1)
	go func() {
		_, err := l.Load(ctx)
		reading <- err
	}()
	<-entered

	meanwhile := make(chan error, 1)
	go func() {
		list, err := l.Load(ctx)
		if err == nil && list.Len() != 6 {
			err = errors.New("not the old list")
		}
		meanwhile <- err
	}()
	select {
	case err := <-meanwhile:
		if err != nil {
			t.Errorf("a call during the read = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("a call during the read waited on it")
	}
	close(release)
	if err := <-reading; err != nil {
		t.Errorf("the read = %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Errorf("fetched %d times, want the one read again only", n)
	}
}

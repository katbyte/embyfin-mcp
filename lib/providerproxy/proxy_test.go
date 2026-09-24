package providerproxy

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// clientThrough returns an http.Client that reaches the given proxy and
// accepts its minted certificates, the way the container does with
// SSL_CERT_FILE pointing at the proxy's CA.
func clientThrough(t *testing.T, p *Proxy) *http.Client {
	t.Helper()

	proxyURL, err := url.Parse("http://" + p.Addr())
	if err != nil {
		t.Fatal(err)
	}

	return &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the proxy mints its own certs on purpose
		},
	}
}

func writeCassette(t *testing.T, dir string, c cassette) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hostFile(c.Host)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A recorded request must come back through the CONNECT tunnel byte for byte.
func TestReplayServesRecording(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCassette(t, dir, cassette{
		Host: "api.themoviedb.org",
		Interactions: []*interaction{{
			Key:     "GET api.themoviedb.org/authors?name=Alien",
			Method:  "GET",
			Host:    "api.themoviedb.org",
			Path:    "/authors",
			Query:   "name=Alien",
			Status:  200,
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `[{"asin":"B000AP9A6K","name":"Isaac Asimov"}]`,
		}},
	})

	p, err := New(Options{CassetteDir: dir, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.themoviedb.org/authors?name=Alien", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(string(body), "Isaac Asimov") {
		t.Errorf("body = %q", body)
	}
	if misses := p.Misses(); len(misses) != 0 {
		t.Errorf("misses = %v, want none", misses)
	}
}

// A client in the same process reaches the recordings through Transport,
// trusting the proxy's own certificate rather than skipping the check.
func TestTransportTrustsTheProxy(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCassette(t, dir, cassette{
		Host: "api.themoviedb.org",
		Interactions: []*interaction{{
			Key: "GET api.themoviedb.org/3/movie/550", Method: "GET", Host: "api.themoviedb.org", Path: "/3/movie/550",
			Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"id":550}`,
		}},
	})
	p, err := New(Options{CassetteDir: dir, Addr: "127.0.0.1:0", Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.themoviedb.org/3/movie/550", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: p.Transport()}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if body, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK || string(body) != `{"id":550}` {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
}

// A request with no recording must fail loudly rather than look like an empty
// but successful response, which would let a test pass for the wrong reason.
func TestReplayMissIsLoud(t *testing.T) {
	t.Parallel()

	p, err := New(Options{CassetteDir: t.TempDir(), Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.tmdb.com/1.0/catalog/products?keywords=nothing", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	misses := p.Misses()
	if len(misses) != 1 {
		t.Fatalf("misses = %v, want one", misses)
	}
	if !strings.Contains(misses[0], "api.tmdb.com/1.0/catalog/products") {
		t.Errorf("miss does not name the request: %q", misses[0])
	}
}

// Query parameter order must not matter, or a cassette would miss on a request
// that is the same in every way that affects the response.
func TestKeyIsOrderIndependent(t *testing.T) {
	t.Parallel()

	a := key("get", "API.TMDB.com", "/1.0/catalog", url.Values{"b": {"2"}, "a": {"1"}})
	b := key("GET", "api.tmdb.com", "/1.0/catalog", url.Values{"a": {"1"}, "b": {"2"}})
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
}

// A body over the cap is elided rather than committed, and a binary one is
// stored base64 so the cassette stays valid JSON.
func TestBodyStorage(t *testing.T) {
	t.Parallel()

	var big interaction
	big.setBody(make([]byte, maxBodyBytes+1), "text/xml")
	if !big.Elided || big.ElidedSize != maxBodyBytes+1 {
		t.Errorf("oversized body not elided: %+v", big)
	}

	// audio is elided however small, so no episode audio reaches the repository
	var audio interaction
	audio.setBody([]byte("ID3short"), "audio/mpeg")
	if !audio.Elided {
		t.Errorf("audio body not elided: %+v", audio)
	}

	// an elided image still has to replay as something decodable, or the
	// server will not accept it as a cover
	var jpeg interaction
	jpeg.setBody(make([]byte, 500<<10), "image/jpeg")
	if !jpeg.Elided {
		t.Fatalf("image not elided: %+v", jpeg)
	}
	if got := jpeg.bytes(); len(got) < 4 || got[0] != 0xff || got[1] != 0xd8 {
		t.Errorf("elided image did not replay as a JPEG: %v", got[:min(4, len(got))])
	}

	var png interaction
	png.setBody(make([]byte, 10), "image/png")
	if got := png.bytes(); len(got) < 4 || got[1] != 'P' {
		t.Errorf("elided png did not replay as a PNG: %v", got[:min(4, len(got))])
	}

	// but a large RSS feed must survive intact or it will not parse on replay
	feed := make([]byte, 900<<10)
	for n := range feed {
		feed[n] = 'x'
	}
	var rss interaction
	rss.setBody(feed, "application/rss+xml")
	if rss.Elided || len(rss.Body) != len(feed) {
		t.Errorf("large feed was not kept intact: elided=%v len=%d", rss.Elided, len(rss.Body))
	}

	// bytes that are not text are kept as base64 (a server needs them back
	// intact): a Latin-1 list under octet-stream too. A blob under that type
	// holding NUL bytes is a plugin or an installer, elided like media
	var binary interaction
	binary.setBody([]byte("Studio Caf\xe9\n"), "application/octet-stream")
	if binary.BodyBase64 == "" || binary.Body != "" || binary.Elided {
		t.Errorf("binary body not base64: %+v", binary)
	}
	var blob interaction
	blob.setBody([]byte{0x4d, 0x5a, 0x90, 0x00}, "application/octet-stream")
	if !blob.Elided || blob.BodyBase64 != "" {
		t.Errorf("an octet-stream blob was kept: %+v", blob)
	}

	var text interaction
	text.setBody([]byte(`{"ok":true}`), "application/json")
	if text.Body != `{"ok":true}` || text.BodyBase64 != "" {
		t.Errorf("text body not stored as text: %+v", text)
	}
}

// Record mode against a local upstream, so the recording path is checked on
// every run: a gzipped JSON answer is stored decoded and readable, a plugin
// binary is elided, and what was written replays.
func TestRecordDecodesGzipAndElidesBinaries(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3/authentication/token/new":
			w.Header().Set("Content-Type", "application/json;charset=utf-8")
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Authentication-Callback", "https://www.themoviedb.org/authenticate/tok-1")
			zw := gzip.NewWriter(w)
			_, _ = zw.Write([]byte(`{"success":true,"request_token":"tok-1"}`))
			_ = zw.Close()
		case "/packageFiles/Plugin.dll":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte{0x4d, 0x5a, 0x90, 0x00, 0xff, 0xfe})
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("Studio One\nStudio Two\n"))
		}
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	p, err := New(Options{Mode: Record, CassetteDir: dir, Logger: log.New(io.Discard, "", 0), RedactBodyFields: []string{"request_token"}})
	if err != nil {
		t.Fatal(err)
	}
	client := clientThrough(t, p)
	get := func(path string) string {
		t.Helper()
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL+path, http.NoBody)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Accept-Encoding", "gzip") // as a server asking for gzip would
		resp, doErr := client.Do(req)
		if doErr != nil {
			t.Fatal(doErr)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return string(body)
	}
	if got := get("/3/authentication/token/new"); strings.Contains(got, "tok-1") {
		t.Errorf("the recorded answer still carries the token: %s", got)
	}
	get("/packageFiles/Plugin.dll")
	get("/studios.txt")
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	host := strings.TrimPrefix(upstream.URL, "http://")
	raw, err := os.ReadFile(filepath.Join(dir, hostFile(host))) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	var c cassette
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	byPath := map[string]*interaction{}
	for _, i := range c.Interactions {
		byPath[i.Path] = i
	}
	token := byPath["/3/authentication/token/new"]
	if token == nil || token.BodyBase64 != "" || token.Body != `{"success":true,"request_token":"`+redactedValue+`"}` || token.Headers["Content-Encoding"] != "" {
		t.Errorf("gzipped JSON = %+v, want it decoded, redacted and stored as text", token)
	}
	if cb := token.Headers["Authentication-Callback"]; strings.Contains(cb, "tok-1") {
		t.Errorf("the header still carries the token: %s", cb)
	}
	if dll := byPath["/packageFiles/Plugin.dll"]; dll == nil || !dll.Elided || dll.ElidedSize != 6 {
		t.Errorf("a binary = %+v, want it elided", dll)
	}
	if txt := byPath["/studios.txt"]; txt == nil || txt.Elided || txt.Body != "Studio One\nStudio Two\n" {
		t.Errorf("a text list under octet-stream = %+v, want it kept", txt)
	}

	// and it replays, decoded, without the upstream
	upstream.Close()
	replay, err := New(Options{CassetteDir: dir, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+host+"/3/authentication/token/new", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, replay).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `"success":true`) || len(replay.Misses()) != 0 {
		t.Errorf("replayed = %s, misses %v", body, replay.Misses())
	}
}

// Record fills in only what the cassettes lack, so recording a new test's
// lookups leaves every other recording as it was; Rerecord (make record)
// refreshes what is recorded too: a request is fetched live the first time
// the proxy sees it, its recording replaced, and repeats in the same run are
// served that fresh answer without another fetch.
func TestRecordFillsInAndRerecordRefreshes(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	fetched := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"runtime":118}`)
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")
	fetches := func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return fetched[path]
	}

	dir := t.TempDir()
	writeCassette(t, dir, cassette{Host: host, Interactions: []*interaction{{
		Key: "GET " + host + "/3/movie/348", Method: "GET", Host: host, Path: "/3/movie/348",
		Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"runtime":117}`,
	}}})
	get := func(p *Proxy, path string) string {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, upstream.URL+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := clientThrough(t, p).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	recorded := func() map[string][]string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, hostFile(host))) //nolint:gosec // a path this test wrote
		if err != nil {
			t.Fatal(err)
		}
		var c cassette
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		bodies := map[string][]string{}
		for _, i := range c.Interactions {
			bodies[i.Path] = append(bodies[i.Path], i.Body)
		}
		return bodies
	}

	// Record: the recording answers, and only the request it lacks goes out
	p, err := New(Options{Mode: Record, CassetteDir: dir, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	if got := get(p, "/3/movie/348"); got != `{"runtime":117}` || fetches("/3/movie/348") != 0 {
		t.Errorf("Record answered a recorded request with %s after %d fetches, want the recording", got, fetches("/3/movie/348"))
	}
	if got := get(p, "/3/movie/78"); !strings.Contains(got, `"runtime":118`) || fetches("/3/movie/78") != 1 {
		t.Errorf("Record answered a new request with %s", got)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := recorded(); !slices.Equal(got["/3/movie/348"], []string{`{"runtime":117}`}) || len(got["/3/movie/78"]) != 1 {
		t.Errorf("after Record the cassette holds %v", got)
	}

	// Rerecord: each request fetched once, the recording replaced in place
	p, err = New(Options{Mode: Rerecord, CassetteDir: dir, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := get(p, "/3/movie/348"); !strings.Contains(got, `"runtime":118`) {
			t.Errorf("Rerecord answered %s, want the fresh answer", got)
		}
	}
	if n := fetches("/3/movie/348"); n != 1 {
		t.Errorf("Rerecord fetched a request %d times in one run, want once", n)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := recorded(); len(got["/3/movie/348"]) != 1 || !strings.Contains(got["/3/movie/348"][0], `"runtime":118`) || len(got["/3/movie/78"]) != 1 {
		t.Errorf("after Rerecord the cassette holds %v, want each request once, refreshed", got)
	}
}

// The suites run the proxy in Record mode whenever EMBYFIN_TEST_RECORD is
// set; set to all, as make record sets it, that is a full refresh. (Not
// parallel: it sets the environment.)
func TestRecordAllInTheEnvironmentRerecords(t *testing.T) {
	for value, want := range map[string]Mode{"all": Rerecord, "ALL": Rerecord, "1": Record} {
		t.Setenv("EMBYFIN_TEST_RECORD", value)
		p, err := New(Options{Mode: Record, CassetteDir: t.TempDir(), Addr: "127.0.0.1:0", Logger: log.New(io.Discard, "", 0)})
		if err != nil {
			t.Fatal(err)
		}
		if p.mode != want {
			t.Errorf("EMBYFIN_TEST_RECORD=%s runs in mode %d, want %d", value, p.mode, want)
		}
		_ = p.Close()
	}
	// and it never turns a replay into a recording
	t.Setenv("EMBYFIN_TEST_RECORD", "all")
	p, err := New(Options{CassetteDir: t.TempDir(), Addr: "127.0.0.1:0", Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	if p.mode != Replay {
		t.Errorf("a replay proxy runs in mode %d", p.mode)
	}
	_ = p.Close()
}

// Record mode against a real provider. Off by default so `go test ./...` stays
// hermetic; this is the check that the recording path still works when a
// cassette needs refreshing.
//
//	EMBYFIN_TEST_PROVIDERS_LIVE=1 go test ./lib/providerproxy/ -run Record -v
func TestRecordAgainstRealProvider(t *testing.T) {
	t.Parallel()

	if os.Getenv("EMBYFIN_TEST_PROVIDERS_LIVE") == "" {
		t.Skip("set EMBYFIN_TEST_PROVIDERS_LIVE=1 to record against the real providers")
	}

	dir := t.TempDir()
	p, err := New(Options{Mode: Record, CassetteDir: dir, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.themoviedb.org/authors?name=Isaac%20Asimov", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	// and the cassette it wrote must replay without touching the network
	raw, err := os.ReadFile(filepath.Join(dir, "api.themoviedb.org.json")) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatalf("no cassette written: %v", err)
	}
	if !strings.Contains(string(raw), "Asimov") {
		t.Errorf("cassette does not contain the response: %s", raw)
	}

	replay, err := New(Options{CassetteDir: dir, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()

	req2, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.themoviedb.org/authors?name=Isaac%20Asimov", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := clientThrough(t, replay).Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if !bytes.Equal(body, body2) {
		t.Error("replayed body differs from the recorded one")
	}
	if misses := replay.Misses(); len(misses) != 0 {
		t.Errorf("replay missed: %v", misses)
	}
}

// A redacted parameter is neither keyed on nor stored, so an operator's own
// TMDB key replays against a cassette recorded with someone else's.
func TestRedactQueryKeysWithoutTheSecret(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCassette(t, dir, cassette{
		Host: "api.themoviedb.org",
		Interactions: []*interaction{{
			Key:    "GET api.themoviedb.org/3/movie/348",
			Method: "GET", Host: "api.themoviedb.org", Path: "/3/movie/348",
			Status: 200, Headers: map[string]string{"Content-Type": "application/json"},
			Body: `{"runtime":117}`,
		}},
	})
	p, err := New(Options{CassetteDir: dir, Addr: "127.0.0.1:0", RedactQuery: []string{"api_key"}, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.themoviedb.org/3/movie/348?api_key=someone-elses", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := clientThrough(t, p).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "117") {
		t.Errorf("redacted request missed the cassette: %d %s", resp.StatusCode, body)
	}
	if m := p.Misses(); len(m) != 0 {
		t.Errorf("misses = %v", m)
	}
}

// A provider's login answer must not reach the cassette with its token in it,
// and everything around the token must survive byte for byte.
func TestRedactJSONFields(t *testing.T) {
	t.Parallel()

	const login = `{"status":"success","data":{"token":"eyJhbGciOiJSUzI1NiJ9.payload.sig"}}`
	got, secrets := redactJSONFields(login, []string{"token"})
	if strings.Contains(got, "eyJ") {
		t.Errorf("the token survived: %s", got)
	}
	if want := `{"status":"success","data":{"token":"` + redactedValue + `"}}`; got != want {
		t.Errorf("redacted = %s, want %s", got, want)
	}
	if !slices.Equal(secrets, []string{"eyJhbGciOiJSUzI1NiJ9.payload.sig"}) {
		t.Errorf("secrets = %v, want the token, so a header carrying it can be scrubbed too", secrets)
	}

	// the same value in a header goes too: TMDB's new request token comes
	// back in the body and in an Authentication-Callback link
	i := &interaction{Body: `{"request_token":"abc123"}`, Headers: map[string]string{"Authentication-Callback": "https://www.themoviedb.org/authenticate/abc123", "Server": "openresty"}}
	i.redact([]string{"request_token"})
	if strings.Contains(i.Body, "abc123") || strings.Contains(i.Headers["Authentication-Callback"], "abc123") || i.Headers["Server"] != "openresty" {
		t.Errorf("redact = %+v", i)
	}

	// a value carrying an escaped quote, a field that is not named, a body
	// that is not JSON, and no fields at all
	for _, tc := range []struct {
		name, body, want string
		fields           []string
	}{
		{"escaped quote", `{"token":"a\"b","keep":"x"}`, `{"token":"` + redactedValue + `","keep":"x"}`, []string{"token"}},
		{"spacing kept", `{"token" : "abc"}`, `{"token" : "` + redactedValue + `"}`, []string{"token"}},
		{"other fields left alone", `{"apikey":"abc"}`, `{"apikey":"abc"}`, []string{"token"}},
		{"not json", `plain text with token: abc`, `plain text with token: abc`, []string{"token"}},
		{"no fields named", `{"token":"abc"}`, `{"token":"abc"}`, nil},
	} {
		if got, _ := redactJSONFields(tc.body, tc.fields); got != tc.want {
			t.Errorf("%s: redacted = %s, want %s", tc.name, got, tc.want)
		}
	}
}

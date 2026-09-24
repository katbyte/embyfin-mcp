package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools end to end against a canned media server: the request a handler
// builds and the answer it projects, without a container. The live suite
// proves the canned shapes match a real server; these pin the behaviour the
// fixtures there cannot reach - here, the paged movie runtime audit against
// a TMDB that answers, which the live suite skips without a key.

// fakeServer is a canned MediaBrowser API: routes on a ServeMux plus a
// record of every request the tools made to it.
type fakeServer struct {
	mux *http.ServeMux
	srv *httptest.Server
	// jellyfin makes session build a Jellyfin client for it: the two servers
	// answer some questions differently, and a canned one that is always Emby
	// hides that.
	jellyfin bool

	mu   sync.Mutex
	seen []request
}

type request struct {
	Method, Path, Query string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()

	f := &fakeServer{mux: http.NewServeMux()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, request{r.Method, r.URL.Path, r.URL.RawQuery})
		f.mu.Unlock()
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)

	return f
}

// reset forgets the calls seen so far, for a test that cares about what one
// call asked the server for rather than what every call did.
func (f *fakeServer) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.seen = nil
}

// requests returns the calls made to a path, in order.
func (f *fakeServer) requests(path string) []request {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []request
	for _, r := range f.seen {
		if r.Path == path {
			out = append(out, r)
		}
	}

	return out
}

// session connects an in-memory MCP client to a server registering every
// tool against the fake, with the given options.
func session(t *testing.T, f *fakeServer, opts Options) *mcp.ClientSession {
	t.Helper()

	backend := embyfin.Emby
	if f.jellyfin {
		backend = embyfin.Jellyfin
	}
	client, err := embyfin.New(backend, f.srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	opts.Toolsets = []string{"all"}
	if _, err := RegisterAll(srv, client, opts); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// runtimeAudit calls audit_runtime and returns its structured result.
func runtimeAudit(t *testing.T, cs *mcp.ClientSession, args map[string]any) map[string]any {
	t.Helper()

	const name = "audit_runtime"
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		t.Fatalf("%s: %s", name, strings.Join(msgs, "; "))
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s: structured content is %T", name, res.StructuredContent)
	}

	return out
}

// number pulls a JSON number out of a decoded field.
func number(t *testing.T, v any, field string) int {
	t.Helper()

	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s is %T, want a number", field, v)
	}

	return int(f)
}

// findingNames lists the names in an audit's worklist, checking each field
// carries a detail from the provider comparison.
func findingNames(t *testing.T, out map[string]any) []string {
	t.Helper()

	raw, ok := out["findings"].([]any)
	if !ok {
		t.Fatalf("findings is %T, want a list", out["findings"])
	}
	names := make([]string, 0, len(raw))
	for _, row := range raw {
		m, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("finding is %T, want an object", row)
		}
		name, ok := m["name"].(string)
		if !ok {
			t.Fatalf("finding name is %T, want a string", m["name"])
		}
		names = append(names, name)
		if d, ok := m["detail"].(string); !ok || !strings.Contains(d, "TMDB says") {
			t.Errorf("detail = %v", m["detail"])
		}
	}

	return names
}

// rewrite is a transport that sends every request to a local server in place
// of the host the client asked for.
type rewrite struct{ target *url.URL }

func (rw rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme, clone.URL.Host = rw.target.Scheme, rw.target.Host

	return http.DefaultTransport.RoundTrip(clone)
}

// tmdbTransport routes the tools' own TMDB calls to a canned server keyed by
// movie id.
func tmdbTransport(t *testing.T, runtimes map[string]int) http.RoundTripper {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/3/movie/")
		minutes, ok := runtimes[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"runtime":%d}`, minutes)
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return rewrite{target}
}

// movieRows builds an /Items page of films, each n minutes long with a tmdb
// id when it has one.
func movieRows(films []struct {
	name, tmdb string
	minutes    int
}, start, limit int,
) string {
	// the wire names, as the server spells them
	type item struct {
		ID             string            `json:"Id"`
		Name           string            `json:"Name"`
		Type           string            `json:"Type"`
		Path           string            `json:"Path"`
		ProviderIDs    map[string]string `json:"ProviderIds,omitempty"`
		RunTimeTicks   int64             `json:"RunTimeTicks"`
		ProductionYear int               `json:"ProductionYear"`
	}
	page := []item{}
	for i := start; i < len(films) && i < start+limit; i++ {
		f := films[i]
		it := item{ID: strconv.Itoa(i + 1), Name: f.name, Type: "Movie", Path: "/m/" + f.name + ".mp4", RunTimeTicks: int64(f.minutes) * 600_000_000, ProductionYear: 2000}
		if f.tmdb != "" {
			it.ProviderIDs = map[string]string{"Tmdb": f.tmdb}
		}
		page = append(page, it)
	}
	b, _ := json.Marshal(map[string]any{"Items": page, "TotalRecordCount": len(films)})

	return string(b)
}

// The movie runtime audit pages through the library, asks TMDB once per
// matched film, skips the unmatched, reports the ones that are off, and
// hands back where to continue when the lookup budget runs out.
func TestAuditRuntimeMoviesPages(t *testing.T) {
	t.Parallel()

	films := []struct {
		name, tmdb string
		minutes    int
	}{
		{"Alien", "348", 117},         // right
		{"Princess Mononoke", "", 60}, // unmatched: skipped
		{"Arrival", "329865", 30},     // a quarter of the film
		{"Blade Runner", "78", 117},   // right
		{"Interstellar", "157336", 1}, // a minute of it
		{"Unknown", "1", 100},         // TMDB has no runtime: nothing to compare
	}
	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Movies","CollectionType":"movies","ItemId":"lib"}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("Limit"))
		_, _ = io.WriteString(w, movieRows(films, start, limit))
	})
	cs := session(t, f, Options{TMDBKey: "k", ProviderTransport: tmdbTransport(t, map[string]int{"348": 117, "329865": 116, "78": 117, "157336": 169, "1": 0})})

	out := runtimeAudit(t, cs, map[string]any{"library": "Movies", "types": "Movie"})
	if got := number(t, out["total_findings"], "total_findings"); got != 2 {
		t.Errorf("total_findings = %v, want 2: %v", got, out["findings"])
	}
	if got := number(t, out["items_scanned"], "items_scanned"); got != 6 {
		t.Errorf("items_scanned = %v, want 6", got)
	}
	if names := findingNames(t, out); strings.Join(names, ",") != "Arrival,Interstellar" {
		t.Errorf("findings = %v", names)
	}
	if _, ok := out["next_offset"]; ok {
		t.Error("a finished sweep still points at a next page")
	}
	if q := f.requests("/Items")[0].Query; !strings.Contains(q, "ParentId=lib") || !strings.Contains(q, "IncludeItemTypes=Movie") {
		t.Errorf("the sweep did not scope to the library: %s", q)
	}

	// two lookups per call: Alien and Arrival, then continue from the third
	out = runtimeAudit(t, cs, map[string]any{"library": "Movies", "types": "Movie", "max_lookups": 2})
	if got := number(t, out["total_findings"], "total_findings"); got != 1 {
		t.Errorf("first page total_findings = %v, want 1 (Arrival)", got)
	}
	next := number(t, out["next_offset"], "next_offset")
	if next != 3 {
		t.Fatalf("next_offset = %v, want 3", next)
	}
	out = runtimeAudit(t, cs, map[string]any{"library": "Movies", "types": "Movie", "offset": next})
	if got := number(t, out["total_findings"], "total_findings"); got != 1 {
		t.Errorf("second page total_findings = %v, want 1 (Interstellar)", got)
	}
	// a tolerance wide enough finds nothing
	out = runtimeAudit(t, cs, map[string]any{"library": "Movies", "types": "Movie", "tolerance_percent": 100})
	if got := number(t, out["total_findings"], "total_findings"); got != 0 {
		t.Errorf("tolerance 100 found %v", got)
	}
}

// Without a key the movie mode is refused with the fix in the message, and
// the tool says so in its description.
func TestAuditRuntimeMoviesNeedsAKey(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[],"TotalRecordCount":0}`)
	})
	cs := session(t, f, Options{})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "audit_runtime", Arguments: map[string]any{"types": "Movie"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("movie mode ran without a TMDB key")
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); !ok || !strings.Contains(tc.Text, "EMBYFIN_TMDB_TOKEN") {
		t.Errorf("the refusal does not say how to fix it: %v", res.Content)
	}

	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		if tool.Name == "audit_runtime" && !strings.Contains(tool.Description, "movie mode disabled") {
			t.Errorf("the description does not say movie mode is off: %s", tool.Description)
		}
	}
}

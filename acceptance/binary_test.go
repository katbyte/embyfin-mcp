//go:build integration

package acceptance

// The binary itself. Everything else in this suite drives the tools through
// an in-memory session, which runs the same server code but not the command
// around it: these start embyfin-mcp as a client does, over stdio and over
// HTTP, and check what only the process can get wrong. Its flags and
// environment reaching the server, a stray line on stdout breaking the stdio
// stream, the bearer check on the endpoint, shutting down cleanly, and
// refusing to start with a message that says why.

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildBinary builds embyfin-mcp from this checkout. Under a coverage run it
// is built with coverage on and writes its counters beside the suite's own,
// so make cover counts what the process ran.
func buildBinary(t *testing.T) (bin, coverDir string) {
	t.Helper()

	bin = filepath.Join(t.TempDir(), "embyfin-mcp")
	args := []string{"build", "-o", bin}
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		coverDir = f.Value.String()
		// the main package too: without it the binary registers no exit hook
		// and writes no counters at all
		args = append(args, "-cover", "-coverpkg=.,./tools/...,./lib/...,./cli/...,./internal/...")
	}
	cmd := exec.CommandContext(t.Context(), "go", append(args, ".")...) //nolint:gosec // go build of this checkout
	// from the module root
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building embyfin-mcp: %v\n%s", err, out)
	}

	return bin, coverDir
}

// lockedBuffer collects a process's stderr, which it writes while the test
// reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// binary is the built embyfin-mcp and how to run it against the test server.
type binary struct {
	path, coverDir string
}

// command runs the binary in a directory and $HOME of its own, so a
// .embyfin-mcp config on the machine running the tests is not read, with the
// connection from the suite's environment and no other EMBYFIN_ setting
// unless given.
func (b binary) command(t *testing.T, env []string, args ...string) (*exec.Cmd, *lockedBuffer) {
	t.Helper()

	dir := t.TempDir()
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), b.path, args...) //nolint:gosec // the binary this test built
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(name, "EMBYFIN_") || slices.Contains([]string{"EMBYFIN_BACKEND", "EMBYFIN_SERVER", "EMBYFIN_TOKEN"}, name) {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+dir)
	if b.coverDir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+b.coverDir)
	}
	cmd.Env = append(cmd.Env, env...)
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr

	return cmd, stderr
}

// toolsFor is what the server should list for a set of options.
func toolsFor(t *testing.T, opts tools.Options) []string {
	t.Helper()

	infos, err := tools.Describe(opts)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}

	return names
}

// listed is the tools a session's server lists, sorted.
func listed(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)

	return names
}

// listedTools is the tools a session's server lists, with what each says of
// itself.
func listedTools(t *testing.T, cs *mcp.ClientSession) []*mcp.Tool {
	t.Helper()

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	return res.Tools
}

// kindOf reads a tool's kind from the hints a client sees: read-only, or
// destructive (a delete), or neither (a write).
func kindOf(tool *mcp.Tool) string {
	a := tool.Annotations
	switch {
	case a == nil:
		return "unannotated"
	case a.ReadOnlyHint:
		return "read"
	case a.DestructiveHint != nil && *a.DestructiveHint:
		return "delete"
	}

	return "write"
}

// callOn calls a tool on a session and returns its structured result.
func callOn(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: %v", name, res.Content)
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s: structured content is %T", name, res.StructuredContent)
	}

	return out
}

func connect(t *testing.T, transport mcp.Transport, stderr *lockedBuffer) *mcp.ClientSession {
	t.Helper()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "0"}, nil).Connect(t.Context(), transport, nil)
	if err != nil {
		t.Fatalf("connecting to embyfin-mcp: %v\n%s", err, stderr)
	}

	return cs
}

func TestTheBinary(t *testing.T) {
	if !ready {
		t.Skip("EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set")
	}
	path, coverDir := buildBinary(t)
	bin := binary{path: path, coverDir: coverDir}

	t.Run("version and info", func(t *testing.T) {
		cmd, stderr := bin.command(t, nil, "version")
		if out, err := cmd.Output(); err != nil || !strings.HasPrefix(string(out), "embyfin-mcp ") {
			t.Errorf("version = %q, %v\n%s", out, err, stderr)
		}
		cmd, stderr = bin.command(t, nil, "info")
		if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), `library "Movies"`) {
			t.Errorf("info = %q, %v\n%s", out, err, stderr)
		}
	})

	t.Run("stdio", func(t *testing.T) {
		for _, c := range []struct {
			name string
			env  []string
			args []string
			want tools.Options
		}{
			{"the default", nil, nil, tools.Options{Toolsets: []string{"core"}}},
			{"toolsets from the environment", []string{"EMBYFIN_TOOLSETS=all", "EMBYFIN_ENABLE_DELETE=true"}, nil, tools.Options{Toolsets: []string{"all"}, EnableDelete: true}},
			{"read only from a flag", nil, []string{"--toolsets", "all", "--read-only"}, tools.Options{Toolsets: []string{"all"}, ReadOnly: true}},
		} {
			t.Run(c.name, func(t *testing.T) {
				cmd, stderr := bin.command(t, c.env, append([]string{"serve"}, c.args...)...)
				cs := connect(t, &mcp.CommandTransport{Command: cmd}, stderr)
				if got, want := listed(t, cs), toolsFor(t, c.want); !slices.Equal(got, want) {
					t.Errorf("tools = %v\nwant %v", got, want)
				}
				// a real answer came back over stdout, uncorrupted
				if out := callOn(t, cs, "server_info", nil); str(out["backend"]) != os.Getenv("EMBYFIN_BACKEND") {
					t.Errorf("server_info = %v", out)
				}
				// closing stdin ends the process cleanly
				if err := cs.Close(); err != nil {
					t.Errorf("shutting down: %v\n%s", err, stderr)
				}
			})
		}

		// every tool says what it may do, and the kinds are what the
		// operator's flags choose between: playlist_delete and
		// collection_delete remove a list the server keeps, so they are
		// delete tools, and library_export writes only this machine's disk,
		// so it is a read tool
		t.Run("the kinds a client is told", func(t *testing.T) {
			cmd, stderr := bin.command(t, nil, "serve", "--toolsets", "all", "--enable-delete")
			cs := connect(t, &mcp.CommandTransport{Command: cmd}, stderr)
			t.Cleanup(func() { _ = cs.Close() })
			kinds := map[string][]string{}
			for _, tool := range listedTools(t, cs) {
				kinds[kindOf(tool)] = append(kinds[kindOf(tool)], tool.Name)
			}
			if len(kinds["read"]) != 60 || len(kinds["write"]) != 22 || len(kinds["delete"]) != 5 {
				t.Errorf("read %d, write %d, delete %d, want 60, 22 and 5", len(kinds["read"]), len(kinds["write"]), len(kinds["delete"]))
			}
			if want := []string{"collection_delete", "item_delete", "item_orphans_delete", "library_delete", "playlist_delete"}; !slices.Equal(sorted(kinds["delete"]), want) {
				t.Errorf("delete tools = %v, want %v", kinds["delete"], want)
			}
			if !slices.Contains(kinds["read"], "library_export") {
				t.Errorf("library_export is not a read tool: %v", kinds)
			}
		})

		// --read-only wins over --enable-delete: nothing that changes the
		// server is listed, and a write asked for anyway is refused and
		// changes nothing
		t.Run("read only refuses writes", func(t *testing.T) {
			cmd, stderr := bin.command(t, nil, "serve", "--toolsets", "all", "--read-only", "--enable-delete")
			cs := connect(t, &mcp.CommandTransport{Command: cmd}, stderr)
			t.Cleanup(func() { _ = cs.Close() })
			served := listedTools(t, cs)
			var got []string
			for _, tool := range served {
				got = append(got, tool.Name)
				if k := kindOf(tool); k != "read" {
					t.Errorf("%s is listed under --read-only as a %s tool", tool.Name, k)
				}
			}
			slices.Sort(got)
			if want := toolsFor(t, tools.Options{Toolsets: []string{"all"}, ReadOnly: true, EnableDelete: true}); !slices.Equal(got, want) || !slices.Contains(got, "library_export") {
				t.Errorf("tools = %v\nwant %v", got, want)
			}

			arrival := findItem(t, "Movies", "Movie", "Arrival")
			favourites := func() int {
				return len(rows(t, callOn(t, cs, "library_items", map[string]any{"watched": "favourite"})["items"], "items"))
			}
			before := favourites()
			for name, args := range map[string]map[string]any{
				"item_set_state": {"id": arrival, "favourite": true},
				"item_delete":    {"id": arrival, "confirm": true},
			} {
				res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
				if err == nil && !res.IsError {
					t.Errorf("%s ran under --read-only: %v", name, res.StructuredContent)
				}
			}
			if after := favourites(); after != before {
				t.Errorf("root's favourites went from %d to %d under --read-only", before, after)
			}
			if got := callOn(t, cs, "item_get", map[string]any{"id": arrival}); str(got["name"]) != "Arrival" {
				t.Errorf("Arrival after the refused delete = %v", got)
			}
		})

		t.Run("a write", func(t *testing.T) {
			cmd, stderr := bin.command(t, []string{"EMBYFIN_TOOLSETS=all"}, "serve")
			cs := connect(t, &mcp.CommandTransport{Command: cmd}, stderr)
			t.Cleanup(func() { _ = cs.Close() })
			arrival := findItem(t, "Movies", "Movie", "Arrival")
			favourite := func(on bool) bool {
				callOn(t, cs, "item_set_state", map[string]any{"id": arrival, "favourite": on})
				for _, it := range rows(t, callOn(t, cs, "library_items", map[string]any{"watched": "favourite"})["items"], "items") {
					if str(it["id"]) == arrival {
						return true
					}
				}
				return false
			}
			t.Cleanup(func() { _, _ = invoke("item_set_state", map[string]any{"id": arrival, "favourite": false}) })
			if !favourite(true) || favourite(false) {
				t.Error("item_set_state through the binary did not change root's favourites")
			}
		})
	})

	t.Run("http", func(t *testing.T) {
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		token := fmt.Sprintf("acceptance-%d", time.Now().UnixNano())

		cmd, stderr := bin.command(t, []string{"EMBYFIN_TOOLSETS=curation", "EMBYFIN_AUTH_TOKEN=" + token}, "serve", "--listen", addr)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		stopped := false
		t.Cleanup(func() {
			if !stopped {
				_ = cmd.Process.Kill()
			}
		})

		base := "http://" + addr
		if !eventually(func() bool {
			resp, err := http.Get(base + "/healthz") //nolint:noctx // a probe with the test's own timeout
			if err != nil {
				return false
			}
			_ = resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}) {
			t.Fatalf("the server never answered /healthz\n%s", stderr)
		}

		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, reqErr := http.DefaultClient.Do(req)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("/mcp without the token = HTTP %d, want 401", resp.StatusCode)
		}

		cs := connect(t, &mcp.StreamableClientTransport{
			Endpoint:   base + "/mcp",
			HTTPClient: &http.Client{Transport: bearer{token: token}},
		}, stderr)
		if got, want := listed(t, cs), toolsFor(t, tools.Options{Toolsets: []string{"curation"}}); !slices.Equal(got, want) {
			t.Errorf("tools = %v\nwant %v", got, want)
		}
		if got := findings(t, callOn(t, cs, "audit_file_path", map[string]any{"library": "Messy Movies", "checks": "year"})); !slices.Equal(got, []string{"Dune"}) {
			t.Errorf("audit_file_path over HTTP = %v, want [Dune]", got)
		}
		_ = cs.Close()

		// SIGTERM drains and exits cleanly
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-exited:
			stopped = true
			if err != nil {
				t.Errorf("after SIGTERM the server exited with %v\n%s", err, stderr)
			}
		case <-time.After(15 * time.Second):
			t.Errorf("the server did not exit within 15s of SIGTERM\n%s", stderr)
		}
	})

	t.Run("refuses to start", func(t *testing.T) {
		for _, c := range []struct {
			name string
			env  []string
			args []string
			want string
		}{
			{"without a media server token", []string{"EMBYFIN_TOKEN="}, []string{"serve"}, "token parameter can't be empty"},
			{"on a port without a bearer token", nil, []string{"serve", "--listen", "127.0.0.1:0"}, "--listen needs --auth-token"},
		} {
			t.Run(c.name, func(t *testing.T) {
				cmd, stderr := bin.command(t, c.env, c.args...)
				cmd.Stdin = strings.NewReader("")
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case err := <-done:
					if err == nil || !strings.Contains(stderr.String(), c.want) {
						t.Errorf("exit %v, stderr %q: want a failure saying %q", err, stderr, c.want)
					}
				case <-ctx.Done():
					_ = cmd.Process.Kill()
					t.Errorf("still running after 30s: want it to refuse to start, saying %q", c.want)
				}
			})
		}
	})
}

// bearer sends the endpoint's token on every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)

	return http.DefaultTransport.RoundTrip(r)
}

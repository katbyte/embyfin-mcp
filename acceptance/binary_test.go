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

// serveStdio starts the binary serving over stdio and connects to it,
// closing it when the test ends.
func (b binary) serveStdio(t *testing.T, env []string, args ...string) *mcp.ClientSession {
	t.Helper()

	cmd, stderr := b.command(t, env, append([]string{"serve"}, args...)...)
	cs := connect(t, &mcp.CommandTransport{Command: cmd}, stderr)
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// refusesToStart runs the binary expecting it to exit with an error that
// says want, and fails the test if it runs on or says something else.
func (b binary) refusesToStart(t *testing.T, env []string, want string, args ...string) {
	t.Helper()

	cmd, stderr := b.command(t, env, args...)
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
		// the log line quotes its message, escaping the quotes inside it
		if said := strings.ReplaceAll(stderr.String(), `\"`, `"`); err == nil || !strings.Contains(said, want) {
			t.Errorf("exit %v, stderr %q: want a failure saying %q", err, stderr, want)
		}
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		t.Errorf("still running after 30s: want it to refuse to start, saying %q", want)
	}
}

// freeAddr is a loopback address nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	return addr
}

// httpServer is the binary serving over HTTP.
type httpServer struct {
	base   string
	cmd    *exec.Cmd
	stderr *lockedBuffer
	exited chan error
}

// serveHTTP starts the binary on a port of its own and waits for it to
// answer its health check; it is killed when the test ends if it has not
// stopped by then.
func (b binary) serveHTTP(t *testing.T, env []string, args ...string) *httpServer {
	t.Helper()

	addr := freeAddr(t)
	cmd, stderr := b.command(t, env, append([]string{"serve", "--listen", addr}, args...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &httpServer{base: "http://" + addr, cmd: cmd, stderr: stderr, exited: make(chan error, 1)}
	go func() { s.exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if !eventually(func() bool {
		resp, err := http.Get(s.base + "/healthz") //nolint:noctx // a probe with the test's own timeout
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}) {
		t.Fatalf("the server never answered /healthz\n%s", stderr)
	}

	return s
}

// initialize posts an MCP initialize to the endpoint with an Authorization
// header (none when empty) and returns the status it answers with.
func (s *httpServer) initialize(t *testing.T, authorization string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.base+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"acceptance","version":"0"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	return resp.StatusCode
}

// sessionWith connects to the endpoint sending a bearer token (none when
// empty).
func (s *httpServer) sessionWith(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()

	transport := &mcp.StreamableClientTransport{Endpoint: s.base + "/mcp"}
	if token != "" {
		transport.HTTPClient = &http.Client{Transport: bearer{token: token}}
	}
	cs := connect(t, transport, s.stderr)
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// matching is the tools of a listing that pass keep.
func matching(names []string, keep func(string) bool) []string {
	var out []string
	for _, n := range names {
		if keep(n) {
			out = append(out, n)
		}
	}

	return out
}

func TestTheBinary(t *testing.T) {
	if !ready {
		t.Skip("EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set")
	}
	path, coverDir := buildBinary(t)
	bin := binary{path: path, coverDir: coverDir}
	every := toolsFor(t, tools.Options{Toolsets: []string{"all"}, EnableDelete: true})
	core := toolsFor(t, tools.Options{Toolsets: []string{"core"}})

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
			cs := bin.serveStdio(t, nil, "--toolsets", "all", "--enable-delete")
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

		// every tool, but not the ones that delete, unless asked for
		t.Run("all without delete", func(t *testing.T) {
			cs := bin.serveStdio(t, nil, "--toolsets", "all")
			got := listed(t, cs)
			for _, tool := range listedTools(t, cs) {
				if kindOf(tool) == "delete" {
					t.Errorf("%s is listed without --enable-delete", tool.Name)
				}
			}
			if want := matching(every, func(n string) bool {
				return !slices.Contains([]string{"collection_delete", "item_delete", "item_orphans_delete", "library_delete", "playlist_delete"}, n)
			}); !slices.Equal(got, want) {
				t.Errorf("tools = %v\nwant %v", got, want)
			}
		})

		// --read-only wins over --enable-delete: nothing that changes the
		// server is listed, and a write asked for anyway is refused as a
		// tool the server does not have, and changes nothing
		t.Run("read only refuses writes", func(t *testing.T) {
			cs := bin.serveStdio(t, nil, "--toolsets", "all", "--read-only", "--enable-delete")
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
			// a delete of nothing and a favourite: neither could do harm had
			// read-only failed, and neither is there to call
			for name, args := range map[string]map[string]any{
				"item_set_state": {"id": arrival, "favourite": true},
				"item_delete":    {"id": unknownID(), "confirm": true},
			} {
				res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
				switch {
				case err == nil && !res.IsError:
					t.Errorf("%s ran under --read-only: %v", name, res.StructuredContent)
				case err == nil || !strings.Contains(err.Error(), fmt.Sprintf("unknown tool %q", name)):
					t.Errorf("%s under --read-only: %v %v, want it refused as a tool the server does not have", name, err, res)
				}
			}
			if after := favourites(); after != before {
				t.Errorf("root's favourites went from %d to %d under --read-only", before, after)
			}
		})

		// read-only from the environment is read-only too
		t.Run("read only from the environment", func(t *testing.T) {
			cs := bin.serveStdio(t, []string{"EMBYFIN_READ_ONLY=true", "EMBYFIN_TOOLSETS=all"})
			if got, want := listed(t, cs), toolsFor(t, tools.Options{Toolsets: []string{"all"}, ReadOnly: true}); !slices.Equal(got, want) || slices.Contains(got, "item_set_state") {
				t.Errorf("tools = %v\nwant %v", got, want)
			}
		})

		t.Run("a write", func(t *testing.T) {
			cs := bin.serveStdio(t, []string{"EMBYFIN_TOOLSETS=all"})
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

	// the sets and families --toolsets takes, and the patterns
	// --allow-tools and --deny-tools narrow them with; core comes with every
	// set
	t.Run("choosing tools", func(t *testing.T) {
		withCore := func(keep func(string) bool) []string {
			return matching(every, func(n string) bool { return keep(n) || slices.Contains(core, n) })
		}
		for _, c := range []struct {
			name string
			env  []string
			args []string
			want []string
		}{
			{"a family", nil, []string{"--toolsets", "show"}, withCore(func(n string) bool { return strings.HasPrefix(n, "show_") })},
			{"a list of families", nil, []string{"--toolsets", "show,user"}, withCore(func(n string) bool {
				return strings.HasPrefix(n, "show_") || strings.HasPrefix(n, "user_")
			})},
			{"a family from the environment", []string{"EMBYFIN_TOOLSETS=session"}, nil, withCore(func(n string) bool { return strings.HasPrefix(n, "session_") })},
			{"allowed by name and glob", nil, []string{"--toolsets", "all", "--allow-tools", "library_*,item_get"}, matching(every, func(n string) bool {
				return (strings.HasPrefix(n, "library_") && n != "library_delete") || n == "item_get"
			})},
			{"allowed from the environment", []string{"EMBYFIN_TOOLSETS=all", "EMBYFIN_ALLOW_TOOLS=user_get,*_list"}, nil, matching(every, func(n string) bool {
				return n == "user_get" || strings.HasSuffix(n, "_list")
			})},
			{"denied by glob and name", nil, []string{"--toolsets", "all", "--enable-delete", "--deny-tools", "*_delete,audit_*,server_info"}, matching(every, func(n string) bool {
				return !strings.HasSuffix(n, "_delete") && !strings.HasPrefix(n, "audit_") && n != "server_info"
			})},
			{"allowed and then denied", nil, []string{"--toolsets", "all", "--allow-tools", "show_*", "--deny-tools", "show_missing"}, []string{"show_episodes_exist", "show_resolve", "show_seasons"}},
		} {
			t.Run(c.name, func(t *testing.T) {
				if got := listed(t, bin.serveStdio(t, c.env, c.args...)); !slices.Equal(got, c.want) {
					t.Errorf("tools = %v\nwant %v", got, c.want)
				}
			})
		}

		// `embyfin-mcp tools -q` is what serve registers, for the same flags
		t.Run("tools -q is what serve lists", func(t *testing.T) {
			for _, args := range [][]string{nil, {"--toolsets", "curation"}, {"--toolsets", "all", "--enable-delete", "--deny-tools", "audit_*"}} {
				cmd, stderr := bin.command(t, nil, append([]string{"tools", "-q"}, args...)...)
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("tools -q %v: %v\n%s", args, err, stderr)
				}
				printed := strings.Fields(string(out))
				if served := listed(t, bin.serveStdio(t, nil, args...)); !slices.Equal(printed, served) || len(served) == 0 {
					t.Errorf("tools -q %v printed %v\nserve lists %v", args, printed, served)
				}
			}
		})
	})

	// a .embyfin-mcp in $HOME is read, spelling each setting as the
	// environment does less its prefix, and a flag beats it
	t.Run("the config file", func(t *testing.T) {
		config := []string{"TOOLSETS=show", "READ_ONLY=true"}
		serve := func(args ...string) []string {
			cmd, stderr := bin.command(t, nil, append([]string{"serve"}, args...)...)
			if err := os.WriteFile(filepath.Join(cmd.Dir, ".embyfin-mcp"), []byte(strings.Join(config, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cs := connect(t, &mcp.CommandTransport{Command: cmd}, stderr)
			defer func() { _ = cs.Close() }()
			return listed(t, cs)
		}
		if got, want := serve(), toolsFor(t, tools.Options{Toolsets: []string{"show"}, ReadOnly: true}); !slices.Equal(got, want) {
			t.Errorf("with the config file, tools = %v\nwant %v", got, want)
		}
		if got, want := serve("--toolsets", "user"), toolsFor(t, tools.Options{Toolsets: []string{"user"}, ReadOnly: true}); !slices.Equal(got, want) || slices.ContainsFunc(got, func(n string) bool { return strings.HasPrefix(n, "show_") }) {
			t.Errorf("with --toolsets user over the file's show, tools = %v\nwant %v", got, want)
		}
	})

	// the binary is given no TMDB token here, so show_missing cannot read a
	// run the server keeps no record of, and says what would fix that
	t.Run("show_missing without a TMDB token", func(t *testing.T) {
		cs := bin.serveStdio(t, nil, "--toolsets", "show")
		out := callOn(t, cs, "show_missing", map[string]any{"series_id": findItem(t, "Shows", "Series", "Severance")})
		if out["supported"] != false || out["missing"] != nil || str(out["source"]) != "none" || !strings.Contains(str(out["reason"]), "no metadata provider is configured to be asked instead: set EMBYFIN_TMDB_TOKEN") {
			t.Errorf("show_missing with no token = supported %v missing %v source %v reason %q", out["supported"], out["missing"], out["source"], out["reason"])
		}
	})

	t.Run("http", func(t *testing.T) {
		token := fmt.Sprintf("acceptance-%d", time.Now().UnixNano())
		s := bin.serveHTTP(t, []string{"EMBYFIN_TOOLSETS=curation", "EMBYFIN_AUTH_TOKEN=" + token})

		// no token, and the wrong one, are turned away
		for name, auth := range map[string]string{"no token": "", "the wrong token": "Bearer not-" + token, "the token without Bearer": token} {
			if status := s.initialize(t, auth); status != http.StatusUnauthorized {
				t.Errorf("/mcp with %s = HTTP %d, want 401", name, status)
			}
		}

		cs := s.sessionWith(t, token)
		if got, want := listed(t, cs), toolsFor(t, tools.Options{Toolsets: []string{"curation"}}); !slices.Equal(got, want) {
			t.Errorf("tools = %v\nwant %v", got, want)
		}
		if got := findings(t, callOn(t, cs, "audit_file_path", map[string]any{"library": "Messy Movies", "checks": "year"})); !slices.Equal(got, []string{"Dune"}) {
			t.Errorf("audit_file_path over HTTP = %v, want [Dune]", got)
		}
		_ = cs.Close()

		// SIGTERM drains and exits cleanly
		if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-s.exited:
			if err != nil {
				t.Errorf("after SIGTERM the server exited with %v\n%s", err, s.stderr)
			}
		case <-time.After(15 * time.Second):
			t.Errorf("the server did not exit within 15s of SIGTERM\n%s", s.stderr)
		}
	})

	t.Run("http with the token as a flag", func(t *testing.T) {
		token := fmt.Sprintf("acceptance-flag-%d", time.Now().UnixNano())
		s := bin.serveHTTP(t, nil, "--auth-token", token)
		if status := s.initialize(t, ""); status != http.StatusUnauthorized {
			t.Errorf("/mcp without the token = HTTP %d, want 401", status)
		}
		if got := listed(t, s.sessionWith(t, token)); !slices.Equal(got, core) {
			t.Errorf("tools = %v\nwant the default %v", got, core)
		}
	})

	// with no token at all, said in so many words, anyone may connect
	t.Run("http with no auth", func(t *testing.T) {
		s := bin.serveHTTP(t, nil, "--allow-no-auth")
		if status := s.initialize(t, ""); status != http.StatusOK {
			t.Errorf("/mcp with no auth and no token = HTTP %d, want 200", status)
		}
		if out := callOn(t, s.sessionWith(t, ""), "server_info", nil); str(out["backend"]) != os.Getenv("EMBYFIN_BACKEND") {
			t.Errorf("server_info with no auth = %v", out)
		}
		if !strings.Contains(s.stderr.String(), "no auth token set") {
			t.Errorf("serving with no token said nothing of it: %s", s.stderr)
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
			{"with a toolset that is none", nil, []string{"serve", "--toolsets", "core,zzyzx"}, `unknown toolset "zzyzx" (sets: all, `},
			{"with an allow pattern that matches no tool", nil, []string{"serve", "--allow-tools", "zzyzx_*"}, `allow-tools pattern "zzyzx_*" matches no tool`},
			{"with a deny pattern that matches no tool", []string{"EMBYFIN_DENY_TOOLS=item_zzyzx"}, []string{"serve"}, `deny-tools pattern "item_zzyzx" matches no tool`},
		} {
			t.Run(c.name, func(t *testing.T) { bin.refusesToStart(t, c.env, c.want, c.args...) })
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

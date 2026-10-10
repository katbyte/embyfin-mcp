//go:build integration

package acceptance

// Two servers at once. An instance of embyfin-mcp serves one server, and a
// second server is served by a second instance given a settings file of its
// own (--config, or EMBYFIN_CONFIG). scripts/testenv.sh starts a server of
// the other kind, with nothing in it, beside this suite's own, and this runs
// the built binary against the pair.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	acc "github.com/katbyte/go-kt/mcp/acctest"

	"github.com/katbyte/embyfin-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// aServer is where a server is and the key to it, as a settings file names
// them.
type aServer struct {
	backend, address, token string
}

// publicInfo is what a server says of itself to anyone who asks, with no
// key: the name and version server_info must give for it.
func publicInfo(t *testing.T, address string) (name, version string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address+"/System/Info/Public", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("asking %s who it is: %v", address, err)
	}
	defer func() { _ = res.Body.Close() }()

	var info struct {
		ServerName string
		Version    string
	}
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil || res.StatusCode != http.StatusOK || info.ServerName == "" || info.Version == "" {
		t.Fatalf("asking %s who it is: status %d, %+v, %v", address, res.StatusCode, info, err)
	}

	return info.ServerName, info.Version
}

// Two instances of embyfin-mcp running at once, each told its server by the
// settings file it was given and by nothing else, answer from two servers:
// this suite's own and one of the other kind. The environment names no
// server here, so an answer from the right one is the file's address and key
// having reached it; and the second server's address with the first's key is
// turned away, so the key sent is the file's too.
func TestTwoInstancesServeTwoServers(t *testing.T) {
	if !suite.Ready {
		t.Skip("EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN are not set")
	}
	own := aServer{os.Getenv("EMBYFIN_BACKEND"), os.Getenv("EMBYFIN_SERVER"), os.Getenv("EMBYFIN_TOKEN")}
	beside := aServer{os.Getenv("EMBYFIN_TEST_BESIDE_BACKEND"), os.Getenv("EMBYFIN_TEST_BESIDE_SERVER"), os.Getenv("EMBYFIN_TEST_BESIDE_TOKEN")}
	if beside.backend == "" || beside.address == "" || beside.token == "" {
		t.Skip("EMBYFIN_TEST_BESIDE_BACKEND, EMBYFIN_TEST_BESIDE_SERVER and EMBYFIN_TEST_BESIDE_TOKEN are not set: scripts/testenv.sh beside starts the second server, as make testacc-acceptance does")
	}
	if beside.backend == own.backend || beside.address == own.address {
		t.Fatalf("the second server is %s at %s and the first %s at %s: want one of each kind, at two addresses", beside.backend, beside.address, own.backend, own.address)
	}
	ownName, ownVersion := publicInfo(t, own.address)
	besideName, besideVersion := publicInfo(t, beside.address)
	if ownName == besideName {
		t.Fatalf("both servers call themselves %q: nothing would tell their answers apart", ownName)
	}

	path, coverDir := buildBinary(t)
	bin := binary{path: path, coverDir: coverDir}
	settings := func(name string, s aServer, more ...string) string {
		file := filepath.Join(t.TempDir(), name)
		lines := append([]string{"BACKEND=" + s.backend, "SERVER=" + s.address, "TOKEN=" + s.token}, more...)
		if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		return file
	}
	// start runs an instance whose environment names no server: the suite's
	// own is taken out of it, so the settings file is the only thing that
	// says which server to serve
	start := func(env []string, args ...string) *mcp.ClientSession {
		cmd, stderr := bin.command(t, env, append([]string{"serve"}, args...)...)
		cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
			name, _, _ := strings.Cut(kv, "=")

			return slices.Contains([]string{"EMBYFIN_BACKEND", "EMBYFIN_SERVER", "EMBYFIN_TOKEN"}, name)
		})
		cs := connect(t, &mcp.CommandTransport{Command: cmd}, stderr)
		t.Cleanup(func() { _ = cs.Close() })

		return cs
	}
	ask := func(cs *mcp.ClientSession) (map[string]any, error) {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "server_info"})
		if err != nil {
			return nil, err
		}
		if res.IsError {
			var said []string
			for _, c := range res.Content {
				if text, ok := c.(*mcp.TextContent); ok {
					said = append(said, text.Text)
				}
			}

			return nil, fmt.Errorf("%s", strings.Join(said, "\n"))
		}
		out, ok := res.StructuredContent.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("structured content is %T", res.StructuredContent)
		}

		return out, nil
	}

	// one named by the flag and one by the environment, both running from
	// here on
	first := start(nil, "--config", settings("first.env", own))
	second := start([]string{"EMBYFIN_CONFIG=" + settings("second.env", beside, "TOOLSETS=admin")})
	instances := []struct {
		which         string
		cs            *mcp.ClientSession
		server        aServer
		name, version string
	}{
		{"the first", first, own, ownName, ownVersion},
		{"the second", second, beside, besideName, besideVersion},
	}
	answersAsItsOwn := func(when string) {
		for _, i := range instances {
			if out, err := ask(i.cs); err != nil || acc.Str(out["backend"]) != i.server.backend || acc.Str(out["server_name"]) != i.name || acc.Str(out["server_version"]) != i.version {
				t.Errorf("%s, %s instance's server_info = %v, %v\nwant %s %q, version %s", when, i.which, out, err, i.server.backend, i.name, i.version)
			}
		}
	}

	// asked in turn, and again, each answers as its own server
	answersAsItsOwn("asked in turn")
	answersAsItsOwn("asked in turn again")
	t.Logf("the first instance serves %s %q (%s) and the second %s %q (%s)", own.backend, ownName, ownVersion, beside.backend, besideName, besideVersion)
	// and asked at the same moment, over and over
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { answersAsItsOwn("asked at the same moment") })
	}
	wg.Wait()

	// each has what its own file asked for and nothing of the other's: the
	// tools, and the libraries of the server it serves, the second's being a
	// server with nothing in it
	if got, want := listed(t, first), toolsFor(t, tools.Options{Toolsets: []string{"core"}}); !slices.Equal(got, want) {
		t.Errorf("the first instance's tools = %v\nwant %v", got, want)
	}
	if got, want := listed(t, second), toolsFor(t, tools.Options{Toolsets: []string{"admin"}}); !slices.Equal(got, want) {
		t.Errorf("the second instance's tools = %v\nwant %v", got, want)
	}
	if libraries := acc.RowsOf(callOn(t, first, "library_list", nil)["libraries"]); !slices.ContainsFunc(libraries, func(l map[string]any) bool { return acc.Str(l["name"]) == "Movies" }) {
		t.Errorf("the first instance's libraries = %v, want the suite's own, Movies among them", libraries)
	}
	if libraries := acc.RowsOf(callOn(t, second, "library_list", nil)["libraries"]); len(libraries) != 0 {
		t.Errorf("the second instance's libraries = %v, want none: its server has nothing in it", libraries)
	}

	// the key sent is the file's: the second server's address with the
	// first server's key is turned away
	crossed := start(nil, "--config", settings("crossed.env", aServer{beside.backend, beside.address, own.token}))
	out, err := ask(crossed)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("the second server asked with the first's key = %v, %v: want it refused", out, err)
	}
	t.Logf("the second server, asked with the first's key: %v", err)
	// and the two that were given the right ones are none the worse for it
	answersAsItsOwn("after a third was turned away")
}

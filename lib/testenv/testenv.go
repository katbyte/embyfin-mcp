// Package testenv is what the two live suites share: the environment
// scripts/testenv.sh hands them, the record/replay proxy their media server's
// provider calls go through, the checks that the container can reach it, and
// the report at the end of a run on what the proxy saw.
//
// The acceptance suite (../../acceptance) drives the tools over MCP and the
// integration suite (../../integration) drives the generated SDKs; both run
// against the same containers and cassettes, and used to carry this code by
// copy.
package testenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/providerproxy"
)

// Recording reports whether this run may call the real providers.
// EMBYFIN_TEST_RECORD=1 fills in only the answers a cassette lacks, replaying
// the rest as recorded; EMBYFIN_TEST_RECORD=all fetches every answer afresh
// (make record). Either needs a TMDB token.
func Recording() bool { return os.Getenv("EMBYFIN_TEST_RECORD") != "" }

// Verifying reports whether to check the cassettes against the live providers
// without rewriting them (EMBYFIN_TEST_VERIFY).
func Verifying() bool { return os.Getenv("EMBYFIN_TEST_VERIFY") != "" }

// Configured reports whether the container environment is present: a server,
// a token and a backend.
func Configured() bool {
	return os.Getenv("EMBYFIN_SERVER") != "" && os.Getenv("EMBYFIN_TOKEN") != "" && os.Getenv("EMBYFIN_BACKEND") != ""
}

// Backend is the server the environment names, "emby" or "jellyfin".
func Backend() string { return os.Getenv("EMBYFIN_BACKEND") }

// DataDir is the host path the container's /media is bind-mounted from, so a
// test can add or remove files and rescan. It is "" when EMBYFIN_TEST_DATA is
// not set, which every test that lays files out must skip on: joined onto
// nothing, the path was ./media, and those tests wrote into the checkout.
func DataDir() string {
	env := os.Getenv("EMBYFIN_TEST_DATA")
	if env == "" {
		return ""
	}

	return filepath.Join(env, "media")
}

// CassetteDir is where a backend's recordings live, under a suite's testdata:
// Emby and Jellyfin ask the providers different questions, so each has its
// own set.
func CassetteDir(backend string) string {
	return filepath.Join("testdata", "cassettes", backend)
}

// ProxyPort is the port the provider proxy listens on, EMBYFIN_TEST_PROXY_PORT
// or 18080, which the container's HTTPS_PROXY already names.
func ProxyPort() (int, error) {
	v := os.Getenv("EMBYFIN_TEST_PROXY_PORT")
	if v == "" {
		return 18080, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("EMBYFIN_TEST_PROXY_PORT=%q: %w", v, err)
	}

	return n, nil
}

// Proxy is the record/replay proxy a suite runs for the length of its run,
// and what it saw once stopped.
type Proxy struct {
	proxy *providerproxy.Proxy
	// Misses are the requests replay had no recording for, and Drifts the
	// answers that changed shape since recording (under Verifying); both
	// are read when the proxy is stopped
	Misses []string
	Drifts []providerproxy.Drift
}

// StartProxy brings up the record/replay proxy the container's HTTPS_PROXY
// already points at, signing with the CA scripts/testenv.sh minted and
// mounted into the container, with its cassettes under cassetteDir, and
// proves the container can reach it, saying on stderr what the container
// sees of the network.
func StartProxy(ctx context.Context, cassetteDir string) (*Proxy, error) {
	port, err := ProxyPort()
	if err != nil {
		return nil, err
	}

	mode := providerproxy.Replay
	switch {
	case Recording():
		mode = providerproxy.Record
	case Verifying():
		mode = providerproxy.Verify
	}

	opts := providerproxy.Options{
		Mode:        mode,
		CassetteDir: cassetteDir,
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
		// the media server reaching itself is not provider traffic, and
		// Emby asks ipify, then its own service, for its public address on
		// startup, which is nobody's business and not part of any recording
		IgnoreHosts: append(ContainerAddresses(ctx), "api.ipify.org", "api64.ipify.org", "connect.emby.media"),
	}
	if ca := os.Getenv("EMBYFIN_TEST_PROXY_CA"); ca != "" {
		opts.CACert, opts.CAKey = filepath.Join(ca, "ca.pem"), filepath.Join(ca, "ca.key")
	}
	p, err := providerproxy.New(opts) //nolint:contextcheck // the proxy outlives this call: it runs for the whole suite
	if err != nil {
		return nil, err
	}
	network, err := CheckProxyReachable(ctx, port)
	if network != "" {
		_, _ = fmt.Fprintf(os.Stderr, "container network: %s\n", network)
	}
	if err != nil {
		_ = p.Close() //nolint:contextcheck // closing takes no context

		return nil, err
	}

	return &Proxy{proxy: p}, nil
}

// Addr is the address the proxy listens on, for a client on this machine
// that should go through it too.
func (p *Proxy) Addr() string { return p.proxy.Addr() }

// Stop closes the proxy and keeps what it saw, returning what closing it
// failed with. Stopping twice is harmless.
func (p *Proxy) Stop() error {
	if p == nil || p.proxy == nil {
		return nil
	}
	p.Misses = p.proxy.Misses()
	p.Drifts = p.proxy.Drifts()
	err := p.proxy.Close()
	p.proxy = nil
	if err != nil {
		return fmt.Errorf("provider proxy close: %w", err)
	}

	return nil
}

// Report says what a stopped proxy saw that fails a run, "" for nothing: a
// replay miss means a test ran against a 502 rather than a recording, so it
// is said loudly even when the assertions happened to survive it, and a drift
// (collected under Verifying alone) means a provider still answers, but no
// longer in the shape the server decodes.
func (p *Proxy) Report() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	if len(p.Misses) > 0 {
		fmt.Fprintf(&b, "\nprovider proxy: %d request(s) had no recording:\n", len(p.Misses))
		for _, m := range p.Misses {
			fmt.Fprintln(&b, "  "+m)
		}
		fmt.Fprintln(&b, "run `make record` to capture them")
	}
	if len(p.Drifts) > 0 {
		fmt.Fprintf(&b, "\nprovider proxy: %d response(s) changed shape since recording:\n", len(p.Drifts))
		for _, d := range p.Drifts {
			fmt.Fprintln(&b, "  "+d.String())
		}
		fmt.Fprintln(&b, "\nreview the changes, then run `make record` to accept them")
	}

	return b.String()
}

// ContainerAddresses are the addresses the media server reaches itself on:
// Emby pings its own container address at startup, which goes through the
// proxy because NO_PROXY is set before docker hands the container an address.
// The proxy answers those without a cassette (providerproxy.Options.IgnoreHosts).
func ContainerAddresses(ctx context.Context) []string {
	name := os.Getenv("EMBYFIN_TEST_CONTAINER")
	if name == "" {
		return nil
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", //nolint:gosec // the container scripts/testenv.sh started, named by the environment it wrote
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}{{.Config.Hostname}}", name).Output()
	if err != nil {
		return nil // not our container to ask about
	}

	return strings.Fields(string(out))
}

// CheckProxyReachable proves, from inside the container, that the media
// server can reach the provider proxy, and says what the container sees of
// the network: its hosts entry for the gateway and its proxy setting. A
// server that cannot reach the proxy fails every provider lookup with a
// timeout of its own, which reads as dozens of unrelated assertion failures
// rather than the one plumbing problem it is - so say it plainly, once,
// before the suite runs.
func CheckProxyReachable(ctx context.Context, port int) (network string, err error) {
	name := os.Getenv("EMBYFIN_TEST_CONTAINER")
	if name == "" {
		return "", nil // not a container this suite started
	}
	// exit 3 says the image has no probe tool, which is not a failure. The
	// hosts entries come too: a container handed an IPv6 route to the host
	// gateway can reach the proxy with one address and not the other.
	script := fmt.Sprintf(
		"grep -i host.docker.internal /etc/hosts; echo \"proxy env: ${HTTPS_PROXY:-unset}\"; "+
			"command -v nc >/dev/null || exit 3; nc -z -w 5 host.docker.internal %d", port)
	out, err := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", script).CombinedOutput() //nolint:gosec // the container scripts/testenv.sh started, and a script of this file's own
	network = strings.TrimSpace(string(out))
	switch {
	case err == nil:
		return network, nil
	case strings.Contains(err.Error(), "exit status 3"):
		return network, nil
	default:
		return network, fmt.Errorf("%s cannot reach the provider proxy on host.docker.internal:%d, so every provider lookup will time out: %w: %s",
			name, port, err, out)
	}
}

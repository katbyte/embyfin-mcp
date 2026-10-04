package testenv

import (
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/providerproxy"
)

// The environment is read as scripts/testenv.sh writes it: a data dir that
// is not set is "", not ./media, so a test that lays files out skips rather
// than writing into the checkout; the proxy port is 18080 unless set, and a
// port that is no number is refused.
func TestEnvironment(t *testing.T) {
	t.Setenv("EMBYFIN_TEST_DATA", "")
	if got := DataDir(); got != "" {
		t.Errorf("DataDir() with nothing set = %q, want \"\"", got)
	}
	t.Setenv("EMBYFIN_TEST_DATA", "/x/testenv/emby")
	if got := DataDir(); got != "/x/testenv/emby/media" {
		t.Errorf("DataDir() = %q", got)
	}
	if got := CassetteDir("jellyfin"); got != "testdata/cassettes/jellyfin" {
		t.Errorf("CassetteDir(jellyfin) = %q", got)
	}

	t.Setenv("EMBYFIN_TEST_PROXY_PORT", "")
	if port, err := ProxyPort(); err != nil || port != 18080 {
		t.Errorf("ProxyPort() unset = %d, %v", port, err)
	}
	t.Setenv("EMBYFIN_TEST_PROXY_PORT", "18280")
	if port, err := ProxyPort(); err != nil || port != 18280 {
		t.Errorf("ProxyPort() = %d, %v", port, err)
	}
	t.Setenv("EMBYFIN_TEST_PROXY_PORT", "many")
	if _, err := ProxyPort(); err == nil || !strings.Contains(err.Error(), `EMBYFIN_TEST_PROXY_PORT="many"`) {
		t.Errorf("a port that is no number = %v", err)
	}

	t.Setenv("EMBYFIN_SERVER", "")
	if Configured() {
		t.Error("Configured() with no server")
	}
	for k, v := range map[string]string{"EMBYFIN_SERVER": "http://x", "EMBYFIN_TOKEN": "t", "EMBYFIN_BACKEND": "emby"} {
		t.Setenv(k, v)
	}
	if !Configured() || Backend() != "emby" {
		t.Errorf("Configured() = %v, Backend() = %q", Configured(), Backend())
	}
	t.Setenv("EMBYFIN_TEST_RECORD", "")
	t.Setenv("EMBYFIN_TEST_VERIFY", "1")
	if Recording() || !Verifying() {
		t.Error("EMBYFIN_TEST_VERIFY alone is verifying, not recording")
	}
}

// What a stopped proxy saw is a failure when replay missed a recording or an
// answer changed shape, and nothing to say otherwise; a proxy never started
// has nothing to say either.
func TestProxyReport(t *testing.T) {
	if got := (*Proxy)(nil).Report(); got != "" {
		t.Errorf("a nil proxy reported %q", got)
	}
	if got := (&Proxy{}).Report(); got != "" {
		t.Errorf("a clean proxy reported %q", got)
	}
	p := &Proxy{Misses: []string{"GET api.themoviedb.org/3/movie/1"}}
	if got := p.Report(); !strings.Contains(got, "1 request(s) had no recording") || !strings.Contains(got, "make record") {
		t.Errorf("a miss reported %q", got)
	}
	p = &Proxy{Drifts: []providerproxy.Drift{{}}}
	if got := p.Report(); !strings.Contains(got, "1 response(s) changed shape") {
		t.Errorf("a drift reported %q", got)
	}
	// stopping what was never started is harmless
	if err := p.Stop(); err != nil {
		t.Error(err)
	}
	if err := (*Proxy)(nil).Stop(); err != nil {
		t.Error(err)
	}
	// and outside a container there is nothing to check or to ask
	t.Setenv("EMBYFIN_TEST_CONTAINER", "")
	if network, err := CheckProxyReachable(t.Context(), 18080); network != "" || err != nil {
		t.Errorf("CheckProxyReachable outside a container = %q, %v", network, err)
	}
	if got := ContainerAddresses(t.Context()); got != nil {
		t.Errorf("ContainerAddresses outside a container = %v", got)
	}
}

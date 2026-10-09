// Package testenv is what the two live suites share that is this
// application's own: the environment scripts/testenv.sh hands them, and how
// the record/replay proxy their media server's provider calls go through is
// set up. The machinery is go-kt's (test/env and test/replayproxy).
//
// The acceptance suite (../../acceptance) drives the tools over MCP and the
// integration suite (../../integration) drives the generated SDKs; both run
// against the same containers and cassettes.
package testenv

import (
	"context"
	"path/filepath"

	"github.com/katbyte/go-kt/test/env"
	"github.com/katbyte/go-kt/test/replayproxy"
)

// embyfin is the environment scripts/testenv.sh writes, every variable of
// which begins EMBYFIN_.
var embyfin = env.New("EMBYFIN")

// Recording reports whether this run may call the real providers.
// EMBYFIN_TEST_RECORD=1 fills in only the answers a cassette lacks, replaying
// the rest as recorded; EMBYFIN_TEST_RECORD=all fetches every answer afresh
// (make record). Either needs a TMDB token.
func Recording() bool { return embyfin.Recording() }

// Verifying reports whether to check the cassettes against the live providers
// without rewriting them (EMBYFIN_TEST_VERIFY).
func Verifying() bool { return embyfin.Verifying() }

// Mode is how a proxy treats a request, as the environment asks: replay
// unless recording (what is missing, or all of it afresh) or verifying.
func Mode() replayproxy.Mode { return embyfin.Mode() }

// Configured reports whether the container environment is present: a server,
// a token and a backend.
func Configured() bool { return embyfin.Configured("BACKEND") }

// Backend is the server the environment names, "emby" or "jellyfin".
func Backend() string { return embyfin.Get("BACKEND") }

// DataDir is the host path the container's /media is bind-mounted from, so a
// test can add or remove files and rescan. It is "" when EMBYFIN_TEST_DATA is
// not set, which every test that lays files out must skip on: joined onto
// nothing, the path was ./media, and those tests wrote into the checkout.
func DataDir() string { return embyfin.DataDir("media") }

// CassetteDir is where a backend's recordings live, under a suite's testdata:
// Emby and Jellyfin ask the providers different questions, so each has its
// own set.
func CassetteDir(backend string) string {
	return filepath.Join("testdata", "cassettes", backend)
}

// Proxy is the record/replay proxy a suite runs for the length of its run,
// and what it saw once stopped.
type Proxy = env.Proxy

// StartProxy brings up the record/replay proxy the container's HTTPS_PROXY
// already points at, signing with the CA scripts/testenv.sh minted and
// mounted into the container, with its cassettes under cassetteDir, and
// proves the container can reach it, saying on stderr what the container
// sees of the network.
func StartProxy(ctx context.Context, cassetteDir string) (*Proxy, error) {
	return embyfin.StartProxy(ctx, replayproxy.Options{
		CassetteDir: cassetteDir,
		// no API key may decide a cassette match or be committed with it:
		// an operator's own (TMDB's api_key) or the one a media server
		// carries for a provider (OMDb's apikey, which is Emby's and
		// Jellyfin's to rotate, not ours to publish)
		RedactQuery: []string{"api_key", "apikey"},
		// a provider's login answers with a bearer token for the media
		// server's own account; replay never needs one
		RedactBodyFields: []string{"token"},
		// Emby asks ipify, then its own service, for its public address on
		// startup, which is nobody's business and not part of any recording.
		// The media server reaching itself is not provider traffic either,
		// and go-kt adds the container's own addresses
		IgnoreHosts: []string{"api.ipify.org", "api64.ipify.org", "connect.emby.media"},
	})
}

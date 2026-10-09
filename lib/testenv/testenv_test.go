package testenv

import (
	"testing"

	"github.com/katbyte/go-kt/test/replayproxy"
)

// The environment is read as scripts/testenv.sh writes it, under this
// application's own names: a data dir that is not set is "", not ./media, so
// a test that lays files out skips rather than writing into the checkout, and
// a backend is part of being configured.
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

	for k, v := range map[string]string{"EMBYFIN_SERVER": "http://x", "EMBYFIN_TOKEN": "t", "EMBYFIN_BACKEND": ""} {
		t.Setenv(k, v)
	}
	if Configured() {
		t.Error("Configured() with no backend")
	}
	t.Setenv("EMBYFIN_BACKEND", "emby")
	if !Configured() || Backend() != "emby" {
		t.Errorf("Configured() = %v, Backend() = %q", Configured(), Backend())
	}
	t.Setenv("EMBYFIN_SERVER", "")
	if Configured() {
		t.Error("Configured() with no server")
	}
}

// What a run does with the providers is read from the two variables the
// suites and make record set.
func TestMode(t *testing.T) {
	// set here as well as in the loop, so that this is seen to be a test
	// that cannot run beside others
	t.Setenv("EMBYFIN_TEST_RECORD", "")
	for _, c := range []struct {
		record, verify string
		want           replayproxy.Mode
		recording      bool
	}{
		{"", "", replayproxy.Replay, false},
		{"1", "", replayproxy.Record, true},
		{"all", "", replayproxy.Rerecord, true},
		{"", "1", replayproxy.Verify, false},
	} {
		t.Setenv("EMBYFIN_TEST_RECORD", c.record)
		t.Setenv("EMBYFIN_TEST_VERIFY", c.verify)
		if Mode() != c.want || Recording() != c.recording || Verifying() != (c.verify != "") {
			t.Errorf("EMBYFIN_TEST_RECORD=%q EMBYFIN_TEST_VERIFY=%q: mode %v, recording %v, verifying %v", c.record, c.verify, Mode(), Recording(), Verifying())
		}
	}
}

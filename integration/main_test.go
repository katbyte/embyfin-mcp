//go:build integration

// Package integration runs the generated SDKs against what they talk to:
// lib/emby and lib/jf against a real server in Docker, and lib/tmdb against
// TMDB's recorded answers. It tests one thing: that every request the
// generated params structs build is one the server accepts, and that what it
// answers decodes into the generated types without losing what it carries.
// The bespoke tests check the answers the tools rely on field by field, and
// the read sweep calls every other GET and holds its answer to less (see
// sweep_test.go for what). Nothing here is about the tools or the neutral
// lib/embyfin layer; ../acceptance covers those.
//
// The two server SDKs are different packages with different types, so their
// tests are two parallel sets sharing one harness: emby_*_test.go runs when
// EMBYFIN_BACKEND=emby and jf_*_test.go when it is jellyfin. Each set creates
// its own libraries, scans them, and removes them again when the run ends:
// "SDK Movies", "SDK Shows" and "SDK Music" over the clean fixtures, "SDK
// Messy Movies" and "SDK Messy Shows" over the messy ones, and "SDK Scratch"
// and "SDK Bulk Delete" over folders the delete tests lay out themselves. The
// TMDB sweep (tmdb_sweep_test.go) needs no container and runs in any of them.
//
//	make testacc-integration                 # both backends, containers started and torn down
//	make testacc-integration-jellyfin        # one backend
//	make test-tmdb                           # the TMDB sweep alone, no container
//
//	eval "$(EMBYFIN_TEST_BACKEND=jellyfin scripts/testenv.sh up)"  # or drive it by hand
//	go test -tags integration ./integration/...
//	EMBYFIN_TEST_BACKEND=jellyfin scripts/testenv.sh down
package integration

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(runSuite(m)) }

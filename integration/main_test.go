//go:build integration

// Package integration runs the two generated SDKs, lib/emby and lib/jf,
// against a real server in Docker. It tests one thing: that every response
// the server sends decodes into the generated types with the fields actually
// populated, and that every request the generated params structs build is one
// the server accepts. Nothing here is about the tools or the neutral
// lib/embyfin layer; ../acceptance covers those.
//
// The two SDKs are different packages with different types, so the suite is
// two parallel sets of tests sharing one harness: emby_*_test.go runs when
// EMBYFIN_BACKEND=emby and jf_*_test.go when it is jellyfin. Each set creates
// its own libraries ("SDK Movies", "SDK Shows", "SDK Scratch"), scans them,
// and removes them again when the run ends.
//
//	make testacc-integration                 # both backends, containers started and torn down
//	make testacc-integration-jellyfin        # one backend
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

//go:build integration

// Package acceptance covers every tool against a real Emby or Jellyfin
// running in Docker - the API wrappers and the audits alike - so response
// shapes, query encoding and permissions are checked against the thing
// embyfin-mcp actually talks to rather than a stub. The same suite runs
// against both backends; EMBYFIN_BACKEND says which one is up.
//
// Fixtures are built through the tools themselves (library_create,
// library_scan, item_edit), so the setup is part of the coverage.
//
//	make testacc                                        # both backends, containers started and torn down
//	make testacc-acceptance-jellyfin                    # one backend
//
//	eval "$(EMBYFIN_TEST_BACKEND=jellyfin scripts/testenv.sh up)"  # or drive it by hand
//	go test -tags integration ./acceptance/...
//	EMBYFIN_TEST_BACKEND=jellyfin scripts/testenv.sh down
package acceptance

import "testing"

func TestMain(m *testing.M) { testMain(m) }

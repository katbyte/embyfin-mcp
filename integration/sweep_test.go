//go:build integration

package integration

// The read sweep: every GET operation in a server's definitions, called
// against the running server with arguments resolved from the fixtures, and
// its answer decoded (or its file read). The bespoke tests prove the shapes the
// tools rely on field by field; the sweep proves the rest of the read surface
// answers in its documented status with something in it, and that what it
// sends lands in the model, and that it keeps doing so as the definitions
// change: a GET the importer adds is swept on the next run with nothing to
// write, and one that fails must be classified here.
//
// What the sweep holds an answer to: it decodes; it carries something (a
// file of at least one byte, JSON with at least one value that is not empty,
// zero or false), because an empty list decodes into any model and so proves
// nothing about its shape; and no object in it is lost whole, which is how a
// model whose fields do not match the server shows up (the JSON decoder drops
// a key it has no field for without a word). It does not check every field:
// a server sends many its document does not declare, and a model that holds
// one of an object's keys holds that object.
//
// Every operation either answers with something, or has a sweep.Case that
// says why not: the feature needs something the container lacks (a tuner, a
// DLNA client, a transcoding session), the endpoint is gone from the server,
// the server answers a shape its document does not describe, or what the
// operation reads is not there on a fresh server. A Status, Decode or Empty
// case whose operation starts answering fails the sweep, so a stale one is
// noticed and removed, the way a stale importer workaround is. A Skip case
// is never called, because calling it would hang on ffmpeg, scan a network
// or need a fixture the container cannot have, so it is only ever reviewed by
// hand. Nothing is allowed to answer one way on some runs and another on
// others: where a server does, the test sets up what makes it answer the
// same way every time. The one way out is a MayBeEmpty case, for an answer
// that follows the server image rather than anything a test can set up.
//
// The sweep itself is pandorest's (github.com/katbyte/pandorest/sweep),
// shared with the other servers' SDKs. What is here is how this suite hands
// it a service: its checked-in definitions, the SDK's client, and the base
// client's reading of a status.

import (
	"testing"

	"github.com/katbyte/embyfin-mcp/sdk/pandorest/services"
	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/pandorest/config"
	"github.com/katbyte/pandorest/definitions"
	"github.com/katbyte/pandorest/sweep"
)

// runSweep calls every GET operation of the service on sdk.
func runSweep(t *testing.T, service string, sdk any, fixtures sweep.Fixtures, cases map[string]sweep.Case) {
	t.Helper()

	cfg, ok := config.Find(services.All, service)
	if !ok {
		t.Fatalf("no service %q", service)
	}
	cfg, err := cfg.In("..").Resolve()
	if err != nil {
		t.Fatal(err)
	}
	svc, err := definitions.Load(cfg.Path(cfg.Definitions))
	if err != nil {
		t.Fatal(err)
	}

	sweep.Sweep{Definitions: svc, Client: sdk, StatusCode: client.StatusCode, Fixtures: fixtures, Cases: cases}.Run(t)
}

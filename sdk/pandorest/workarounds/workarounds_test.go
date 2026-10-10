package workarounds

import (
	"testing"

	"github.com/katbyte/pandorest/config"
	pandorest "github.com/katbyte/pandorest/importer/workarounds"
	"github.com/katbyte/pandorest/openapi"

	"github.com/katbyte/embyfin-mcp/sdk/pandorest/services"
)

// Every workaround fixes a bug that is in its vendored document, and fails
// once the bug is gone.
func TestWorkarounds(t *testing.T) {
	t.Parallel()

	load := func(service string) (*openapi.Spec, error) {
		cfg, _ := config.Find(services.All, service)
		// tests run in the package directory, three below the repository root
		cfg, err := cfg.In("../../..").Resolve()
		if err != nil {
			return nil, err
		}

		return openapi.Load(cfg.Path(cfg.Spec))
	}
	if err := pandorest.Verify(All, load); err != nil {
		t.Error(err)
	}
}

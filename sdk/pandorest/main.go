// Command pandorest generates the Emby, Jellyfin and TMDB SDKs (sdk/emby,
// sdk/jf, sdk/tmdb) from their vendored OpenAPI documents, with
// github.com/katbyte/pandorest: this is embyfin's part of it, the services
// and the workarounds for their documents. Run from the repository root (the
// make targets do):
//
//	go run ./sdk/pandorest import
//	go run ./sdk/pandorest generate -service jellyfin
//	go run ./sdk/pandorest diff
package main

import (
	"github.com/katbyte/pandorest"

	"github.com/katbyte/embyfin-mcp/sdk/pandorest/services"
	"github.com/katbyte/embyfin-mcp/sdk/pandorest/workarounds"
)

func main() {
	pandorest.Main(pandorest.Config{Services: services.All, Workarounds: workarounds.All})
}

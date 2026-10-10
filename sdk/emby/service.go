package emby

import (
	"fmt"
	"net/http"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/pandorest/client"
)

// userAgent is sent with every request, and names the client to the server
// in its authorization header.
const userAgent = "embyfin-mcp"

// service is what is Emby's own about being talked to. A redirect is
// followed, with the credentials kept to the server's own host: a server
// behind a reverse proxy is often reached through one.
var service = client.Service{Name: "Emby", UserAgent: userAgent, Example: "nas:8096", SecretHeaders: []string{"X-Emby-Token", "X-Emby-Authorization"}, SecretNames: []string{"Pw", "NewPw", "CurrentPw"}, FollowRedirects: true, Note: note}

// credential is an API key or a user access token. Emby reads the token from
// X-Emby-Token and identifies the client from X-Emby-Authorization, which
// carries the token as well.
type credential string

func (t credential) Authorize(req *http.Request) {
	req.Header.Set("X-Emby-Token", string(t))
	req.Header.Set("X-Emby-Authorization", fmt.Sprintf(`Emby Client=%q, Device=%q, DeviceId=%q, Version=%q, Token=%q`, userAgent, userAgent, userAgent, version.Version, string(t)))
}

// note is what a refusal most often means on a server of the caller's own.
func note(f client.Failure) string {
	switch f.StatusCode {
	case http.StatusUnauthorized:
		return "API key rejected; check the token"
	case http.StatusForbidden:
		return "the API key's user lacks permission for this; most write operations need an administrator"
	}

	return ""
}

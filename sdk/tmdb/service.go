package tmdb

import (
	"net/http"
	"strings"

	"github.com/katbyte/pandorest/client"
)

// service is what is TMDB's own about being talked to.
var service = client.Service{Name: "TMDB", UserAgent: "embyfin-mcp", Example: DefaultBaseURL, FollowRedirects: true, Note: note}

// credential is an API Read Access Token, which is a JWT and goes in the
// Authorization header as a bearer, or the older API Key, which goes in the
// api_key query parameter.
type credential string

func (t credential) Authorize(req *http.Request) {
	if strings.HasPrefix(string(t), "eyJ") {
		req.Header.Set("Authorization", "Bearer "+string(t))

		return
	}
	q := req.URL.Query()
	q.Set("api_key", string(t))
	req.URL.RawQuery = q.Encode()
}

// note is what a refusal means on TMDB.
func note(f client.Failure) string {
	if f.StatusCode == http.StatusUnauthorized {
		return "API key rejected; check the token"
	}

	return ""
}

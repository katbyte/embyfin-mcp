// Package services lists the APIs embyfin generates SDKs for, the equivalent
// of Pandora's resource-manager.hcl. Paths are relative to the repository
// root, which is where the make targets run pandorest from.
package services

import (
	"fmt"

	"github.com/katbyte/pandorest/config"
)

// All is every service, in the order the make targets process them.
var All = []config.Service{
	{
		Name:      "emby",
		Package:   "emby",
		Output:    "sdk/emby",
		Naming:    config.PathNaming,
		TagSuffix: "Service",
		Auth:      "Emby",
		Client:    mediaServerClient("Emby"),
		Notes:     mediaServerDates,
		Paging:    mediaServerPaging,
		KeepNull:  mediaServerKeepNull,
	},
	{
		Name:     "jellyfin",
		Package:  "jf",
		Output:   "sdk/jf",
		Naming:   config.OperationIDNaming,
		Auth:     "Jellyfin",
		Client:   mediaServerClient("Jellyfin"),
		Notes:    mediaServerDates,
		Paging:   mediaServerPaging,
		KeepNull: mediaServerKeepNull,
	},
	{
		Name:    "tmdb",
		Package: "tmdb",
		Output:  "sdk/tmdb",
		Naming:  config.OperationIDNaming,
		Auth:    "TMDB",
		Client:  tmdbClient,
		Notes:   tmdbDates,
	},
}

// mediaServerKeepNull are the fields both servers leave out for "none" where
// 0 means something: an episode filed with no season or no number of its own
// (Jellyfin reads a file named without SxxEyy that way even in a "Season 01"
// folder, and Emby one at the show's root) is not a special, nor episode 0,
// and an account with no parental limit is not one held to the ratings
// scored 0 (Jellyfin scores G and TV-G 0).
var mediaServerKeepNull = []string{"BaseItemDto.ParentIndexNumber", "BaseItemDto.IndexNumber", "UserPolicy.MaxParentalRating"}

// mediaServerPaging is how both servers' lists page: from StartIndex, Limit at
// a time, answering Items and the TotalRecordCount.
var mediaServerPaging = &config.Paging{Start: "StartIndex", Limit: "Limit", Items: "Items", Total: "TotalRecordCount"}

// mediaServerDates is what no document says about its dates.
const mediaServerDates = "Dates are strings: the servers send RFC 3339 with seven fractional digits,\nsometimes with no zone."

// tmdbDates is what TMDB's document does not say about its dates.
const tmdbDates = "Dates are strings: TMDB writes a day as 2006-01-02, and a date it does not know\nreads as an empty string."

// mediaServerClient is the client.go of a server of the caller's own, which
// takes an API key or a user access token the way the package's own
// service.go sends one.
func mediaServerClient(server string) string {
	return fmt.Sprintf(`// Client is a client for the %[1]s API; each of its operations is a method.
// Requests go through Client.Client, the shared base client.
type Client struct {
	Client *client.Client
}

// New returns a client for the %[1]s server at baseURL (scheme and host,
// optionally a path prefix) that authenticates with an API key or a user
// access token.
func New(baseURL, token string, opts ...client.Option) (*Client, error) {
	if token == "" {
		return nil, errors.New("API key is required")
	}
	c, err := service.New(baseURL, credential(token), opts...)
	if err != nil {
		return nil, err
	}

	return &Client{Client: c}, nil
}
`, server)
}

// tmdbClient is the client for TMDB, a hosted API rather than a server of the
// caller's own: it lives at one address, and takes a read access token or an
// API key.
const tmdbClient = `// DefaultBaseURL is where the TMDB API lives.
const DefaultBaseURL = "https://api.themoviedb.org"

// Client is a client for the TMDB API; each of its operations is a method.
// Requests go through Client.Client, the shared base client.
type Client struct {
	Client *client.Client
}

// New returns a client for the TMDB API at baseURL (DefaultBaseURL, or a
// stand-in for it) that authenticates with an API Read Access Token or the
// older API Key.
func New(baseURL, token string, opts ...client.Option) (*Client, error) {
	if token == "" {
		return nil, errors.New("a TMDB read access token or API key is required")
	}
	c, err := service.New(baseURL, credential(token), opts...)
	if err != nil {
		return nil, err
	}

	return &Client{Client: c}, nil
}
`

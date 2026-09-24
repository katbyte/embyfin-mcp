package tools

import (
	"net/http"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// The session tools drive a real device, so the one they drive is the one
// asked for: an id first, then a device, app or user name matched whole, then
// a part of a name only one session has. A name more than one session answers
// to is refused with the candidates, never settled by whichever the server
// lists first.
func TestResolveSessionPicksOneDevice(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{
			{"Id": "s1", "UserName": "Quux", "Client": "Zzyzx Player", "DeviceName": "Lounge TV"},
			{"Id": "s2", "UserName": "Plugh", "Client": "Web", "DeviceName": "Laptop"},
			{"Id": "s3", "UserName": "Quux", "Client": "Web", "DeviceName": "Bedroom TV"},
			{"Id": "s4", "Client": "Zzyzx Player", "DeviceName": "TV"},
			{"Id": "s5", "Client": "Zzyzx Player", "DeviceName": "s1"},
		})
	})
	client, err := embyfin.New(embyfin.Emby, f.srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		target string
		want   string   // the session found
		errs   []string // or what the refusal says
	}{
		{target: "s2", want: "s2"},
		// an id wins over a device named like it
		{target: "s1", want: "s1"},
		// a whole name wins over names that hold it
		{target: "tv", want: "s4"},
		{target: "LAPTOP", want: "s2"},
		{target: "plugh", want: "s2"},
		// a part of one name alone
		{target: "lounge", want: "s1"},
		{target: "room tv", want: "s3"},
		// more than one answers: refused, naming them
		{target: "web", errs: []string{`"web" matches 2 sessions`, "Laptop (Web, Plugh) id s2", "Bedroom TV (Web, Quux) id s3"}},
		{target: "quux", errs: []string{`"quux" matches 2 sessions`, "Lounge TV", "Bedroom TV"}},
		{target: "zzy", errs: []string{`"zzy" matches 3 sessions`}},
		{target: "kitchen", errs: []string{`no session matching "kitchen"`, "Lounge TV (Zzyzx Player)"}},
		{target: " ", errs: []string{"session is required"}},
	} {
		s, err := resolveSession(t.Context(), client, tc.target)
		if len(tc.errs) > 0 {
			if err == nil {
				t.Errorf("%q found %s, want a refusal", tc.target, s.ID)
				continue
			}
			for _, want := range tc.errs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q: %v, want it to say %q", tc.target, err, want)
				}
			}
			continue
		}
		if err != nil || s.ID != tc.want {
			t.Errorf("%q found %v (%v), want %s", tc.target, s, err, tc.want)
		}
	}
}

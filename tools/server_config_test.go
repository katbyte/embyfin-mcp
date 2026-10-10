package tools

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

// jellyfinSettings is a Jellyfin settings document cut down to what the
// tests read, with two things a real one can hold that this client's
// version has never heard of: a setting a newer server added, and a number
// too long for a float to keep.
const jellyfinSettings = `{
  "ServerName": "Zzyzx",
  "CachePath": "/cache",
  "LogFileRetentionDays": 3,
  "LibraryScanFanoutConcurrency": 0,
  "LibraryMetadataRefreshConcurrency": 0,
  "PluginRepositories": [{"Name": "Zzyzx Plugins", "Url": "https://plugins.example.org/manifest.json", "Enabled": true}],
  "SortRemoveWords": ["the", "a"],
  "ZzyzxApiKey": "sekrit",
  "SettingFromANewerServer": {"Depth": 9007199254740993, "Modes": ["one", "two"]},
  "TrickplayOptions": {
    "EnableHwAcceleration": false, "EnableHwEncoding": false, "EnableKeyFrameOnlyExtraction": false,
    "ProcessThreads": 1, "Interval": 10000, "TileWidth": 10, "TileHeight": 10, "Qscale": 4, "JpegQuality": 90,
    "WidthResolutions": [320], "ScanBehavior": "NonBlocking"
  }
}`

// settingsServer is a Jellyfin that keeps the settings it is sent, and
// every document it was sent.
type settingsServer struct {
	*fakeServer

	mu     sync.Mutex
	doc    string
	posted []string
	// ignore makes the server answer a change and keep what it had
	ignore bool
}

func newSettingsServer(t *testing.T) *settingsServer {
	t.Helper()

	s := &settingsServer{fakeServer: newFakeServer(t), doc: jellyfinSettings}
	s.jellyfin = true
	s.mux.HandleFunc("GET /System/Configuration", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, s.doc)
	})
	s.mux.HandleFunc("POST /System/Configuration", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.posted = append(s.posted, string(raw))
		if !s.ignore {
			s.doc = string(raw)
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	s.mux.HandleFunc("GET /System/Info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"ServerName": "Zzyzx", "Version": "12.2.0", "HasPendingRestart": true})
	})

	return s
}

func (s *settingsServer) sent(t *testing.T) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.posted)
}

// settingsOf reads a settings document keeping every number's digits.
func settingsOf(t *testing.T, doc string) map[string]any {
	t.Helper()

	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}

	return out
}

// The server's settings are given in groups, by the server's own names: a
// group of settings inside the document one setting a name, a list of
// groups as how many it holds unless asked for whole, and a credential
// never.
func TestServerConfigReadsInGroups(t *testing.T) {
	t.Parallel()

	cs := session(t, newSettingsServer(t).fakeServer, Options{})
	out := mustCall(t, cs, "server_config", map[string]any{})
	groups := object(t, out["groups"], "groups")
	trickplay := object(t, groups["trickplay"], "trickplay")
	if len(trickplay) != 11 || trickplay["TrickplayOptions.ProcessThreads"] != 1.0 || trickplay["TrickplayOptions.ScanBehavior"] != "NonBlocking" || len(texts(out["editable"])) != len(jellyfinEditable) || out["backend"] != "jellyfin" {
		t.Errorf("trickplay = %v, editable %v", trickplay, out["editable"])
	}
	for setting, group := range map[string]string{
		"CachePath": "paths", "LogFileRetentionDays": "logs", "LibraryScanFanoutConcurrency": "scanning", "SortRemoveWords": "metadata",
		"ServerName": "other", "SettingFromANewerServer.Depth": "other", "PluginRepositories": "other", "ZzyzxApiKey": "other",
	} {
		if _, ok := object(t, groups[group], group)[setting]; !ok {
			t.Errorf("%s is not in %s: %v", setting, group, groups)
		}
	}
	other := object(t, groups["other"], "other")
	if other["ZzyzxApiKey"] != "***" || other["PluginRepositories"] != "1 entries, each a group of settings: ask with full to see them" {
		t.Errorf("a credential and a list of groups = %v and %v", other["ZzyzxApiKey"], other["PluginRepositories"])
	}

	// asked for whole, and for some groups only
	out = mustCall(t, cs, "server_config", map[string]any{"groups": []any{"Other", "paths"}, "full": true})
	groups = object(t, out["groups"], "groups")
	if repos := objects(t, object(t, groups["other"], "other")["PluginRepositories"], "PluginRepositories"); len(groups) != 2 || len(repos) != 1 || repos[0]["Name"] != "Zzyzx Plugins" {
		t.Errorf("two groups, whole = %v", groups)
	}
	if msg := mustRefuse(t, cs, "server_config", map[string]any{"groups": []any{"nope"}}); !strings.Contains(msg, `no group "nope": groups are trickplay, paths, logs`) {
		t.Errorf("an unknown group = %q", msg)
	}
}

// A change sends the server's own document back with the settings asked for
// changed and every other exactly as it came - the ones this client has
// never heard of, and a number's every digit - and answers with each
// setting before and after, read back.
func TestServerConfigEditChangesOnlyWhatIsAsked(t *testing.T) {
	t.Parallel()

	s := newSettingsServer(t)
	cs := session(t, s.fakeServer, Options{})
	out := mustCall(t, cs, "server_config_edit", map[string]any{"set": map[string]any{
		"TrickplayOptions.ProcessThreads": 4, "TrickplayOptions.EnableHwAcceleration": true, "TrickplayOptions.WidthResolutions": []any{320, 480},
		"LibraryScanFanoutConcurrency": 2, "TrickplayOptions.Qscale": 4,
	}})
	changed := objects(t, out["changed"], "changed")
	if len(changed) != 4 || changed[0]["setting"] != "LibraryScanFanoutConcurrency" || changed[0]["before"] != 0.0 || changed[0]["after"] != 2.0 || changed[2]["setting"] != "TrickplayOptions.ProcessThreads" {
		t.Errorf("changed = %v", changed)
	}
	if !slices.Equal(texts(out["unchanged"]), []string{"TrickplayOptions.Qscale"}) || !boolean(t, out["pending_restart"], "pending_restart") {
		t.Errorf("unchanged %v, pending restart %v", out["unchanged"], out["pending_restart"])
	}

	sent := s.sent(t)
	if len(sent) != 1 {
		t.Fatalf("the server was sent its settings %d times, want once", len(sent))
	}
	want := settingsOf(t, jellyfinSettings)
	trickplay := object(t, want["TrickplayOptions"], "TrickplayOptions")
	trickplay["ProcessThreads"], trickplay["EnableHwAcceleration"], trickplay["WidthResolutions"] = json.Number("4"), true, []any{json.Number("320"), json.Number("480")}
	want["LibraryScanFanoutConcurrency"] = json.Number("2")
	got := settingsOf(t, sent[0])
	if a, b := string(mustJSON(t, got)), string(mustJSON(t, want)); a != b {
		t.Errorf("the document sent back\n got %s\nwant %s", a, b)
	}
	if !strings.Contains(sent[0], "9007199254740993") || !strings.Contains(sent[0], `"ZzyzxApiKey":"sekrit"`) {
		t.Errorf("a long number or a credential did not go back as it came: %s", sent[0])
	}

	// asked for what the server already has, nothing is sent
	out = mustCall(t, cs, "server_config_edit", map[string]any{"set": map[string]any{"TrickplayOptions.ProcessThreads": 4}})
	if len(objects(t, out["changed"], "changed")) != 0 || !strings.Contains(text(out["note"]), "already had the value asked for") || len(s.sent(t)) != 1 {
		t.Errorf("a change to what is already so = %v, with %d documents sent", out, len(s.sent(t)))
	}
}

// What is refused, each before anything is sent: a setting that is not on
// the list, a value outside what Jellyfin's own dashboard allows, a value of
// the wrong kind, a setting this server's version does not have, and any
// change on Emby.
func TestServerConfigEditRefusals(t *testing.T) {
	t.Parallel()

	s := newSettingsServer(t)
	cs := session(t, s.fakeServer, Options{})
	for _, c := range []struct {
		set  map[string]any
		want string
	}{
		{map[string]any{}, "set is what to change"},
		{map[string]any{"CachePath": "/elsewhere"}, "CachePath is not a setting this tool changes"},
		{map[string]any{"PluginRepositories": []any{}}, "PluginRepositories is not a setting this tool changes"},
		{map[string]any{"TrickplayOptions.Qscale": 40}, "takes 2 to 31, not 40"},
		{map[string]any{"TrickplayOptions.Qscale": 1}, "takes 2 to 31, not 1"},
		{map[string]any{"TrickplayOptions.Interval": 0}, "takes 1 or more, not 0"},
		{map[string]any{"TrickplayOptions.ProcessThreads": -1}, "takes 0 or more, not -1"},
		{map[string]any{"TrickplayOptions.ProcessThreads": 2.5}, "takes a whole number, not 2.5"},
		{map[string]any{"TrickplayOptions.ProcessThreads": "four"}, "takes a whole number, not four"},
		{map[string]any{"TrickplayOptions.EnableHwEncoding": "yes"}, "is on or off: give true or false"},
		{map[string]any{"TrickplayOptions.WidthResolutions": []any{}}, "takes a list of whole numbers with at least one in it"},
		{map[string]any{"TrickplayOptions.WidthResolutions": []any{320, "wide"}}, "wide is not one"},
		{map[string]any{"TrickplayOptions.WidthResolutions": 320}, "takes a list of whole numbers"},
		// one good and one bad: neither is set
		{map[string]any{"TrickplayOptions.ProcessThreads": 8, "TrickplayOptions.JpegQuality": 101}, "takes 1 to 100, not 101"},
	} {
		if msg := mustRefuse(t, cs, "server_config_edit", map[string]any{"set": c.set}); !strings.Contains(msg, c.want) || (len(c.set) > 0 && !strings.Contains(msg, "Nothing was changed")) {
			t.Errorf("%v refused with %q, want %q and that nothing was changed", c.set, msg, c.want)
		}
	}
	if sent := s.sent(t); len(sent) != 0 {
		t.Errorf("a refused change sent the server %d documents", len(sent))
	}

	// a server whose version has no such setting is sent nothing either
	old := newSettingsServer(t)
	old.doc = `{"ServerName": "Zzyzx", "TrickplayOptions": {"ProcessThreads": 1}}`
	if msg := mustRefuse(t, session(t, old.fakeServer, Options{}), "server_config_edit", map[string]any{"set": map[string]any{"TrickplayOptions.JpegQuality": 80}}); !strings.Contains(msg, "hold no TrickplayOptions.JpegQuality") || len(old.sent(t)) != 0 {
		t.Errorf("a setting the server does not have = %q, with %d documents sent", msg, len(old.sent(t)))
	}

	// one that answers a change and keeps what it had is not believed
	deaf := newSettingsServer(t)
	deaf.ignore = true
	if msg := mustRefuse(t, session(t, deaf.fakeServer, Options{}), "server_config_edit", map[string]any{"set": map[string]any{"TrickplayOptions.ProcessThreads": 4}}); !strings.Contains(msg, "gives TrickplayOptions.ProcessThreads as 1, not the 4 asked for") {
		t.Errorf("a change the server did not keep = %q", msg)
	}

	// Emby's settings are read, and none is changed
	emby := newFakeServer(t)
	posts := 0
	emby.mux.HandleFunc("GET /System/Configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"ServerName": "Zzyzx", "CertificatePassword": "sekrit", "CertificatePath": "/certs/zzyzx.pfx", "LogFileRetentionDays": 3, "DatabaseCacheSizeMB": 400})
	})
	emby.mux.HandleFunc("POST /System/Configuration", func(http.ResponseWriter, *http.Request) { posts++ })
	ecs := session(t, emby, Options{})
	out := mustCall(t, ecs, "server_config", map[string]any{})
	groups := object(t, out["groups"], "groups")
	if len(texts(out["editable"])) != 0 || out["editable"] == nil || object(t, groups["network"], "network")["CertificatePassword"] != "***" || object(t, groups["paths"], "paths")["CertificatePath"] != "/certs/zzyzx.pfx" ||
		object(t, groups["database"], "database")["DatabaseCacheSizeMB"] != 400.0 || !strings.Contains(text(out["note"]), "none of Emby's server settings is changed") {
		t.Errorf("Emby's settings = %v", out)
	}
	if msg := mustRefuse(t, ecs, "server_config_edit", map[string]any{"set": map[string]any{"TrickplayOptions.ProcessThreads": 4}}); !strings.Contains(msg, "none of Emby's server settings is changed by this tool") || posts != 0 {
		t.Errorf("a change on Emby = %q, with %d posts", msg, posts)
	}
}

// Every setting on the list has a rule that takes a value of its kind, and
// the list is the one the tool's description gives.
func TestEditableSettingsAreTheTuningOnes(t *testing.T) {
	t.Parallel()

	want := []string{
		"LibraryMetadataRefreshConcurrency", "LibraryScanFanoutConcurrency",
		"TrickplayOptions.EnableHwAcceleration", "TrickplayOptions.EnableHwEncoding", "TrickplayOptions.EnableKeyFrameOnlyExtraction",
		"TrickplayOptions.Interval", "TrickplayOptions.JpegQuality", "TrickplayOptions.ProcessThreads", "TrickplayOptions.Qscale",
		"TrickplayOptions.TileHeight", "TrickplayOptions.TileWidth", "TrickplayOptions.WidthResolutions",
	}
	if got := slices.Sorted(maps.Keys(jellyfinEditable)); !slices.Equal(got, want) {
		t.Errorf("editable settings = %v\nwant %v", got, want)
	}
	for name, rule := range jellyfinEditable {
		var good any = json.Number("4")
		switch rule.kind {
		case "switch":
			good = true
		case "numbers":
			good = []any{json.Number("320")}
		}
		if _, err := rule.check(name, good); err != nil || rule.about == "" {
			t.Errorf("%s refuses %v (%v), or says nothing of what it is", name, good, err)
		}
	}
}

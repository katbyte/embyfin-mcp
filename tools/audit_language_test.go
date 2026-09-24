package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
)

// The same language is written three ways, and a German track written "ger"
// is not missing from a file asked about in "deu".
func TestLanguageKey(t *testing.T) {
	t.Parallel()

	for _, same := range [][]string{
		{"eng", "en", "ENG", " en "},
		{"deu", "ger", "de"},
		{"fra", "fre", "fr"},
		{"zho", "chi", "zh"},
		{"jpn", "ja"},
		{"nor", "nob", "no", "nb"},
	} {
		for _, code := range same[1:] {
			if languageKey(code) != languageKey(same[0]) {
				t.Errorf("%q and %q are the same language: %q against %q", code, same[0], languageKey(code), languageKey(same[0]))
			}
		}
	}
	// a code nobody mapped is compared as written, not dropped
	if languageKey("haw") != "haw" {
		t.Errorf("haw = %q", languageKey("haw"))
	}
}

func item(sources ...embyfin.MediaSource) *embyfin.Item {
	return &embyfin.Item{Name: "x", MediaSources: sources}
}

func tracks(streams ...embyfin.MediaStream) embyfin.MediaSource {
	return embyfin.MediaSource{MediaStreams: streams}
}

func audioIn(lang string) embyfin.MediaStream {
	return embyfin.MediaStream{Type: "Audio", Language: lang}
}

func subsIn(lang string) embyfin.MediaStream {
	return embyfin.MediaStream{Type: "Subtitle", Language: lang}
}

func TestCheckLanguage(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		it             *embyfin.Item
		lang, find     string
		match, unknown bool
	}{
		{"japanese audio found", item(tracks(audioIn("jpn"))), "jpn", findAudio, true, false},
		{"found by another spelling", item(tracks(audioIn("ger"))), "de", findAudio, true, false},
		{"english audio is not japanese", item(tracks(audioIn("eng"))), "jpn", findAudio, false, false},
		{"subtitles found", item(tracks(audioIn("jpn"), subsIn("eng"))), "eng", findSubtitles, true, false},
		{"any version counts", item(tracks(audioIn("eng")), tracks(audioIn("jpn"))), "jpn", findAudio, true, false},

		{"no japanese audio", item(tracks(audioIn("eng"))), "jpn", findNoAudio, true, false},
		{"japanese audio is not lacking it", item(tracks(audioIn("jpn"))), "jpn", findNoAudio, false, false},
		// an untagged track may be the language asked about, so the item is not
		// reported as lacking it
		{"untagged audio cannot be called lacking", item(tracks(audioIn(""))), "jpn", findNoAudio, false, true},
		{"und is untagged", item(tracks(audioIn("und"))), "jpn", findNoAudio, false, true},
		{"a tagged match beside an untagged track is still a match", item(tracks(audioIn("jpn"), audioIn(""))), "jpn", findAudio, true, false},
		// a file with no audio track at all is as often one the server never
		// probed as a silent one, so it is not called lacking the language
		{"no audio track at all is not judged", item(tracks(embyfin.MediaStream{Type: "Video"})), "eng", findNoAudio, false, true},
		{"no streams at all is not judged", item(embyfin.MediaSource{}), "eng", findNoAudio, false, true},

		{"japanese audio with no english anywhere cannot be watched in english", item(tracks(audioIn("jpn"))), "eng", findUnwatchable, true, false},
		{"english subtitles make it watchable", item(tracks(audioIn("jpn"), subsIn("en"))), "eng", findUnwatchable, false, false},
		{"untagged subtitles might be english", item(tracks(audioIn("jpn"), subsIn(""))), "eng", findUnwatchable, false, true},
		{"nothing known is not unwatchable", item(), "eng", findUnwatchable, false, true},
		{"subtitles in it make even a file with no audio watchable", item(tracks(subsIn("eng"))), "eng", findUnwatchable, false, false},
	} {
		detail, match, unknown := checkLanguage(tc.it, tc.lang, tc.find)
		if match != tc.match || unknown != tc.unknown {
			t.Errorf("%s: match %v unknown %v, want %v %v (%s)", tc.name, match, unknown, tc.match, tc.unknown, detail)
		}
	}

	// the detail says what is there, so a caller can see why
	detail, _, _ := checkLanguage(item(tracks(audioIn("jpn"), audioIn(""), subsIn("eng"))), "jpn", findAudio)
	if detail != "audio: jpn, untagged; subtitles: eng" {
		t.Errorf("detail = %q", detail)
	}
}

// The tool end to end, against the canned library, whose episodes carry one
// English audio track and no subtitles.
func TestAuditLanguage(t *testing.T) {
	t.Parallel()

	cs := session(t, tvServer(t, showLibrary(2, 1, 2)...), Options{})

	english := mustCall(t, cs, "audit_language", map[string]any{"language": "en"})
	if n := number(t, english["total_findings"], "total_findings"); n != 4 {
		t.Errorf("english audio across four episodes = %d, want 4", n)
	}
	if row := objects(t, english["findings"], "findings")[0]; !strings.Contains(text(row["detail"]), "audio: eng") {
		t.Errorf("finding = %v", row)
	}

	for find, want := range map[string]int{findAudio: 0, findNoAudio: 4, findUnwatchable: 4, findSubtitles: 0} {
		out := mustCall(t, cs, "audit_language", map[string]any{"language": "jpn", "find": find})
		if n := number(t, out["total_findings"], "total_findings"); n != want {
			t.Errorf("jpn %s = %d, want %d", find, n, want)
		}
		if number(t, out["items_scanned"], "items_scanned") != 4 || number(t, out["untagged"], "untagged") != 0 {
			t.Errorf("jpn %s: %v", find, out)
		}
	}

	for args, want := range map[string]map[string]any{
		"required":     {"language": ""},
		"no language":  {"language": "und"},
		"find must be": {"language": "eng", "find": "missing"},
	} {
		if msg := mustRefuse(t, cs, "audit_language", want); !strings.Contains(msg, args) {
			t.Errorf("%v said: %s", want, msg)
		}
	}
}

// Records with no file, and files whose streams say nothing about audio,
// are not reported as lacking a language: the audit never judges on facts
// it does not have. The files with no audio track are counted instead.
func TestAuditLanguageLeavesOutWhatItKnowsNothingAbout(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /Library/VirtualFolders/Query", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Shows","ItemId":"lib","CollectionType":"tvshows","Locations":["/tv"]}],"TotalRecordCount":1}`)
	})
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"TotalRecordCount": 4, "Items": []map[string]any{
			{
				"Id": "1", "Name": "English", "Type": "Episode", "SeriesName": "Zzyzx Show", "Path": "/tv/z/1.mkv", "LocationType": "FileSystem",
				"MediaSources": []map[string]any{{"Size": 5, "MediaStreams": []map[string]any{{"Type": "Audio", "Language": "eng"}}}},
			},
			// never probed: a file with no streams
			{"Id": "2", "Name": "Unprobed", "Type": "Episode", "SeriesName": "Zzyzx Show", "Path": "/tv/z/2.mkv", "LocationType": "FileSystem", "MediaSources": []map[string]any{{"Size": 0}}},
			// a picture with no sound track
			{
				"Id": "3", "Name": "Silent", "Type": "Episode", "SeriesName": "Zzyzx Show", "Path": "/tv/z/3.mkv", "LocationType": "FileSystem",
				"MediaSources": []map[string]any{{"Size": 5, "MediaStreams": []map[string]any{{"Type": "Video", "Codec": "h264"}}}},
			},
			// the record a server keeps of an episode it has no file for
			{"Id": "4", "Name": "Not Held", "Type": "Episode", "SeriesName": "Zzyzx Show", "LocationType": "Virtual"},
		}})
	})
	cs := session(t, f, Options{})

	for _, find := range []string{findNoAudio, findUnwatchable} {
		out := mustCall(t, cs, "audit_language", map[string]any{"language": "jpn", "find": find})
		rows := objects(t, out["findings"], "findings")
		if len(rows) != 1 || text(rows[0]["id"]) != "1" || number(t, out["total_findings"], "total_findings") != 1 {
			t.Errorf("%s: findings = %v, want only the file with English audio", find, rows)
		}
		if number(t, out["no_audio_track"], "no_audio_track") != 2 || number(t, out["untagged"], "untagged") != 0 {
			t.Errorf("%s: %v, want the two files with no audio track counted apart", find, out)
		}
		// the record with no file is not an item the audit looked at
		if number(t, out["items_scanned"], "items_scanned") != 3 {
			t.Errorf("%s: items_scanned = %v, want the three files", find, out["items_scanned"])
		}
	}
}

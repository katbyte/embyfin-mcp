package tools

import (
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
		{"no audio tracks at all lacks every language", item(tracks(embyfin.MediaStream{Type: "Video"})), "eng", findNoAudio, true, false},

		{"japanese audio with no english anywhere cannot be watched in english", item(tracks(audioIn("jpn"))), "eng", findUnwatchable, true, false},
		{"english subtitles make it watchable", item(tracks(audioIn("jpn"), subsIn("en"))), "eng", findUnwatchable, false, false},
		{"untagged subtitles might be english", item(tracks(audioIn("jpn"), subsIn(""))), "eng", findUnwatchable, false, true},
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

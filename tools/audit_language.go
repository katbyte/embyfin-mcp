package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Finding what a library holds in a language, and what it cannot be watched
// in.
//
// This is an audit rather than a search filter because Emby cannot filter
// items by the language of their streams - its item query knows audio codecs
// and whether subtitles exist, not which languages - so answering at all
// means reading every file's streams. That is what an audit does anyway, and
// the question worth a worklist is the lacking one: what has no audio and no
// subtitles a viewer can follow.

// The questions audit_language answers.
const (
	findAudio       = "audio"
	findSubtitles   = "subtitles"
	findNoAudio     = "no_audio"
	findUnwatchable = "unwatchable"
)

// languageKey is the one spelling a language is compared by. Files and
// servers write the same language three ways - ISO 639-1 "en", 639-2/T "eng"
// and, for a couple of dozen languages, a different 639-2/B code: German is
// "ger" in one file and "deu" in the next. Comparing the codes as written
// would call a German track missing from a file that has one.
func languageKey(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if canonical, ok := languageAliases[code]; ok {
		return canonical
	}

	return code
}

// languageAliases maps ISO 639-1 codes and 639-2/B codes onto 639-2/T, for
// the languages a media library is likely to hold. A code not here is
// compared as written.
var languageAliases = map[string]string{
	"en": "eng", "ja": "jpn", "fr": "fra", "fre": "fra", "de": "deu", "ger": "deu", "es": "spa", "it": "ita",
	"pt": "por", "ru": "rus", "zh": "zho", "chi": "zho", "ko": "kor", "nl": "nld", "dut": "nld", "sv": "swe",
	"no": "nor", "nb": "nor", "nob": "nor", "nn": "nor", "nno": "nor", "da": "dan", "fi": "fin", "pl": "pol",
	"cs": "ces", "cze": "ces", "el": "ell", "gre": "ell", "he": "heb", "hi": "hin", "ar": "ara", "tr": "tur",
	"th": "tha", "vi": "vie", "id": "ind", "ms": "msa", "may": "msa", "hu": "hun", "ro": "ron", "rum": "ron",
	"uk": "ukr", "fa": "fas", "per": "fas", "sk": "slk", "slo": "slk", "cy": "cym", "wel": "cym", "is": "isl",
	"ice": "isl", "hr": "hrv", "sr": "srp", "bg": "bul", "ca": "cat", "ta": "tam", "te": "tel", "tl": "tgl",
}

// untaggedLanguage says whether a track names no language at all, which the
// servers write as nothing or as "und".
func untaggedLanguage(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))

	return code == "" || code == "und"
}

// trackLanguages is what an item's files carry, across every version of it:
// a language one version has is a language the item can be played in.
type trackLanguages struct {
	audio, subtitles                 []string
	untaggedAudio, untaggedSubtitles bool
}

func languagesOf(it *embyfin.Item) trackLanguages {
	var out trackLanguages
	for i := range it.MediaSources {
		for _, st := range it.MediaSources[i].MediaStreams {
			switch st.Type {
			case "Audio":
				if untaggedLanguage(st.Language) {
					out.untaggedAudio = true
				} else if key := languageKey(st.Language); !slices.Contains(out.audio, key) {
					out.audio = append(out.audio, key)
				}
			case "Subtitle":
				if untaggedLanguage(st.Language) {
					out.untaggedSubtitles = true
				} else if key := languageKey(st.Language); !slices.Contains(out.subtitles, key) {
					out.subtitles = append(out.subtitles, key)
				}
			}
		}
	}

	return out
}

func (l trackLanguages) String() string {
	list := func(langs []string, untagged bool) string {
		parts := slices.Clone(langs)
		if untagged {
			parts = append(parts, "untagged")
		}
		if len(parts) == 0 {
			return "none"
		}

		return strings.Join(parts, ", ")
	}

	return "audio: " + list(l.audio, l.untaggedAudio) + "; subtitles: " + list(l.subtitles, l.untaggedSubtitles)
}

// noAudio says the files carry no audio track at all. A file like that is as
// often one the server never probed (an unprobed file answers every question
// about its streams with nothing) as one that is silent, so there is no
// track to judge a language by.
func (l trackLanguages) noAudio() bool {
	return len(l.audio) == 0 && !l.untaggedAudio
}

// checkLanguage answers one item. unknown is set when the answer turns on
// something the files do not say: a track that names no language, which
// may well be the language asked about, or no audio track at all. An item is
// never reported as lacking a language on the strength of a fact it does not
// have.
func checkLanguage(it *embyfin.Item, language, find string) (detail string, match, unknown bool) {
	l := languagesOf(it)
	key := languageKey(language)
	hasAudio, hasSubtitles := slices.Contains(l.audio, key), slices.Contains(l.subtitles, key)

	switch find {
	case findAudio:
		match = hasAudio
	case findSubtitles:
		match = hasSubtitles
	case findNoAudio:
		if !hasAudio {
			unknown = l.untaggedAudio || l.noAudio()
			match = !unknown
		}
	case findUnwatchable:
		if !hasAudio && !hasSubtitles {
			unknown = l.untaggedAudio || l.untaggedSubtitles || l.noAudio()
			match = !unknown
		}
	}

	return l.String(), match, unknown
}

// languageIn is audit_language's input.
type languageIn struct {
	Language string `json:"language"          jsonschema:"ISO 639 code: eng or en, jpn or ja, deu or ger..."`
	Find     string `json:"find,omitempty"    jsonschema:"audio (default): has audio in it; subtitles: has subtitles in it; no_audio: has no audio in it; unwatchable: has neither audio nor subtitles in it"`
	Library  string `json:"library,omitempty" jsonschema:"one library by name or id"`
	Types    string `json:"types,omitempty"   jsonschema:"comma-separated item types; default Movie,Episode"`
	Limit    int    `json:"limit,omitempty"   jsonschema:"maximum findings, default 100"`
}

// languageOut is audit_language's answer.
type languageOut struct {
	auditOut
	Untagged int `json:"untagged"       jsonschema:"items not judged because the track the answer turns on names no language; counted in neither total_findings nor the rest"`
	NoAudio  int `json:"no_audio_track" jsonschema:"no_audio and unwatchable: files not judged because they carry no audio track at all, as often because the server never probed them as because they are silent; counted in neither total_findings nor untagged"`
}

func auditLanguage(ctx context.Context, client *embyfin.Client, in languageIn) (languageOut, error) {
	if strings.TrimSpace(in.Language) == "" {
		return languageOut{}, errors.New("language is required: an ISO 639 code such as eng or jpn")
	}
	if untaggedLanguage(in.Language) {
		return languageOut{}, errors.New("und means no language; ask for a language, and read untagged for the items whose tracks name none")
	}
	find := in.Find
	if find == "" {
		find = findAudio
	}
	if !slices.Contains([]string{findAudio, findSubtitles, findNoAudio, findUnwatchable}, find) {
		return languageOut{}, fmt.Errorf("find must be audio, subtitles, no_audio or unwatchable, not %q", in.Find)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}

	opts, err := sweepOptions(ctx, client, in.Library, in.Types, "Movie,Episode", "Path,ProductionYear,MediaSources")
	if err != nil {
		return languageOut{}, err
	}

	out := languageOut{Findings: []auditFinding{}}
	var findings []auditFinding
	// as people are shown it, every version together: on Emby each version
	// is stored as an item of its own, and one read alone lacked what
	// another version has
	items, err := shownItems(ctx, client, opts)
	if err != nil {
		return languageOut{}, err
	}
	for i := range items {
		it := &items[i]
		// the record a server keeps of an episode it has no file for has no
		// streams to read, and is not something the library can be watched
		// in or not
		if !it.HasFile() {
			continue
		}
		out.Scanned++
		detail, match, unknown := checkLanguage(it, in.Language, find)
		switch {
		case unknown && languagesOf(it).noAudio():
			out.NoAudio++
		case unknown:
			out.Untagged++
		case match:
			name := it.Name
			if it.SeriesName != "" {
				name = fmt.Sprintf("%s S%02dE%02d %s", it.SeriesName, it.ParentIndexNumber, it.IndexNumber, it.Name)
			}
			findings = append(findings, auditFinding{ID: it.ID, Name: name, Year: it.ProductionYear, Path: it.Path, Detail: detail})
		}
	}

	slices.SortStableFunc(findings, func(a, b auditFinding) int { return strings.Compare(a.Name, b.Name) })
	out.Found = len(findings)
	out.Findings = append(out.Findings, findings[:min(len(findings), limit)]...)

	return out, nil
}

func registerLanguageAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_language",
		Description: "Find films and episodes by the language of their audio or subtitles: what has audio or subtitles in a language, what has no audio in it, or what cannot be watched in it at all (neither audio nor subtitles). Any version of an item counts, every version the server shows it in read together (Emby stores each as an item of its own and merges them only in what it shows people, so on Emby this reads the library as the first administrator is shown it). " +
			"A track with no language tag is never taken as lacking the language: an item whose answer turns on one is counted in untagged instead of reported. Nor is a file with no audio track at all, which as often means the server never probed it as that it is silent: those are counted in no_audio_track. A record of an episode with no file is left out.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in languageIn) (*mcp.CallToolResult, languageOut, error) {
		out, err := auditLanguage(ctx, client, in)

		return nil, out, err
	})
}

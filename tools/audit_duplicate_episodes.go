package tools

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// One episode's content filed under two episode numbers.
//
// Neither existing audit sees this. audit_duplicates matches provider ids, and
// the two entries carry different ones because the server thinks they are
// different episodes. audit_multiple_versions finds several files merged under
// ONE item, which is the opposite arrangement. What is left is the plainest
// evidence there is: the same season holding the same episode title twice.
//
// A library can carry such a pair for years, the two files running within
// seconds of each other, with nothing reporting it.
//
// What makes a pair near certain is the files, not the titles or runtimes
// alike: every episode of a sitcom, a talk show or an anime runs within a
// few percent of the next, so a title a season carries twice on purpose - a
// two-parter without its (1) and (2), a placeholder - ran as close as a copy
// does, was called near certain, and a real episode was deleted as the
// copy. A copy shows itself in the file: the same runtime to the second,
// the same content played at PAL's speed (a 23.976 master run at 25 frames,
// about 4% shorter), or the same size to the byte - each only where no other
// episode of the season shares it, since a show cut to a fixed length, or a
// season of placeholder files, has every episode alike.

// titleRow is one member of a repeated-title group.
type titleRow struct {
	ID       string       `json:"id"`
	Episode  *int         `json:"episode"             jsonschema:"null when the server holds no episode number for it"`
	Title    string       `json:"title"`
	Path     string       `json:"path,omitempty"`
	RuntimeS int          `json:"runtime_s,omitempty"`
	Unprobed bool         `json:"unprobed,omitempty"  jsonschema:"the server never read the file: runtime_s is the metadata's rather than the file's, and says nothing about whether the entries are one"`
	Size     int64        `json:"size,omitempty"      jsonschema:"file size in bytes"`
	Bitrate  int64        `json:"bitrate,omitempty"`
	Height   int          `json:"height,omitempty"`
	Audio    []audioTrack `json:"audio,omitempty"`
}

// titleGroup is one season's repeated title.
type titleGroup struct {
	Series     string     `json:"series"`
	SeriesID   string     `json:"series_id"`
	Season     int        `json:"season"`
	Title      string     `json:"title"`
	Episodes   []titleRow `json:"episodes"           jsonschema:"the entries carrying that title, by episode number"`
	RuntimeGap float64    `json:"runtime_gap"        jsonschema:"how far apart the runtimes are, as a fraction of the longest; a title whose entries are more than 0.15 apart is a far_apart group"`
	Confidence string     `json:"confidence"         jsonschema:"near_certain: the files tie every entry to another - the same runtime to the second, one the other's at PAL speed, or the same size to the byte, each shared by no episode of the season under another title (a runtime within a few seconds, or half a percent, is shared), and a runtime only when two or more episodes under other titles run lengths that differ, read off their files, and never for a placeholder title (TBA, 'Episode 5', a number alone) (evidence says which). lead: one title and runtimes within 15%, and nothing in the files to say they are one - which a two-parter without its (1) and (2), or a placeholder title, is too, so check before deleting either. far_apart: the title's entries run more than 15% apart, every one of them listed - a copy cut short, one file holding two episodes, an extended cut under a second number, or two episodes that share a title; entries among them that run alike are a group of their own as well"`
	Evidence   []string   `json:"evidence,omitempty" jsonschema:"what the files say, pair by pair, e.g. 'E02 and E05: the same runtime to the second', and why a match in them is no sign (another episode of the season shares it, or the files are too small to be video)"`
}

type dupTitlesIn struct {
	Library string `json:"library,omitempty" jsonschema:"one library by name or id"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum groups, default 50"`
}

type dupTitlesOut struct {
	Scanned int          `json:"items_scanned"`
	Found   int          `json:"total_findings"`
	Groups  []titleGroup `json:"groups"         jsonschema:"near_certain groups first, then leads, then far_apart; capped at limit"`
	// changed is what the reads said of the library changing, apart from
	// the rest of the note: what audit_all reports
	changed string
	Note    string `json:"note,omitempty" jsonschema:"set when the library was seen to change while it was read: items added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked. Empty when no item was seen to come or go from the read's first page to its last, and an item changed meanwhile is answered as it was read. On Emby, an audit of what people are shown also says how the items shown only as versions of others were placed: by the key Emby merges them by, with a sample checked against a read of each, or by a read of each"`
}

func registerDuplicateEpisodesAudit(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_duplicate_episodes",
		Description: "Find one episode's content filed under two episode numbers: a season holding the same episode title twice. " +
			"Neither other duplicate audit sees this - audit_duplicates matches provider ids, which differ because the server believes they are different episodes, and audit_multiple_versions finds several files under one item (so two files the server shows as one episode's versions are not a finding here; on Emby, which merges them only in what it shows people, this reads the library as the first administrator is shown it). " +
			"near_certain needs the files to say it: the same runtime to the second, one the other's at PAL speed (about 4% shorter), or the same size to the byte, and evidence says which - a match an episode of the season under another title shares is no sign (a show cut to a fixed length; a runtime within a few seconds, or half a percent, is shared), nor is a runtime unless two or more episodes under other titles run lengths that differ, nor any runtime for a placeholder title (TBA, 'Episode 5', a number alone), which the unnamed episodes of a show cut to one slot share, nor a size of files too small to be video (placeholders, .strm links); entries of the one title sharing it are more copies, and three copies of one file are one near_certain group. One title with runtimes within 15% and nothing more is a lead: every episode of a sitcom or an anime runs within a few percent of the next, and a season can carry a title twice on purpose - a two-parter without its (1) and (2), a placeholder - so check a lead before deleting either. A title whose entries run more than 15% apart is a far_apart group, every entry listed: a copy cut short, a file holding two episodes, an extended cut, or two episodes of one title. " +
			"It does not pick a winner: the larger file can be the worse copy. A show's extras, which Emby 4.10 holds as episodes when they sit in a season's Extras folder, are left out.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dupTitlesIn) (*mcp.CallToolResult, dupTitlesOut, error) {
		out, err := auditDuplicateEpisodes(ctx, client, in)

		return nil, out, err
	})
}

func auditDuplicateEpisodes(ctx context.Context, client *embyfin.Client, in dupTitlesIn) (dupTitlesOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	opts, err := sweepOptions(ctx, client, in.Library, typeEpisode, typeEpisode, "Path,MediaSources")
	if err != nil {
		return dupTitlesOut{}, err
	}

	type key struct {
		series, title string
		season        int
	}
	type seasonKey struct {
		series string
		season int
	}
	seasons := map[key][]embyfin.Item{}
	// every entry of a season, which says whether a length or a size two
	// entries share is theirs alone
	whole := map[seasonKey][]embyfin.Item{}
	out := dupTitlesOut{Groups: []titleGroup{}}
	// as people are shown them: two files Emby shows as one episode's
	// versions are one episode (audit_multiple_versions), where a sweep of
	// what it stores holds each as an entry carrying the same title
	items, note, placing, err := client.Shown(ctx, opts)
	if err != nil {
		return dupTitlesOut{}, err
	}
	out.changed, out.Note = note, joinWarnings(note, placing)
	for i := range items {
		it := &items[i]
		// two seasons' featurettes Emby took for episodes are not one
		// episode filed twice
		if extraEpisode(it) {
			continue
		}
		out.Scanned++
		title := strings.TrimSpace(strings.ToLower(it.Name))
		// an episode the server holds no season number for is in no season
		// to hold its title twice
		if it.SeriesID == "" || title == "" || !it.HasFile() || it.ParentIndexNumber == nil {
			continue
		}
		k := key{series: it.SeriesID, season: *it.ParentIndexNumber, title: title}
		seasons[k] = append(seasons[k], *it)
		sk := seasonKey{it.SeriesID, *it.ParentIndexNumber}
		whole[sk] = append(whole[sk], *it)
	}

	var groups []titleGroup
	for k, items := range seasons {
		if len(items) < 2 {
			continue
		}
		slices.SortFunc(items, func(a, b embyfin.Item) int {
			return cmp.Or(compareNumbers(a.IndexNumber, b.IndexNumber), strings.Compare(a.ID, b.ID))
		})
		// one title joins entries running within 15% of each other, each
		// tried against every one before it: a title a season repeats joins
		// the episodes that could be one, not every one that carries it.
		// The 15% holds across a whole group, its shortest against its
		// longest, so a run of lengths each a little longer than the last
		// does not chain two far apart into one
		j := newJoins(len(items))
		shortest, longest := make([]int64, len(items)), make([]int64, len(items))
		for i := range items {
			shortest[i], longest[i] = knownRuntime(&items[i]), knownRuntime(&items[i])
		}
		for b := range items {
			for a := range b {
				ra, rb := j.find(a), j.find(b)
				lo, hi := minKnown(shortest[ra], shortest[rb]), max(longest[ra], longest[rb])
				if ra == rb || !runtimesAlike(lo, hi) {
					continue
				}
				root := j.join(a, b)
				shortest[root], longest[root] = lo, hi
			}
		}
		season := whole[seasonKey{k.series, k.season}]
		clusters := 0
		for i := range items {
			if j.find(i) == i {
				clusters++
			}
		}
		for _, members := range j.groups() {
			groups = append(groups, titleGroupOf(k.series, k.season, items, members, season))
		}
		// lengths too far apart to be one cut are said, not dropped: a copy
		// cut short, two episodes in one file and an extended cut all run
		// far from the episode they copy
		if clusters > 1 {
			all := make([]int, len(items))
			for i := range all {
				all[i] = i
			}
			far := titleGroupOf(k.series, k.season, items, all, season)
			far.Confidence, far.Evidence = "far_apart", nil
			groups = append(groups, far)
		}
	}

	// a whole order, down to the season and the series id: sorted by series
	// name and title alone, two groups of one title in different seasons (or
	// under two series of one name) came out in the map's order, and which of
	// them a limit kept changed from one call to the next
	rank := map[string]int{"near_certain": 0, "lead": 1, "far_apart": 2}
	slices.SortFunc(groups, func(a, b titleGroup) int {
		return cmp.Or(
			cmp.Compare(rank[a.Confidence], rank[b.Confidence]),
			strings.Compare(a.Series, b.Series),
			strings.Compare(a.SeriesID, b.SeriesID),
			cmp.Compare(a.Season, b.Season),
			strings.Compare(a.Title, b.Title),
		)
	})
	out.Found = len(groups)
	out.Groups = append(out.Groups, groups[:min(len(groups), limit)]...)

	return out, nil
}

// titleGroupOf is one group of entries sharing a title: its rows, how far
// apart they run, and how sure it is, by what their files say against the
// rest of the season.
func titleGroupOf(seriesID string, seasonNumber int, items []embyfin.Item, members []int, season []embyfin.Item) titleGroup {
	group := titleGroup{Series: items[members[0]].SeriesName, SeriesID: seriesID, Season: seasonNumber, Title: items[members[0]].Name}
	shortest, longest := 0, 0
	for _, i := range members {
		it := &items[i]
		q := pathQuality(it)
		runtime := int(it.RunTimeTicks / ticksPerSecond)
		group.Episodes = append(group.Episodes, titleRow{
			ID: it.ID, Episode: it.IndexNumber, Title: it.Name, Path: it.Path,
			RuntimeS: runtime, Unprobed: !it.Probed(), Size: q.Size, Bitrate: q.Bitrate, Height: q.Height, Audio: q.Audio,
		})
		if runtime > 0 && (shortest == 0 || runtime < shortest) {
			shortest = runtime
		}
		longest = max(longest, runtime)
	}
	if longest > 0 && shortest > 0 {
		group.RuntimeGap = math.Round(float64(longest-shortest)/float64(longest)*100) / 100
	}

	// near certain only when the files tie every entry to another; what
	// the files share is judged against the season's other titles, as
	// entries of this one sharing it too are more copies, not a sign against
	title := map[string]bool{}
	for i := range items {
		title[items[i].ID] = true
	}
	signs := newJoins(len(members))
	for b := range members {
		for a := range b {
			x, y := &items[members[a]], &items[members[b]]
			sign, weak := copySign(x, y, season, title)
			if sign != "" {
				signs.join(a, b)
			}
			for _, said := range []string{sign, weak} {
				if said != "" {
					group.Evidence = append(group.Evidence, fmt.Sprintf("E%s and E%s: %s", numberText(x.IndexNumber), numberText(y.IndexNumber), said))
				}
			}
		}
	}
	group.Confidence = "lead"
	if tied := signs.groups(); len(tied) == 1 && len(tied[0]) == len(members) {
		group.Confidence = "near_certain"
	}

	return group
}

// palSpeedup is how much longer a film or an episode runs at the 23.976
// frames a second it was made at than at PAL's 25: the same content, sped
// up about 4%, which a copy from a PAL source runs short by.
const palSpeedup = 25.0 * 1001 / 24000

// fileRuntime is an entry's runtime as the server read it off the file, 0
// when it never probed the file: a runtime from a provider or an nfo (TMDB
// gives every episode of a sitcom its 22 minutes) is not the file's, and two
// of them agreeing says nothing about the files.
func fileRuntime(it *embyfin.Item) int64 {
	if !it.Probed() {
		return 0
	}
	if best := it.BestSource(); best != nil && best.RunTimeTicks > 0 {
		return best.RunTimeTicks
	}

	return it.RunTimeTicks
}

// minCopySize is the least a file can be and its size be a sign: a smaller
// file is a placeholder or a link, which come out the same size whatever
// they stand for.
const minCopySize = 1 << 20

// copySign is what two entries' files say about being one content, "" when
// they say nothing: the same runtime to the second, one the other's at PAL
// speed to the second, or the same size to the byte - each shared by no
// entry of the season outside title (the entries of the pair's title, which
// sharing it are more copies), where every episode of a show cut to a fixed
// length runs alike: a runtime within sharedBand of the pair's is shared, as
// such a show runs its episodes a second or two apart. A runtime is a sign only when the season shows its
// lengths vary - two or more entries outside the title running lengths that
// differ (lengthsVary) - and never for a placeholder title (TBA, "Episode
// 5"): an airing show cut to its slot runs every episode to the frame, and
// its episodes not yet named share the title. A size merely close is no sign: an
// old TV encode was made to a fixed size (175 or 350 MB), and every episode
// of a season comes out within a few kilobytes of the next; nor is the size
// of a file too small to be video, or a .strm link's. weak says why a match
// is no sign, "" when there was none to explain.
func copySign(a, b *embyfin.Item, season []embyfin.Item, title map[string]bool) (sign, weak string) {
	link := isLink(a) || isLink(b)
	others := func(alike func(*embyfin.Item) bool) []string {
		var out []string
		for i := range season {
			if c := &season[i]; c.ID != a.ID && c.ID != b.ID && !title[c.ID] && alike(c) {
				out = append(out, "E"+numberText(c.IndexNumber))
			}
		}
		return out
	}
	within := func(x, y, by int64) bool { return max(x, y)-min(x, y) <= by }
	atPAL := func(ticks int64) int64 { return int64(math.Round(float64(ticks) * palSpeedup)) }
	ra, rb := fileRuntime(a), fileRuntime(b)
	if ra > 0 && rb > 0 && !link {
		short, long := min(ra, rb), max(ra, rb)
		same, pal := within(short, long, ticksPerSecond), within(long, atPAL(short), ticksPerSecond)
		// another episode runs it too when it is near it, not the same to
		// the second: a show cut to one slot runs its episodes a second or
		// two apart, and the pair's own second says nothing against that
		near := sharedBand(short)
		var shared []string
		switch {
		case same:
			shared = others(func(c *embyfin.Item) bool {
				rc := fileRuntime(c)
				return rc > 0 && within(rc, short, near)
			})
		case pal:
			// another entry at either length: at PAL speed of the shorter
			// (the longer's), or the longer at PAL speed of it (the
			// shorter's) - a season from two sources, each cut to a length
			shared = others(func(c *embyfin.Item) bool {
				rc := fileRuntime(c)
				return rc > 0 && (within(rc, atPAL(short), near) || within(long, atPAL(rc), near))
			})
		}
		match := "the same runtime to the second"
		if !same {
			match = "one runs the other's length at PAL speed, to the second"
		}
		switch {
		case !same && !pal:
		case len(shared) > 0:
			weak = fmt.Sprintf("%s, but %s of the season runs within a few seconds of it too, so no sign alone", match, strings.Join(shared, ", "))
		case placeholderTitle(a.Name):
			weak = fmt.Sprintf("%s, but %q is a placeholder title, which episodes not yet named share whatever they hold, and a show cut to one slot runs each to the frame, so no sign alone", match, a.Name)
		case !lengthsVary(season, a, b, title):
			weak = match + ", but fewer than two episodes of the season under other titles run lengths of their own that differ by more than a few seconds, read off their files, so nothing says the show is not cut to one length, and no sign alone"
		case same:
			return match, ""
		default:
			return match + ": the same content from a PAL source", ""
		}
	}
	sa, sb := pathQuality(a).Size, pathQuality(b).Size
	if sa <= 0 || sb <= 0 {
		// a file the server has listed but not read has no size, nor may a
		// link or a disc's folder ever have one
		for _, e := range []*embyfin.Item{a, b} {
			if pathQuality(e).Size <= 0 {
				return "", cmp.Or(weak, fmt.Sprintf("E%s's size is not known to the server, so the sizes say nothing", numberText(e.IndexNumber)))
			}
		}
	}
	if sa != sb {
		return "", weak
	}
	if link || sa < minCopySize {
		return "", cmp.Or(weak, "the same size to the byte, but files too small to be video (placeholders or links), so no sign")
	}
	if shared := others(func(c *embyfin.Item) bool { return pathQuality(c).Size == sa }); len(shared) > 0 {
		return "", cmp.Or(weak, fmt.Sprintf("the same size to the byte, which %s of the season is too, so no sign alone", strings.Join(shared, ", ")))
	}

	return "the same size to the byte", ""
}

// placeholderTitle says whether a title is one a provider gives an episode it
// has no name for yet - TBA, TBD, "Episode 5", "Episode Five", a number
// alone, "Untitled" - which a run of episodes shares whatever each holds.
func placeholderTitle(title string) bool {
	return placeholderTitles.MatchString(strings.TrimSpace(title))
}

var placeholderTitles = regexp.MustCompile(`(?i)^(?:tba|tbd|tbc|to be (?:announced|determined|confirmed)|untitled(?: episode)?|unknown|n/?a|episode|` +
	`(?:(?:episode|episodio|épisode|folge|aflevering|afl\.|chapter|capítulo|ep\.?|e)\s*)?#?\s*\d+(?:\.\d+)?|` +
	`(?:episode|chapter)\s+` + spelledNumber + `(?:[\s-]+` + spelledNumber + `)*|` +
	`第\s*\d+\s*話)$`)

// spelledNumber is a number's word, one to ninety, and hundred.
const spelledNumber = `(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety|hundred)`

// sharedBand is how near another episode's runtime is to a length to run it
// too: five seconds, or half a percent of the length when that is more.
func sharedBand(length int64) int64 {
	return max(5*ticksPerSecond, length/200)
}

// lengthsVary says whether the season shows its episodes run lengths of
// their own: two or more entries outside title (the pair's own title) whose
// runtimes, read off their files, differ by more than sharedBand of the
// longer. An airing anime cut to its slot runs every episode within a second
// or two of it, and one double episode beside them says nothing of the rest.
func lengthsVary(season []embyfin.Item, a, b *embyfin.Item, title map[string]bool) bool {
	var shortest, longest int64
	for i := range season {
		c := &season[i]
		rc := fileRuntime(c)
		if c.ID == a.ID || c.ID == b.ID || title[c.ID] || rc <= 0 {
			continue
		}
		if shortest == 0 || rc < shortest {
			shortest = rc
		}
		longest = max(longest, rc)
	}

	return shortest > 0 && longest-shortest > sharedBand(longest)
}

// isLink says whether an entry's file is a .strm link to a stream, whose
// size and runtime are the link's or the metadata's, never the content's.
func isLink(it *embyfin.Item) bool {
	return strings.EqualFold(path.Ext(it.Path), ".strm")
}

// knownRuntime is an entry's runtime to compare with another's: the file's
// when the server read it, else the one the server holds, 0 when neither is
// known.
func knownRuntime(it *embyfin.Item) int64 {
	return max(cmp.Or(fileRuntime(it), it.RunTimeTicks), 0)
}

// minKnown is the shorter of two runtimes, 0 being unknown.
func minKnown(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return max(a, b)
	}

	return min(a, b)
}

// runtimesAlike says whether entries of one title, the shortest and the
// longest of them given, run close enough to be one episode: within 15% of
// the longer, or a runtime unknown, which leaves them a lead rather than
// keeping them apart. Further apart they are a far_apart group: a copy cut
// short, a file holding two episodes, an extended cut, or two episodes a
// season named alike.
func runtimesAlike(shortest, longest int64) bool {
	if shortest <= 0 || longest <= 0 {
		return true
	}

	return float64(longest-shortest) <= 0.15*float64(longest)
}

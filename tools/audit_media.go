package tools

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/lib/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The audits over what is on disk rather than what the metadata says: the
// quality of the files, the episodes a season is missing between the ones it
// has, and what nobody has watched.

// legacyCodecs are the video codecs worth replacing: what DVD rips and early
// downloads were made with, by the names ffprobe gives them.
var legacyCodecs = []string{"mpeg1video", "mpeg2video", "mpeg4", "msmpeg4v1", "msmpeg4v2", "msmpeg4v3", "wmv1", "wmv2", "wmv3", "vc1", "h263", "flv1", "vp6", "vp6f", "theora", "rv30", "rv40", "cinepak", "indeo5"}

// qualityIn is audit_quality's input.
type qualityIn struct {
	Library        string `json:"library,omitempty"          jsonschema:"restrict to one library by name or id"`
	Types          string `json:"types,omitempty"            jsonschema:"comma-separated item types; default Movie,Episode"`
	MinHeight      int    `json:"min_height,omitempty"       jsonschema:"flag video shorter than this many lines, default 720 (so 480p and 576p rips)"`
	MinBitrateKbps int    `json:"min_bitrate_kbps,omitempty" jsonschema:"also flag video below this bitrate; off unless given"`
	Codecs         *bool  `json:"legacy_codecs,omitempty"    jsonschema:"flag legacy video codecs (MPEG-2, MPEG-4 part 2 such as XviD and DivX, WMV, VC-1, RealVideo...); default true"`
	Limit          int    `json:"limit,omitempty"            jsonschema:"maximum findings to return, default 100"`
}

// videoOf is a file's primary video stream, or nil.
func videoOf(src *embyfin.MediaSource) *embyfin.MediaStream {
	for i := range src.MediaStreams {
		if src.MediaStreams[i].Type == "Video" {
			return &src.MediaStreams[i]
		}
	}

	return nil
}

// checkQuality judges an item by its best file: a 4K copy beside a 480p one is
// not a worklist entry. It returns the finding and the best height, which
// orders the worklist worst first.
func checkQuality(it *embyfin.Item, in qualityIn) (detail string, height int, bad bool) {
	var best *embyfin.MediaStream
	bitrate := int64(0)
	for i := range it.MediaSources {
		v := videoOf(&it.MediaSources[i])
		if v == nil {
			continue
		}
		if best == nil || v.Height > best.Height {
			best, bitrate = v, v.BitRate
			if bitrate == 0 {
				bitrate = it.MediaSources[i].Bitrate
			}
		}
	}
	if best == nil {
		return "", 0, false // no video to judge: audio, or never probed
	}

	var problems []string
	if in.MinHeight > 0 && best.Height > 0 && best.Height < in.MinHeight {
		problems = append(problems, fmt.Sprintf("%dp, below %dp", best.Height, in.MinHeight))
	}
	if (in.Codecs == nil || *in.Codecs) && slices.Contains(legacyCodecs, strings.ToLower(best.Codec)) {
		problems = append(problems, "legacy codec "+best.Codec)
	}
	if in.MinBitrateKbps > 0 && bitrate > 0 && bitrate/1000 < int64(in.MinBitrateKbps) {
		problems = append(problems, fmt.Sprintf("%d kbps, below %d", bitrate/1000, in.MinBitrateKbps))
	}
	if len(problems) == 0 {
		return "", best.Height, false
	}

	return fmt.Sprintf("%s %dx%d: %s", best.Codec, best.Width, best.Height, strings.Join(problems, "; ")), best.Height, true
}

// qualityDefaults fills audit_quality's defaults: 720 lines, legacy codecs
// flagged, no bitrate floor.
func qualityDefaults(in qualityIn) qualityIn {
	if in.MinHeight <= 0 {
		in.MinHeight = 720
	}
	if in.Limit <= 0 {
		in.Limit = 100
	}

	return in
}

func auditQuality(ctx context.Context, client *embyfin.Client, in qualityIn) (auditOut, error) {
	in = qualityDefaults(in)
	opts, err := sweepOptions(ctx, client, in.Library, in.Types, "Movie,Episode", "Path,ProductionYear,MediaSources")
	if err != nil {
		return auditOut{}, err
	}

	type scored struct {
		finding auditFinding
		height  int
	}
	var findings []scored
	out := auditOut{Findings: []auditFinding{}}
	if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			out.Scanned++
			detail, height, bad := checkQuality(it, in)
			if !bad {
				continue
			}
			name := it.Name
			if it.SeriesName != "" {
				name = fmt.Sprintf("%s S%02dE%02d %s", it.SeriesName, it.ParentIndexNumber, it.IndexNumber, it.Name)
			}
			findings = append(findings, scored{height: height, finding: auditFinding{ID: it.ID, Name: name, Year: it.ProductionYear, Path: it.Path, Detail: detail}})
		}
		return true
	}); err != nil {
		return auditOut{}, err
	}

	// the lowest resolution first, so a capped worklist starts with the worst
	slices.SortStableFunc(findings, func(a, b scored) int {
		if c := cmp.Compare(a.height, b.height); c != 0 {
			return c
		}
		return strings.Compare(a.finding.Name, b.finding.Name)
	})
	out.Found = len(findings)
	for _, f := range findings[:min(len(findings), in.Limit)] {
		out.Findings = append(out.Findings, f.finding)
	}

	return out, nil
}

// seasonGaps lists what a series' files leave out: the episode numbers
// missing between the lowest and highest a season holds, and the seasons
// missing between the lowest and highest the series holds. Specials (season
// 0) have no order to have gaps in. Nothing past the last episode on disk is
// knowable from the files.
func seasonGaps(episodes map[int][]int) []string {
	seasons := make([]int, 0, len(episodes))
	for s := range episodes {
		if s > 0 {
			seasons = append(seasons, s)
		}
	}
	slices.Sort(seasons)

	var gaps []string
	for i, s := range seasons {
		if i > 0 {
			for missing := seasons[i-1] + 1; missing < s; missing++ {
				gaps = append(gaps, fmt.Sprintf("season %d", missing))
			}
		}
		eps := slices.Clone(episodes[s])
		slices.Sort(eps)
		eps = slices.Compact(eps)
		for j := 1; j < len(eps); j++ {
			for missing := eps[j-1] + 1; missing < eps[j]; missing++ {
				gaps = append(gaps, fmt.Sprintf("S%02dE%02d", s, missing))
			}
		}
	}

	return gaps
}

type episodesIn struct {
	Library string `json:"library,omitempty" jsonschema:"restrict to one library by name or id"`
	Limit   int    `json:"limit,omitempty"   jsonschema:"maximum series to return, default 100"`
}

func auditMissingEpisodes(ctx context.Context, client *embyfin.Client, in episodesIn) (auditOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	opts, err := sweepOptions(ctx, client, in.Library, "", "Episode", "Path")
	if err != nil {
		return auditOut{}, err
	}

	type series struct {
		name     string
		onDisk   map[int][]int
		provider []string // episodes the server lists without a file
	}
	bySeries := map[string]*series{}
	out := auditOut{Findings: []auditFinding{}}
	if err := client.SearchAll(ctx, opts, func(items []embyfin.Item) bool {
		for i := range items {
			it := &items[i]
			if it.SeriesID == "" {
				continue
			}
			s := bySeries[it.SeriesID]
			if s == nil {
				s = &series{name: it.SeriesName, onDisk: map[int][]int{}}
				bySeries[it.SeriesID] = s
			}
			if it.Path == "" || it.IsMissing {
				s.provider = append(s.provider, fmt.Sprintf("S%02dE%02d", it.ParentIndexNumber, it.IndexNumber))
				continue
			}
			out.Scanned++
			if it.IndexNumber > 0 {
				s.onDisk[it.ParentIndexNumber] = append(s.onDisk[it.ParentIndexNumber], it.IndexNumber)
			}
		}
		return true
	}); err != nil {
		return auditOut{}, err
	}

	ids := make([]string, 0, len(bySeries))
	for id := range bySeries {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return strings.Compare(bySeries[a].name, bySeries[b].name) })
	for _, id := range ids {
		s := bySeries[id]
		gaps := seasonGaps(s.onDisk)
		var parts []string
		if len(gaps) > 0 {
			parts = append(parts, "missing between the episodes on disk: "+strings.Join(gaps, ", "))
		}
		if len(s.provider) > 0 {
			slices.Sort(s.provider)
			parts = append(parts, "listed by the metadata provider without a file: "+strings.Join(s.provider, ", "))
		}
		if len(parts) == 0 {
			continue
		}
		out.Found++
		if len(out.Findings) < limit {
			out.Findings = append(out.Findings, auditFinding{ID: id, Name: s.name, Detail: strings.Join(parts, "; ")})
		}
	}

	return out, nil
}

func registerMediaAudits(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name:        "audit_quality",
		Description: "Find the films and episodes worth replacing with a better copy: video below a resolution (default 720 lines, so 480p and 576p rips), in a legacy codec (MPEG-2, XviD and DivX, WMV, VC-1...), or below a bitrate when one is given. An item is judged by its best file, so a 4K version beside a DVD rip is not reported. Lowest resolution first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in qualityIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := auditQuality(ctx, client, in)
		return nil, out, err
	})

	add(r, readTool, &mcp.Tool{
		Name:        "audit_missing_episodes",
		Description: "Find the series with episodes missing: the episode numbers a season skips between the ones on disk (E01 and E03 but no E02), whole seasons skipped between the ones on disk, and, when the server records them, the episodes its metadata provider lists that have no file (stock Jellyfin needs the TheTVDB plugin for those, and Emby 4.10 does not record them). show_missing lists one series' provider episodes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in episodesIn) (*mcp.CallToolResult, auditOut, error) {
		out, err := auditMissingEpisodes(ctx, client, in)
		return nil, out, err
	})

	type unwatchedIn struct {
		Library   string `json:"library,omitempty"    jsonschema:"restrict to one library by name or id"`
		Types     string `json:"types,omitempty"      jsonschema:"Movie, Series or both, comma-separated; default Movie. A series counts as watched when anyone has watched any of its episodes"`
		AddedDays int    `json:"added_days,omitempty" jsonschema:"only items added at least this many days ago, so what just arrived is left out"`
		Limit     int    `json:"limit,omitempty"      jsonschema:"maximum findings to return, default 100"`
	}
	type unwatchedOut struct {
		auditOut
		Users []string `json:"users" jsonschema:"whose watch state was read: every account on the server, each in its own view, so what an account cannot see it has not watched"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "audit_unwatched",
		Description: "Find what nobody has watched: the films (or series) no account on the server has played, oldest additions first, optionally only those added more than some days ago. A copy of a film watched anywhere on the server counts for every copy. What to archive or delete to free space, or what to recommend.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in unwatchedIn) (*mcp.CallToolResult, unwatchedOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 100
		}
		types := map[string]bool{}
		for t := range strings.SplitSeq(cmp.Or(in.Types, typeMovie), ",") {
			switch t = strings.TrimSpace(t); t {
			case typeMovie, "Series":
				types[t] = true
			default:
				return nil, unwatchedOut{}, fmt.Errorf("types must be Movie, Series or both, not %q", t)
			}
		}
		folder, err := resolveLibrary(ctx, client, in.Library)
		if err != nil {
			return nil, unwatchedOut{}, err
		}
		parent := ""
		if folder != nil {
			parent = folder.ItemID
		}
		users, err := client.Users(ctx)
		if err != nil {
			return nil, unwatchedOut{}, err
		}

		// what anyone has watched, by title (a copy watched in another library
		// counts for this one's) and across the server: films played, and the
		// series of any episode played. Not scoped to the library: Jellyfin
		// lists what a user played in a library they cannot see when the
		// library is named as the parent, where Emby leaves it out
		watched := titles{}
		series := map[string]bool{}
		out := unwatchedOut{
			Users:    []string{},
			Findings: []auditFinding{},
		}
		playedTypes := []string{}
		if types[typeMovie] {
			playedTypes = append(playedTypes, typeMovie)
		}
		if types["Series"] {
			playedTypes = append(playedTypes, "Episode")
		}
		for _, u := range users {
			out.Users = append(out.Users, u.Name)
			if err := client.SearchAll(ctx, embyfin.SearchOptions{
				IncludeItemTypes: strings.Join(playedTypes, ","), Filters: "IsPlayed", UserID: u.ID, EnableUserData: true, Fields: "Path,ProviderIds",
			}, func(items []embyfin.Item) bool {
				for i := range items {
					if items[i].Type == "Episode" {
						series[items[i].SeriesID] = true
						continue
					}
					watched.add(&items[i])
				}
				return true
			}); err != nil {
				return nil, unwatchedOut{}, err
			}
		}
		delete(series, "")
		for ids := range slices.Chunk(slices.Collect(maps.Keys(series)), 100) {
			if err := client.SearchAll(ctx, embyfin.SearchOptions{IDs: strings.Join(ids, ","), IncludeItemTypes: "Series", Fields: "Path,ProviderIds"}, func(items []embyfin.Item) bool {
				for i := range items {
					watched.add(&items[i])
				}
				return true
			}); err != nil {
				return nil, unwatchedOut{}, err
			}
		}

		kinds := make([]string, 0, len(types))
		for t := range types {
			kinds = append(kinds, t)
		}
		var findings []auditFinding
		if err := client.SearchAll(ctx, embyfin.SearchOptions{IncludeItemTypes: strings.Join(kinds, ","), ParentID: parent, Fields: embyfin.FieldsLean}, func(items []embyfin.Item) bool {
			for i := range items {
				it := &items[i]
				out.Scanned++
				if watched.has(it) {
					continue
				}
				if in.AddedDays > 0 && afterCutoff(it.DateCreated, daysCutoff(in.AddedDays)) {
					continue
				}
				findings = append(findings, auditFinding{ID: it.ID, Name: it.Name, Year: it.ProductionYear, Path: it.Path, Detail: "never watched, added " + dateOf(it.DateCreated)})
			}
			return true
		}); err != nil {
			return nil, unwatchedOut{}, err
		}

		slices.SortStableFunc(findings, func(a, b auditFinding) int {
			if c := strings.Compare(a.Detail, b.Detail); c != 0 {
				return c
			}
			return strings.Compare(a.Name, b.Name)
		})
		out.Found = len(findings)
		out.Findings = append(out.Findings, findings[:min(len(findings), limit)]...)

		return nil, out, nil
	})
}

// dateOf is the date part of a server timestamp.
func dateOf(stamp string) string {
	if d, _, ok := strings.Cut(stamp, "T"); ok {
		return d
	}
	if stamp == "" {
		return "on an unknown date"
	}

	return stamp
}

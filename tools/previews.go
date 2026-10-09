package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Preview thumbnails: the frames a player shows over the seek bar while the
// viewer scrubs, which Emby keeps in one BIF file a video (see
// embyfin.Preview for the file and how the server answers it).
//
// Emby makes them from a scheduled task. On 4.10.1 the task takes the videos
// it has never made any for and passes over the rest, so a file deleted or
// damaged since - by hand, by a disk, by clearing the metadata folder to
// make room - stays that way, and nothing on the server says so: the task
// ends in a second with nothing to do. (A 4.11 beta was seen passing them
// over too and, days later, making them again from the task; what changed
// there is not known.) The audit here asks for each video's file as it is
// on disk now, and the tool beside it makes again the ones the audit finds,
// one at a time, for the ones the task does not.

const (
	// previewDefaultTypes are the videos read when none are named: what a
	// library of films or shows holds. A trailer and an extra have none
	previewDefaultTypes = typeMovie + "," + typeEpisode
	// defaultPreviewChecks is how many videos one audit call asks about
	defaultPreviewChecks = 5000
	// previewLookAhead is how many videos the regeneration looks at before
	// it makes any again: a library mostly sound is read through quickly
	previewLookAhead = 200
	// how many videos one regeneration call makes again, and for how long
	defaultRegenerate        = 5
	mostRegenerate           = 100
	defaultRegenerateMinutes = 4
	mostRegenerateMinutes    = 60
	// previewPatience is how long one video is waited for once asked
	previewPatience = 10 * time.Minute
)

// previewKinds are the kinds of video Emby keeps preview thumbnails for.
var previewKinds = []string{typeMovie, typeEpisode, "Video", "MusicVideo"}

// The problems the audit names, in the order a count lists them.
const (
	previewMissing = "missing"
	previewDamaged = "damaged"
	previewLength  = "length"
)

type previewsIn struct {
	Library   string   `json:"library,omitempty"    jsonschema:"one library by name or id; default every library"`
	IDs       []string `json:"ids,omitempty"        jsonschema:"only these videos, by id: a handful checked without sweeping a library"`
	Types     string   `json:"types,omitempty"      jsonschema:"comma-separated: Movie, Episode, Video, MusicVideo; default Movie,Episode"`
	Limit     int      `json:"limit,omitempty"      jsonschema:"maximum findings, default 50"`
	MaxChecks int      `json:"max_checks,omitempty" jsonschema:"videos to ask the server about in this call, default 5000: one small request for a video with no file, two for one with"`
	Offset    int      `json:"offset,omitempty"     jsonschema:"where to go on from: a previous call's next_offset"`
}

type previewFinding struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Year    int    `json:"year,omitempty"`
	Path    string `json:"path,omitempty"`
	Library string `json:"library"`
	Problem string `json:"problem"        jsonschema:"missing: the server holds no thumbnails for the video. damaged: it holds a file a player cannot read. length: it holds a sound file that does not fit the video"`
	Detail  string `json:"detail"`
}

type previewsOffRow struct {
	Library string `json:"library" jsonschema:"empty for videos in no library's folders"`
	Videos  int    `json:"videos"`
	Why     string `json:"why"`
}

type previewsOut struct {
	Scanned    int              `json:"items_scanned"           jsonschema:"videos this call asked the server about"`
	Found      int              `json:"total_findings"          jsonschema:"among the videos this call asked about; the rest wait for next_offset"`
	ByProblem  map[string]int   `json:"by_problem"`
	Unjudged   int              `json:"length_not_judged"       jsonschema:"videos with a sound file whose length could not be held against the video's, the server holding no runtime for it. None of them is known to fit"`
	Findings   []previewFinding `json:"findings"                jsonschema:"in the order of the files' paths; capped at limit"`
	Off        []previewsOffRow `json:"libraries_off,omitempty" jsonschema:"the libraries read that make no preview thumbnails, with how many videos each holds: none of those was asked about, and none is missing anything"`
	NextOffset int              `json:"next_offset,omitempty"   jsonschema:"pass back as offset to go on; absent when the sweep finished"`
	Note       string           `json:"note,omitempty"          jsonschema:"set when the library was seen to change while this call read its videos: videos added or removed meanwhile may be missing, or listed though gone. It also says when the read stopped short, the library changing too much to follow, or whether it changed could not be checked"`
}

type regenerateIn struct {
	IDs       []string `json:"ids,omitempty"       jsonschema:"the videos to make thumbnails for, by id (audit_previews lists them)"`
	Library   string   `json:"library,omitempty"   jsonschema:"instead of ids: one library by name or id, whose next videos with missing or damaged thumbnails are taken in the order of their paths"`
	Types     string   `json:"types,omitempty"     jsonschema:"comma-separated: Movie, Episode, Video, MusicVideo; default Movie,Episode"`
	Limit     int      `json:"limit,omitempty"     jsonschema:"most videos to make thumbnails for in this call, default 5, at most 100"`
	Minutes   int      `json:"minutes,omitempty"   jsonschema:"stop starting new videos after this long, default 4, at most 60; a video already asked for is still waited on, ten minutes at most"`
	Offset    int      `json:"offset,omitempty"    jsonschema:"with library, where to go on from: a previous call's next_offset"`
	Unmatched bool     `json:"unmatched,omitempty" jsonschema:"also take a video that holds no provider id in a library that fetches metadata, which the refresh may match to a title"`
	Length    bool     `json:"length,omitempty"    jsonschema:"with library, also take a video whose thumbnails are sound but not the length of the video: they are made again from the file there now, which changes nothing when it is the runtime the server holds that is wrong. A video named in ids is taken either way"`
}

type regenerateRow struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Path        string   `json:"path,omitempty"`
	Library     string   `json:"library,omitempty"`
	Was         string   `json:"was"                    jsonschema:"what audit_previews says of the video's thumbnails before: missing, damaged or length with its detail, or sound"`
	Result      string   `json:"result"                 jsonschema:"made: a sound file is there now. not_made: Emby ran the refresh and left none. asked: the refresh had not run when the wait ended, and runs when its turn comes. skipped: not asked for, and why. failed: a request failed, and what"`
	Detail      string   `json:"detail,omitempty"`
	Thumbnails  int      `json:"thumbnails,omitempty"   jsonschema:"how many the file holds now"`
	TookS       int      `json:"took_s,omitempty"       jsonschema:"seconds from asking to the refresh having run"`
	AlsoChanged []string `json:"also_changed,omitempty" jsonschema:"the item's fields, as the server names them, that read differently after the refresh than before it: what the refresh did besides the thumbnails. Nothing puts them back"`
}

type regenerateOut struct {
	Checked    int             `json:"videos_checked"        jsonschema:"videos whose thumbnails were read to see whether they needed making"`
	Made       int             `json:"made"`
	NotMade    int             `json:"not_made"`
	Asked      int             `json:"asked"`
	Skipped    int             `json:"skipped"`
	Failed     int             `json:"failed"`
	Videos     []regenerateRow `json:"videos"                jsonschema:"one row for each video asked for or passed over with a reason, in order; a video of a library sweep whose thumbnails were sound has no row"`
	Stopped    string          `json:"stopped,omitempty"     jsonschema:"why the call ended before every video was looked at: the limit, the minutes, a refresh that had not run, or a request that failed"`
	NotReached []string        `json:"not_reached,omitempty" jsonschema:"with ids: the ids not looked at when the call stopped"`
	NextOffset int             `json:"next_offset,omitempty" jsonschema:"with library: pass back as offset to go on from the first video not looked at; absent when the library was read to its end"`
	Off        *previewsOffRow `json:"library_off,omitempty" jsonschema:"with library: set when the library makes no preview thumbnails, so nothing was asked for"`
	Note       string          `json:"note,omitempty"`
}

func registerPreviewTools(r *registry) {
	client := r.client

	add(r, readTool, &mcp.Tool{
		Name: "audit_previews",
		Description: "Find videos whose preview thumbnails are missing or damaged: the frames a player shows over the seek bar while scrubbing, which Emby keeps in one BIF file a video. " +
			"Emby makes them from a scheduled task, which can pass over a video it made them for before (seen on 4.10.1: a file deleted since stayed missing however often the task ran), and nothing on the server says one is missing or damaged. This asks the server for each video's file as it is on disk now and reads its header and the end of its index, 72 bytes: missing is no file; damaged is a file a player cannot read - empty, not a thumbnail file, or cut off; length is a sound file whose frames stop more than a twentieth of the video's length, and two minutes, short of its end or past it, as one made from another file does, or one on a video whose runtime the server has wrong. A sound file on a video the server holds no runtime for is counted in length_not_judged. " +
			"Only a library that makes thumbnails is read - extraction on and an interval set, both, in its options - and the others are listed in libraries_off with how many videos each holds, none of them missing anything. Films and episodes by default; a trailer and an extra have none and are not read. " +
			"Paged, in the order of the files' paths: max_checks videos a call, and next_offset to go on; ids checks a handful. item_previews_regenerate makes them again. Emby only so far: Jellyfin keeps trickplay tiles, which this does not read.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in previewsIn) (*mcp.CallToolResult, previewsOut, error) {
		out, err := auditPreviews(ctx, client, in)

		return nil, out, err
	})

	add(r, writeTool, &mcp.Tool{
		Name: "item_previews_regenerate",
		Description: "Make a video's preview thumbnails again on Emby, for the videos audit_previews finds: ids names them, or library takes that library's next videos whose thumbnails are missing or damaged, and with length true the ones that are sound but the wrong length for the video. A video whose thumbnails are sound and fit it is left alone. " +
			"One video at a time, each waited for before the next is asked: Emby reads the whole video to make them and runs the refreshes it is asked for one after another, so nothing queues behind this call. It stops at limit videos or after minutes, and with library answers next_offset to go on from. " +
			"Emby's own scheduled task is the way to make many, and this is for the ones it passes over. Emby has no request for the thumbnails alone, so this sends the narrowest refresh that makes them: full for the item's metadata, replacing none of it, and fetching no images. That refresh also reads the file's streams again, fills a field the item has empty from its nfo or its metadata providers, and in a library that saves nfo files writes the item's nfo again, the same when nothing changed. also_changed names every field of the item that reads differently after; nothing puts them back. A video holding no provider id in a library that fetches metadata is skipped unless unmatched is true, because that refresh may match it to a title. " +
			"Each row says made, with how many thumbnails, and says so when they still do not fit the runtime the server holds; not_made, when Emby ran the refresh and left no sound file, as for a video it cannot read; asked, when the refresh had not run after ten minutes - it is made when its turn comes, and the call stops there rather than queue more behind it; skipped, with why; or failed, with the error, where the call also stops. Emby only so far.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in regenerateIn) (*mcp.CallToolResult, regenerateOut, error) {
		out, err := regeneratePreviews(ctx, client, in, previewRun{
			make: func(ctx context.Context, id string) (embyfin.Preview, bool, error) {
				return client.RegeneratePreview(ctx, id, previewPatience)
			},
			now: time.Now,
		})

		return nil, out, err
	})
}

// previewRun is what a regeneration is run with: how a video's thumbnails
// are made again - what the server holds after, and whether the refresh was
// seen to run - and the clock its minutes are counted on. The tests give it
// a server that answers at once and a clock that jumps.
type previewRun struct {
	make func(ctx context.Context, itemID string) (after embyfin.Preview, ran bool, err error)
	now  func() time.Time
}

// previewVideo is one video and the library whose folders hold it, nil for
// a video in none.
type previewVideo struct {
	item    embyfin.Item
	library *embyfin.VirtualFolder
}

// makes reports whether the video's library makes preview thumbnails.
func (v *previewVideo) makes() bool { return v.library != nil && v.library.PreviewEveryS > 0 }

// libraryName is the video's library by name, empty for a video in none.
func (v *previewVideo) libraryName() string {
	if v.library == nil {
		return ""
	}

	return v.library.Name
}

// off says why a video's thumbnails are not read, for one whose library
// makes none.
func (v *previewVideo) off() string {
	if v.library == nil {
		return "in no library's folders, so no library's options say whether Emby makes preview thumbnails for them"
	}

	return "the library's options make no preview thumbnails (extraction off, or no interval set), so Emby keeps none for its videos"
}

// previewTypes reads the types a call names: some of previewKinds, films
// and episodes when none are named.
func previewTypes(types string) ([]string, error) {
	var kinds []string
	for t := range strings.SplitSeq(cmp.Or(strings.TrimSpace(types), previewDefaultTypes), ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		i := slices.IndexFunc(previewKinds, func(k string) bool { return strings.EqualFold(k, t) })
		if i < 0 {
			return nil, fmt.Errorf("types must be among %s, not %q: Emby keeps preview thumbnails for videos alone", strings.Join(previewKinds, ", "), t)
		}
		if !slices.Contains(kinds, previewKinds[i]) {
			kinds = append(kinds, previewKinds[i])
		}
	}

	return kinds, nil
}

// previewVideos reads the videos a call is about - the ids', or a library's,
// or every library's - each with the library that holds it, in the order of
// their paths: the order a folder is read in, and the same on every call.
func previewVideos(ctx context.Context, client *embyfin.Client, library string, ids []string, types string) (videos []previewVideo, note string, err error) {
	if client.Backend() != embyfin.Emby {
		return nil, "", embyfin.ErrNoPreviewFiles
	}
	kinds, err := previewTypes(types)
	if err != nil {
		return nil, "", err
	}
	folders, err := client.VirtualFolders(ctx)
	if err != nil {
		return nil, "", err
	}
	opts := embyfin.SearchOptions{IncludeItemTypes: strings.Join(kinds, ","), Fields: "Path,ProductionYear,ProviderIds"}
	if ids = nonEmpty(ids); len(ids) > 0 {
		if library != "" {
			return nil, "", errors.New("give library or ids, not both: a video is already in one library")
		}
		// read first: an id the server cannot use as a filter is not a
		// narrower sweep, it is the whole library or nothing (see checkIDs)
		if err := checkIDs(ctx, client, ids, kinds, "videos"); err != nil {
			return nil, "", err
		}
		opts.IDs = strings.Join(ids, ",")
	} else {
		folder, libErr := client.ResolveLibrary(ctx, library)
		if libErr != nil {
			return nil, "", libErr
		}
		if folder != nil {
			opts.ParentID = folder.ItemID
		}
	}

	read, err := client.ReadAll(ctx, opts, embyfin.ToAnswer, func(items []embyfin.Item) bool {
		for i := range items {
			// a featurette Emby took for an episode is an extra, and a
			// record of an episode with no file has no video to scrub
			if extraEpisode(&items[i]) || !items[i].HasFile() {
				continue
			}
			videos = append(videos, previewVideo{item: items[i], library: embyfin.FolderOf(folders, items[i].Path)})
		}

		return true
	})
	if err != nil {
		return nil, "", err
	}
	slices.SortFunc(videos, func(a, b previewVideo) int {
		return cmp.Or(strings.Compare(a.item.Path, b.item.Path), strings.Compare(a.item.ID, b.item.ID))
	})

	return videos, read.Changed(), nil
}

// previewProblem judges what the server holds of a video's thumbnails: the
// problem's name and what to say of it, both empty for a sound file that
// fits the video. judged is false for a sound file on a video the server
// holds no runtime for, whose length cannot be held against anything.
func previewProblem(p *embyfin.Preview, it *embyfin.Item) (problem, detail string, judged bool) {
	switch {
	case p.Missing():
		return previewMissing, "the server holds no preview thumbnails for it", true
	case !p.Sound():
		return previewDamaged, p.Damage, true
	case it.RunTimeTicks <= 0:
		return "", "", false
	}
	runtime := time.Duration(it.RunTimeTicks) * 100
	apart := p.Covers() - runtime
	if apart < 0 {
		apart = -apart
	}
	if apart <= max(2*time.Minute, runtime/20) {
		return "", "", true
	}

	// which of the two is wrong cannot be told from here: a runtime the
	// server read wrong makes sound thumbnails look too long (seen on a real
	// library: an hour of thumbnails on a film the server times at 8 s)
	why := "they stop short of its end: cut short, or made from another file than the one there now"
	if p.Covers() > runtime {
		why = "they run past its end: made from another file than the one there now, or the runtime the server holds is wrong"
	}

	return previewLength, fmt.Sprintf("%d thumbnails %s apart reach %s, and the server times the video at %s: %s",
		p.Thumbnails, runtimeSaid(int(p.Interval.Seconds())), runtimeSaid(int(p.Covers().Seconds())), runtimeSaid(int(runtime.Seconds())), why), true
}

// readPreviews asks the server about each video's thumbnails, a few at
// once. A request that fails ends the read and names the video.
func readPreviews(ctx context.Context, client *embyfin.Client, videos []previewVideo) ([]embyfin.Preview, error) {
	previews := make([]embyfin.Preview, len(videos))
	err := eachAtOnce(ctx, len(videos), func(ctx context.Context, i int) error {
		p, err := client.PreviewOf(ctx, videos[i].item.ID)
		if err != nil {
			return fmt.Errorf("reading the preview thumbnails of %s (%s): %w", episodeOrItemName(&videos[i].item), videos[i].item.ID, err)
		}
		previews[i] = p

		return nil
	})

	return previews, err
}

// previewsOff counts the videos whose library makes no thumbnails, by
// library, and answers the ones whose library does.
func previewsOff(videos []previewVideo) (makes []previewVideo, off []previewsOffRow) {
	at := map[string]int{}
	for i := range videos {
		v := &videos[i]
		if v.makes() {
			makes = append(makes, *v)

			continue
		}
		n, seen := at[v.libraryName()]
		if !seen {
			n = len(off)
			at[v.libraryName()] = n
			off = append(off, previewsOffRow{Library: v.libraryName(), Why: v.off()})
		}
		off[n].Videos++
	}
	slices.SortFunc(off, func(a, b previewsOffRow) int { return strings.Compare(a.Library, b.Library) })

	return makes, off
}

func auditPreviews(ctx context.Context, client *embyfin.Client, in previewsIn) (previewsOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	checks := in.MaxChecks
	if checks <= 0 {
		checks = defaultPreviewChecks
	}
	videos, note, err := previewVideos(ctx, client, in.Library, in.IDs, in.Types)
	if err != nil {
		return previewsOut{}, err
	}
	makes, off := previewsOff(videos)

	out := previewsOut{Findings: []previewFinding{}, ByProblem: map[string]int{}, Off: off, Note: note}
	start := min(max(in.Offset, 0), len(makes))
	batch := makes[start:min(start+checks, len(makes))]
	if start+len(batch) < len(makes) {
		out.NextOffset = start + len(batch)
	}
	previews, err := readPreviews(ctx, client, batch)
	if err != nil {
		return previewsOut{}, err
	}
	for i := range batch {
		it := &batch[i].item
		out.Scanned++
		problem, detail, judged := previewProblem(&previews[i], it)
		if !judged {
			out.Unjudged++
		}
		if problem == "" {
			continue
		}
		out.Found++
		out.ByProblem[problem]++
		if len(out.Findings) < limit {
			out.Findings = append(out.Findings, previewFinding{
				ID: it.ID, Name: episodeOrItemName(it), Year: it.ProductionYear, Path: it.Path,
				Library: batch[i].libraryName(), Problem: problem, Detail: detail,
			})
		}
	}

	return out, nil
}

// itemBookkeeping are the fields of an item the server moves on every save,
// whatever the save changed, and a user's own state of it: none of them is
// what a refresh did to the item.
var itemBookkeeping = []string{"Etag", "DateModified", "UserData", "ServerId"}

// withoutChapterImages is a part of an item less the image tag on each of
// its chapters. Emby shows a chapter's picture out of the video's preview
// thumbnails, so every chapter gains a tag when the thumbnails are made and
// loses it when they go (seen on 4.10.1): that is the thumbnails themselves,
// and not something else the refresh did.
func withoutChapterImages(v any, inChapters bool) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			if inChapters && k == "ImageTag" {
				continue
			}
			out[k] = withoutChapterImages(e, k == "Chapters")
		}

		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = withoutChapterImages(e, inChapters)
		}

		return out
	}

	return v
}

// alsoChanged names the fields of an item that read differently after a
// refresh than before it, as the server names them, in order: what the
// refresh did besides making the thumbnails.
func alsoChanged(before, after map[string]any) []string {
	var changed []string
	for field := range before {
		if _, kept := after[field]; !kept {
			changed = append(changed, field)
		}
	}
	for field, now := range after {
		was, had := before[field]
		if !had || !reflect.DeepEqual(withoutChapterImages(was, field == "Chapters"), withoutChapterImages(now, field == "Chapters")) {
			changed = append(changed, field)
		}
	}
	changed = slices.DeleteFunc(changed, func(f string) bool { return slices.Contains(itemBookkeeping, f) })
	slices.Sort(changed)

	return slices.Compact(changed)
}

func regeneratePreviews(ctx context.Context, client *embyfin.Client, in regenerateIn, run previewRun) (regenerateOut, error) {
	ids := nonEmpty(in.IDs)
	if len(ids) == 0 && strings.TrimSpace(in.Library) == "" {
		if client.Backend() != embyfin.Emby {
			return regenerateOut{}, embyfin.ErrNoPreviewFiles
		}

		return regenerateOut{}, errors.New("give ids, or a library to take the next videos of: every library at once is not offered, since each video makes Emby read the whole file")
	}
	limit := min(cmp.Or(max(in.Limit, 0), defaultRegenerate), mostRegenerate)
	deadline := run.now().Add(time.Duration(min(cmp.Or(max(in.Minutes, 0), defaultRegenerateMinutes), mostRegenerateMinutes)) * time.Minute)

	videos, note, err := previewVideos(ctx, client, in.Library, ids, in.Types)
	if err != nil {
		return regenerateOut{}, err
	}
	admin, err := client.ResolveUser(ctx, "")
	if err != nil {
		return regenerateOut{}, err
	}
	out := regenerateOut{Videos: []regenerateRow{}, Note: note}
	byIDs := len(ids) > 0
	row := func(v *previewVideo, was, result, detail string) *regenerateRow {
		out.Videos = append(out.Videos, regenerateRow{
			ID: v.item.ID, Name: episodeOrItemName(&v.item), Path: v.item.Path, Library: v.libraryName(),
			Was: was, Result: result, Detail: detail,
		})

		return &out.Videos[len(out.Videos)-1]
	}

	makes, off := previewsOff(videos)
	if byIDs {
		// a video named and not read has a row saying why
		for i := range videos {
			if v := &videos[i]; !v.makes() {
				row(v, "not read", "skipped", v.off())
				out.Skipped++
			}
		}
	} else if len(makes) == 0 && len(off) > 0 {
		out.Off = &off[0]

		return out, nil
	}

	nfo := map[string]bool{}
	start := min(max(in.Offset, 0), len(makes))
	// stop ends the call at the video it has not looked at yet
	stop := func(at int, why string) {
		out.Stopped = why
		if !byIDs {
			out.NextOffset = at
		}
		for i := at; byIDs && i < len(makes); i++ {
			out.NotReached = append(out.NotReached, makes[i].item.ID)
		}
	}
	done := false
	for at := start; at < len(makes) && !done; at += previewLookAhead {
		chunk := makes[at:min(at+previewLookAhead, len(makes))]
		previews, err := readPreviews(ctx, client, chunk)
		if err != nil {
			return out, err
		}
		for i := range chunk {
			v := &chunk[i]
			problem, detail, _ := previewProblem(&previews[i], &v.item)
			if problem == "" {
				out.Checked++
				if byIDs {
					row(v, "sound", "skipped", fmt.Sprintf("its %d thumbnails are sound, and are left alone", previews[i].Thumbnails))
					out.Skipped++
				}

				continue
			}
			was := problem + ": " + detail
			if problem == previewLength && !byIDs && !in.Length {
				out.Checked++
				row(v, was, "skipped", "its thumbnails are sound, and which of them and the runtime is wrong cannot be told from here: pass length true to make them again from the file there now")
				out.Skipped++

				continue
			}
			switch {
			case out.Made+out.NotMade >= limit:
				stop(at+i, fmt.Sprintf("the limit of %d videos was reached", limit))
				done = true
			case !run.now().Before(deadline):
				stop(at+i, "the minutes ran out")
				done = true
			}
			if done {
				break
			}
			out.Checked++
			// a refresh searches for what an item is missing, and an item
			// with no id is missing its match
			if len(v.item.ProviderIDs) == 0 && !v.library.FetchersOff(v.item.Type) && !in.Unmatched {
				row(v, was, "skipped", "it holds no provider id and its library fetches metadata, so the refresh that makes thumbnails may match it to a title: identify it first, or pass unmatched true to take it as it is")
				out.Skipped++

				continue
			}

			r := row(v, was, "", "")
			before, err := client.FullItem(ctx, admin.ID, v.item.ID)
			if err != nil {
				r.Result, r.Detail = "failed", fmt.Sprintf("nothing was asked for: reading the item first failed: %v", err)
				out.Failed++
				stop(at+i+1, "a request failed")
				done = true

				break
			}
			asked := run.now()
			after, ran, err := run.make(ctx, v.item.ID)
			r.TookS = int(run.now().Sub(asked).Round(time.Second).Seconds())
			if err != nil {
				r.Result, r.Detail = "failed", err.Error()
				out.Failed++
				stop(at+i+1, "a request failed")
				done = true

				break
			}
			nfo[v.libraryName()] = nfo[v.libraryName()] || v.library.SavesNfo
			r.Thumbnails = after.Thumbnails
			if !ran {
				r.Result = "asked"
				r.Detail = fmt.Sprintf("the refresh had not run after %s: Emby runs refreshes one at a time, and this one is still waiting or still reading the video. It is made when its turn comes, so do not ask for it again: audit_previews with its id says when it is there", runtimeSaid(r.TookS))
				out.Asked++
				stop(at+i+1, "a refresh had not run when the wait for it ended")
				done = true

				break
			}
			now, err := client.FullItem(ctx, admin.ID, v.item.ID)
			if err != nil {
				r.Result, r.Detail = "failed", fmt.Sprintf("the refresh ran, but reading the item after it failed, so what else it changed is not known: %v", err)
				out.Failed++
				stop(at+i+1, "a request failed")
				done = true

				break
			}
			r.AlsoChanged = alsoChanged(before, now)
			// against the runtime the item holds now: the refresh read the
			// file's streams again
			refreshed := v.item
			if ticks, ok := now["RunTimeTicks"].(float64); ok {
				refreshed.RunTimeTicks = int64(ticks)
			}
			still, what, _ := previewProblem(&after, &refreshed)
			if !after.Sound() {
				r.Result, r.Detail = "not_made", fmt.Sprintf("Emby ran the refresh and the thumbnails are still %s: %s", still, what)
				out.NotMade++

				continue
			}
			if still != "" {
				r.Detail = "made from the file there now, and still not the length the server holds for the video: " + what
			}
			r.Result = "made"
			out.Made++
		}
	}

	var saving []string
	for name, saves := range nfo {
		if saves {
			saving = append(saving, name)
		}
	}
	slices.Sort(saving)
	if len(saving) > 0 {
		out.Note = joinNotes(out.Note, listed(saving, 5)+" saves nfo files: Emby wrote the nfo of each video it refreshed here again, with what the item held")
	}

	return out, nil
}

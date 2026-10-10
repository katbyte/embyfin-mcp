package embyfin

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/katbyte/go-kt/pointer"

	"github.com/katbyte/embyfin-mcp/sdk/emby"
)

// Emby DTOs to neutral types, one function per DTO. Nothing here talks to the
// server; the methods do, and hand the typed results in.

// locationVirtual is how both servers mark an episode the library lacks.
const locationVirtual = "Virtual"

// orEmpty is an Emby answer's model, or an empty one for Emby's null result:
// Emby answers 204 with no body for a lookup that finds nothing, which the
// typed client hands back as a nil model and no error
// (emby-null-result-no-content). A list read that way is empty. A single
// item cannot be read as an empty one, so each single-item lookup checks for
// nil itself and answers noResult.
func orEmpty[T any](model *T) *T {
	if model == nil {
		return new(T)
	}

	return model
}

// noResult is the error for a single-item lookup Emby answered with its null
// result, which apiclient.IsNotFound reads as not found, as it does a 404.
func noResult(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrNoResult)...)
}

func itemsFromEmby(dtos []emby.BaseItemDto) []Item {
	items := make([]Item, 0, len(dtos))
	for i := range dtos {
		items = append(items, itemFromEmby(&dtos[i]))
	}

	return items
}

// lockedFromEmby is an item's locked fields, by name.
func lockedFromEmby(fields []emby.MetadataFields) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, string(f))
	}

	return out
}

func itemFromEmby(d *emby.BaseItemDto) Item {
	it := Item{
		ID:                d.Id,
		Name:              d.Name,
		OriginalTitle:     d.OriginalTitle,
		SortName:          d.SortName,
		PresentationKey:   d.PresentationUniqueKey,
		Settings:          settingsOf(d.ForcedSortName, lockedFromEmby(d.LockedFields)),
		Type:              d.Type,
		ProductionYear:    d.ProductionYear,
		PremiereDate:      d.PremiereDate,
		DateCreated:       d.DateCreated,
		DateModified:      d.DateModified,
		Path:              d.Path,
		ParentID:          d.ParentId,
		Overview:          d.Overview,
		Genres:            d.Genres,
		Tags:              d.Tags,
		TagItems:          nameRefsFromEmby(d.TagItems), // Emby 4.10 puts tags here; Tags is always null
		Studios:           nameRefsFromEmby(d.Studios),
		OfficialRating:    d.OfficialRating,
		CommunityRating:   float64(d.CommunityRating),
		RunTimeTicks:      d.RunTimeTicks,
		ProviderIDs:       d.ProviderIds,
		Etag:              d.Etag,
		ImageTags:         d.ImageTags,
		SeriesName:        d.SeriesName,
		SeriesID:          d.SeriesId,
		Album:             d.Album,
		AlbumArtist:       d.AlbumArtist,
		Artists:           d.Artists,
		ParentIndexNumber: d.ParentIndexNumber,
		IndexNumber:       d.IndexNumber,
		IndexNumberEnd:    d.IndexNumberEnd,
		PlaylistItemID:    d.PlaylistItemId,
		IsMissing:         d.LocationType == locationVirtual,
		IsFolder:          pointer.From(d.IsFolder),
		UserData:          userDataFromEmby(d.UserData),
	}
	if len(d.MediaSources) > 0 {
		it.MediaSources = make([]MediaSource, 0, len(d.MediaSources))
		for i := range d.MediaSources {
			it.MediaSources = append(it.MediaSources, mediaSourceFromEmby(&d.MediaSources[i]))
		}
	}
	if len(d.People) > 0 {
		it.People = make([]Person, 0, len(d.People))
		for _, p := range d.People {
			it.People = append(it.People, Person{Name: p.Name, ID: p.Id, Role: p.Role, Type: string(p.Type)})
		}
	}

	return it
}

func mediaSourceFromEmby(d *emby.MediaSourceInfo) MediaSource {
	ms := MediaSource{ItemID: d.ItemId, Name: d.Name, Container: d.Container, Size: d.Size, Bitrate: int64(d.Bitrate), Path: d.Path, RunTimeTicks: d.RunTimeTicks}
	if len(d.MediaStreams) > 0 {
		ms.MediaStreams = make([]MediaStream, 0, len(d.MediaStreams))
		for i := range d.MediaStreams {
			ms.MediaStreams = append(ms.MediaStreams, mediaStreamFromEmby(&d.MediaStreams[i]))
		}
	}

	return ms
}

func mediaStreamFromEmby(d *emby.MediaStream) MediaStream {
	return MediaStream{
		Type: string(d.Type), Codec: d.Codec, Language: d.Language, Width: d.Width, Height: d.Height,
		BitRate: int64(d.BitRate), Channels: d.Channels, DisplayTitle: d.DisplayTitle, IsExternal: pointer.From(d.IsExternal),
		IsForced: pointer.From(d.IsForced),
		// the servers give both; AverageFrameRate is the one over the whole
		// file, RealFrameRate the container's nominal one, and either is
		// enough to tell 23.976 from 60
		FrameRate:       cmp.Or(d.AverageFrameRate, d.RealFrameRate),
		ColourTransfer:  d.ColorTransfer,
		ColourPrimaries: d.ColorPrimaries,
		VideoRange:      d.VideoRange,
		AspectRatio:     d.AspectRatio,
		// Emby's own reading of the format, which VideoRange ("HDR 10") does
		// not narrow to Dolby Vision or HDR10+
		ExtendedVideoType:    string(d.ExtendedVideoType),
		ExtendedVideoSubType: string(d.ExtendedVideoSubType),
	}
}

func userDataFromEmby(d *emby.UserItemDataDto) *UserData {
	if d == nil {
		return nil
	}

	return &UserData{
		Played: pointer.From(d.Played), PlayCount: d.PlayCount, IsFavourite: pointer.From(d.IsFavorite),
		LastPlayedDate: d.LastPlayedDate, PlaybackPositionTicks: d.PlaybackPositionTicks,
		PlayedPercentage: d.PlayedPercentage, UnplayedItemCount: d.UnplayedItemCount,
	}
}

func nameRefsFromEmby(pairs []emby.NameLongIdPair) []NameRef {
	if len(pairs) == 0 {
		return nil
	}
	out := make([]NameRef, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, NameRef{Name: p.Name, ID: p.Id})
	}

	return out
}

func virtualFolderFromEmby(d *emby.VirtualFolderInfo) VirtualFolder {
	f := VirtualFolder{Name: d.Name, CollectionType: d.CollectionType, Locations: d.Locations, ItemID: d.ItemId, GUID: d.Guid}
	if o := d.LibraryOptions; o != nil {
		f.SavesNfo = slices.Contains(o.MetadataSavers, nfoSaver)
		// both: extraction on with no interval, or an interval with
		// extraction off, makes none
		if pointer.From(o.EnableChapterImageExtraction) && o.ThumbnailImagesIntervalSeconds > 0 {
			f.PreviewEveryS = o.ThumbnailImagesIntervalSeconds
		}
		if len(o.TypeOptions) > 0 {
			f.MetadataFetchers = make(map[string][]string, len(o.TypeOptions))
			for _, t := range o.TypeOptions {
				f.MetadataFetchers[t.Type] = t.MetadataFetchers
			}
		}
	}

	return f
}

func userFromEmby(d *emby.UserDto) User {
	u := User{ID: d.Id, Name: d.Name, LastActivityDate: d.LastActivityDate, LastLoginDate: d.LastLoginDate, HasPassword: pointer.From(d.HasPassword)}
	if p := d.Policy; p != nil {
		// Emby has one tag list: a block, or with IsTagBlockingModeInclusive
		// the only tags shown. IncludeTags hid nothing on 4.10 in either
		// mode, and is not read as a limit
		blocked, allowed := p.BlockedTags, []string(nil)
		if pointer.From(p.IsTagBlockingModeInclusive) {
			blocked, allowed = nil, p.BlockedTags
		}
		u.Policy = UserPolicy{
			IsAdministrator: pointer.From(p.IsAdministrator), IsDisabled: pointer.From(p.IsDisabled), IsHidden: pointer.From(p.IsHidden),
			EnableAllFolders: pointer.From(p.EnableAllFolders), EnabledFolders: p.EnabledFolders,
			EnableContentDeletion: pointer.From(p.EnableContentDeletion), EnableRemoteAccess: pointer.From(p.EnableRemoteAccess),
			BlockedTags: blocked, AllowedTags: allowed, BlockUnratedItems: unrated(p.BlockUnratedItems), BlockedFolders: p.ExcludedSubFolders,
			EnableAllChannels: pointer.From(p.EnableAllChannels), MaxParentalRating: p.MaxParentalRating,
		}
	}
	if c := d.Configuration; c != nil {
		u.Preferences = Preferences{
			AudioLanguage: c.AudioLanguagePreference, SubtitleLanguage: c.SubtitleLanguagePreference,
			SubtitleMode: string(c.SubtitleMode), PlayDefaultAudioTrack: pointer.From(c.PlayDefaultAudioTrack),
		}
	}

	return u
}

func sessionFromEmby(d *emby.SessionSessionInfo) Session {
	s := Session{
		ID: d.Id, UserName: d.UserName, Client: d.Client, AppVersion: d.ApplicationVersion, DeviceName: d.DeviceName,
		RemoteEndPoint: d.RemoteEndPoint, LastActivityDate: d.LastActivityDate,
	}
	if d.NowPlayingItem != nil {
		it := itemFromEmby(d.NowPlayingItem)
		s.NowPlayingItem = &it
	}
	if d.PlayState != nil {
		s.PlayState = PlayState{
			PositionTicks: d.PlayState.PositionTicks, IsPaused: pointer.From(d.PlayState.IsPaused),
			PlayMethod: string(d.PlayState.PlayMethod), MediaSourceID: d.PlayState.MediaSourceId,
		}
	}
	if t := d.TranscodingInfo; t != nil {
		s.Transcoding = &Transcoding{
			Container: t.Container, VideoCodec: t.VideoCodec, AudioCodec: t.AudioCodec,
			VideoDirect: pointer.From(t.IsVideoDirect), AudioDirect: pointer.From(t.IsAudioDirect),
			Bitrate: t.Bitrate, Width: t.Width, Height: t.Height, AudioChannels: t.AudioChannels,
			Framerate: float64(t.Framerate), Completion: t.CompletionPercentage,
			VideoDecoder: t.VideoDecoder, VideoEncoder: t.VideoEncoder,
			DecoderHardware: t.VideoDecoderIsHardware, EncoderHardware: t.VideoEncoderIsHardware,
			HardwareAcceleration: cmp.Or(t.VideoEncoderHwAccel, t.VideoDecoderHwAccel),
		}
		for _, r := range t.TranscodeReasons {
			s.Transcoding.Reasons = append(s.Transcoding.Reasons, string(r))
		}
	}

	return s
}

func taskFromEmby(d *emby.TaskInfo) Task {
	t := Task{
		ID: d.Id, Key: d.Key, Name: d.Name, Category: d.Category, Description: d.Description, State: string(d.State),
		Progress: d.CurrentProgressPercentage, Hidden: d.IsHidden != nil && *d.IsHidden,
	}
	for i := range d.Triggers {
		tr := &d.Triggers[i]
		t.Triggers = append(t.Triggers, TaskTrigger{
			Type: tr.Type, DayOfWeek: string(tr.DayOfWeek), SystemEvent: string(tr.SystemEvent), TimeOfDay: time.Duration(tr.TimeOfDayTicks) * tick,
			Interval: time.Duration(tr.IntervalTicks) * tick, MaxRuntime: time.Duration(tr.MaxRuntimeTicks) * tick,
		})
	}
	if r := d.LastExecutionResult; r != nil {
		t.LastExecutionResult = &TaskResult{
			Status: string(r.Status), StartTimeUtc: r.StartTimeUtc, EndTimeUtc: r.EndTimeUtc,
			ErrorMessage: r.ErrorMessage, LongErrorMessage: r.LongErrorMessage,
		}
	}

	return t
}

func activityFromEmby(d *emby.ActivityLogEntry) ActivityEntry {
	return ActivityEntry{
		Name: d.Name, Type: d.Type, Date: d.Date, Severity: string(d.Severity),
		ShortOverview: d.ShortOverview, UserID: d.UserId, ItemID: d.ItemId,
	}
}

func deviceFromEmby(d *emby.DevicesDeviceInfo) Device {
	return Device{
		Name: d.Name, AppName: d.AppName, AppVersion: d.AppVersion,
		LastUserName: d.LastUserName, DateLastActivity: d.DateLastActivity, ID: d.Id,
	}
}

func logFileFromEmby(d *emby.LogFile) LogFile {
	return LogFile{Name: d.Name, Size: d.Size, DateCreated: d.DateCreated, DateModified: d.DateModified}
}

func systemInfoFromEmby(d *emby.SystemInfo) *SystemInfo {
	return &SystemInfo{
		ServerName: d.ServerName, Version: d.Version, ID: d.Id, OperatingSystem: d.OperatingSystem,
		HasPendingRestart: pointer.From(d.HasPendingRestart), HasUpdateAvailable: pointer.From(d.HasUpdateAvailable),
		IsShuttingDown: pointer.From(d.IsShuttingDown), CanSelfRestart: pointer.From(d.CanSelfRestart),
		Paths: systemPaths(d.ProgramDataPath, d.LogPath, d.CachePath, d.InternalMetadataPath, d.TranscodingTempPath),
	}
}

func countsFromEmby(d *emby.ItemCounts) *ItemCounts {
	return &ItemCounts{
		MovieCount: d.MovieCount, SeriesCount: d.SeriesCount, EpisodeCount: d.EpisodeCount, AlbumCount: d.AlbumCount,
		SongCount: d.SongCount, MusicVideoCount: d.MusicVideoCount, BoxSetCount: d.BoxSetCount, TrailerCount: d.TrailerCount,
	}
}

func remoteSearchResultFromEmby(d *emby.RemoteSearchResult) RemoteSearchResult {
	return RemoteSearchResult{
		Name: d.Name, ProductionYear: d.ProductionYear, PremiereDate: d.PremiereDate, Overview: d.Overview,
		ProviderIDs: d.ProviderIds, SearchProviderName: d.SearchProviderName, ImageURL: d.ImageUrl,
	}
}

func remoteSearchResultToEmby(r *RemoteSearchResult) emby.RemoteSearchResult {
	return emby.RemoteSearchResult{
		Name: r.Name, ProductionYear: r.ProductionYear, PremiereDate: r.PremiereDate, Overview: r.Overview,
		ProviderIds: r.ProviderIDs, SearchProviderName: r.SearchProviderName, ImageUrl: r.ImageURL,
	}
}

func imageInfoFromEmby(d *emby.ImageInfo) ImageInfo {
	return ImageInfo{ImageType: string(d.ImageType), Width: d.Width, Height: d.Height, Size: d.Size, Path: d.Path}
}

func remoteImageFromEmby(d *emby.RemoteImageInfo) RemoteImage {
	return RemoteImage{
		ProviderName: d.ProviderName, URL: d.Url, Type: string(d.Type), Width: d.Width, Height: d.Height,
		Language: d.Language, CommunityRating: d.CommunityRating, VoteCount: d.VoteCount,
	}
}

func remoteSubtitleFromEmby(d *emby.RemoteSubtitleInfo) RemoteSubtitle {
	return RemoteSubtitle{
		ID: d.Id, Name: d.Name, ProviderName: d.ProviderName, Format: d.Format,
		ThreeLetterISOLanguageName: d.ThreeLetterISOLanguageName, DownloadCount: d.DownloadCount,
		CommunityRating: float64(d.CommunityRating), Comment: d.Comment,
	}
}

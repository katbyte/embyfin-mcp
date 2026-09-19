package embyfin

import (
	"cmp"
	"slices"

	"github.com/katbyte/go-kt/pointer"

	"github.com/katbyte/embyfin-mcp/lib/jf"
)

// Jellyfin DTOs to neutral types, one function per DTO. Jellyfin's enums are
// named string types in the typed client; the neutral types keep them as the
// plain strings both servers send.

func itemsFromJF(dtos []jf.BaseItemDto) []Item {
	items := make([]Item, 0, len(dtos))
	for i := range dtos {
		items = append(items, itemFromJF(&dtos[i]))
	}

	return items
}

func itemFromJF(d *jf.BaseItemDto) Item {
	it := Item{
		ID:                d.Id,
		Name:              d.Name,
		OriginalTitle:     d.OriginalTitle,
		Type:              string(d.Type),
		ProductionYear:    d.ProductionYear,
		PremiereDate:      d.PremiereDate,
		DateCreated:       d.DateCreated,
		Path:              d.Path,
		ParentID:          d.ParentId,
		Overview:          d.Overview,
		Genres:            d.Genres,
		Tags:              d.Tags,
		Studios:           nameRefsFromJF(d.Studios),
		OfficialRating:    d.OfficialRating,
		CommunityRating:   float64(d.CommunityRating),
		RunTimeTicks:      d.RunTimeTicks,
		ProviderIDs:       d.ProviderIds,
		ImageTags:         d.ImageTags,
		SeriesName:        d.SeriesName,
		SeriesID:          d.SeriesId,
		ParentIndexNumber: d.ParentIndexNumber,
		IndexNumber:       d.IndexNumber,
		IndexNumberEnd:    d.IndexNumberEnd,
		PlaylistItemID:    d.PlaylistItemId,
		IsMissing:         d.LocationType == locationVirtual,
		UserData:          userDataFromJF(d.UserData),
	}
	if len(d.MediaSources) > 0 {
		it.MediaSources = make([]MediaSource, 0, len(d.MediaSources))
		for i := range d.MediaSources {
			it.MediaSources = append(it.MediaSources, mediaSourceFromJF(&d.MediaSources[i]))
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

func mediaSourceFromJF(d *jf.MediaSourceInfo) MediaSource {
	ms := MediaSource{Container: d.Container, Size: d.Size, Bitrate: int64(d.Bitrate), Path: d.Path}
	if len(d.MediaStreams) > 0 {
		ms.MediaStreams = make([]MediaStream, 0, len(d.MediaStreams))
		for i := range d.MediaStreams {
			ms.MediaStreams = append(ms.MediaStreams, mediaStreamFromJF(&d.MediaStreams[i]))
		}
	}

	return ms
}

func mediaStreamFromJF(d *jf.MediaStream) MediaStream {
	return MediaStream{
		Type: string(d.Type), Codec: d.Codec, Language: d.Language, Width: d.Width, Height: d.Height,
		BitRate: int64(d.BitRate), Channels: d.Channels, DisplayTitle: d.DisplayTitle, IsExternal: pointer.From(d.IsExternal),
		// the servers give both; AverageFrameRate is the one over the whole
		// file, RealFrameRate the container's nominal one, and either is
		// enough to tell 23.976 from 60
		FrameRate:       cmp.Or(d.AverageFrameRate, d.RealFrameRate),
		ColourTransfer:  d.ColorTransfer,
		ColourPrimaries: d.ColorPrimaries,
		VideoRange:      string(d.VideoRange),
		VideoRangeType:  string(d.VideoRangeType),
	}
}

func userDataFromJF(d *jf.UserItemDataDto) *UserData {
	if d == nil {
		return nil
	}

	return &UserData{
		Played: pointer.From(d.Played), PlayCount: d.PlayCount, IsFavourite: pointer.From(d.IsFavorite),
		LastPlayedDate: d.LastPlayedDate, PlaybackPositionTicks: d.PlaybackPositionTicks,
		PlayedPercentage: d.PlayedPercentage, UnplayedItemCount: d.UnplayedItemCount,
	}
}

func nameRefsFromJF(pairs []jf.NameGuidPair) []NameRef {
	if len(pairs) == 0 {
		return nil
	}
	out := make([]NameRef, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, NameRef{Name: p.Name, ID: p.Id})
	}

	return out
}

func virtualFolderFromJF(d *jf.VirtualFolderInfo) VirtualFolder {
	f := VirtualFolder{Name: d.Name, CollectionType: string(d.CollectionType), Locations: d.Locations, ItemID: d.ItemId}
	if o := d.LibraryOptions; o != nil {
		// no list at all is every saver
		f.SavesNfo = o.MetadataSavers == nil || slices.Contains(o.MetadataSavers, nfoSaver)
		if len(o.TypeOptions) > 0 {
			f.MetadataFetchers = make(map[string][]string, len(o.TypeOptions))
			for _, t := range o.TypeOptions {
				f.MetadataFetchers[t.Type] = t.MetadataFetchers
			}
		}
	}

	return f
}

func userFromJF(d *jf.UserDto) User {
	u := User{ID: d.Id, Name: d.Name, LastActivityDate: d.LastActivityDate, LastLoginDate: d.LastLoginDate, HasPassword: pointer.From(d.HasPassword)}
	if p := d.Policy; p != nil {
		u.Policy = UserPolicy{
			IsAdministrator: pointer.From(p.IsAdministrator), IsDisabled: pointer.From(p.IsDisabled), IsHidden: pointer.From(p.IsHidden),
			EnableAllFolders: pointer.From(p.EnableAllFolders), EnabledFolders: p.EnabledFolders,
			EnableContentDeletion: pointer.From(p.EnableContentDeletion), EnableRemoteAccess: pointer.From(p.EnableRemoteAccess),
			MaxParentalRating: p.MaxParentalRating,
		}
	}

	return u
}

func sessionFromJF(d *jf.SessionInfoDto) Session {
	s := Session{
		ID: d.Id, UserName: d.UserName, Client: d.Client, DeviceName: d.DeviceName, LastActivityDate: d.LastActivityDate,
	}
	if d.NowPlayingItem != nil {
		it := itemFromJF(d.NowPlayingItem)
		s.NowPlayingItem = &it
	}
	if d.PlayState != nil {
		s.PlayState = PlayState{PositionTicks: d.PlayState.PositionTicks, IsPaused: pointer.From(d.PlayState.IsPaused)}
	}

	return s
}

func taskFromJF(d *jf.TaskInfo) Task {
	t := Task{ID: d.Id, Name: d.Name, Category: d.Category, State: string(d.State)}
	if r := d.LastExecutionResult; r != nil {
		t.LastExecutionResult = &TaskResult{Status: string(r.Status), StartTimeUtc: r.StartTimeUtc, EndTimeUtc: r.EndTimeUtc, ErrorMessage: r.ErrorMessage}
	}

	return t
}

func activityFromJF(d *jf.ActivityLogEntry) ActivityEntry {
	return ActivityEntry{
		Name: d.Name, Type: d.Type, Date: d.Date, Severity: string(d.Severity),
		ShortOverview: d.ShortOverview, UserID: d.UserId, ItemID: d.ItemId,
	}
}

func deviceFromJF(d *jf.DeviceInfoDto) Device {
	return Device{
		Name: d.Name, AppName: d.AppName, AppVersion: d.AppVersion,
		LastUserName: d.LastUserName, DateLastActivity: d.DateLastActivity, ID: d.Id,
	}
}

func logFileFromJF(d *jf.LogFile) LogFile {
	return LogFile{Name: d.Name, Size: d.Size, DateModified: d.DateModified}
}

func systemInfoFromJF(d *jf.SystemInfo) *SystemInfo {
	return &SystemInfo{ServerName: d.ServerName, Version: d.Version, ID: d.Id, OperatingSystem: d.OperatingSystem}
}

func countsFromJF(d *jf.ItemCounts) *ItemCounts {
	return &ItemCounts{
		MovieCount: d.MovieCount, SeriesCount: d.SeriesCount, EpisodeCount: d.EpisodeCount, AlbumCount: d.AlbumCount,
		SongCount: d.SongCount, MusicVideoCount: d.MusicVideoCount, BoxSetCount: d.BoxSetCount, TrailerCount: d.TrailerCount,
	}
}

func remoteSearchResultFromJF(d *jf.RemoteSearchResult) RemoteSearchResult {
	return RemoteSearchResult{
		Name: d.Name, ProductionYear: d.ProductionYear, PremiereDate: d.PremiereDate, Overview: d.Overview,
		ProviderIDs: d.ProviderIds, SearchProviderName: d.SearchProviderName, ImageURL: d.ImageUrl,
	}
}

func remoteSearchResultToJF(r *RemoteSearchResult) jf.RemoteSearchResult {
	return jf.RemoteSearchResult{
		Name: r.Name, ProductionYear: r.ProductionYear, PremiereDate: r.PremiereDate, Overview: r.Overview,
		ProviderIds: r.ProviderIDs, SearchProviderName: r.SearchProviderName, ImageUrl: r.ImageURL,
	}
}

func imageInfoFromJF(d *jf.ImageInfo) ImageInfo {
	return ImageInfo{ImageType: string(d.ImageType), Width: d.Width, Height: d.Height, Size: d.Size}
}

func remoteImageFromJF(d *jf.RemoteImageInfo) RemoteImage {
	return RemoteImage{
		ProviderName: d.ProviderName, URL: d.Url, Type: string(d.Type), Width: d.Width, Height: d.Height,
		Language: d.Language, CommunityRating: d.CommunityRating, VoteCount: d.VoteCount,
	}
}

func remoteSubtitleFromJF(d *jf.RemoteSubtitleInfo) RemoteSubtitle {
	return RemoteSubtitle{
		ID: d.Id, Name: d.Name, ProviderName: d.ProviderName, Format: d.Format,
		ThreeLetterISOLanguageName: d.ThreeLetterISOLanguageName, DownloadCount: d.DownloadCount,
		CommunityRating: float64(d.CommunityRating), Comment: d.Comment,
	}
}

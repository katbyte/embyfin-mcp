//go:build integration

package integration

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/jf"
)

// TestJFReadSweep calls every Jellyfin GET against the fixtures (see
// sweep_test.go).
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFReadSweep(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	moviesID := jfLibrary(t, sdkMovies)
	jfLibrary(t, sdkShows)

	musicID := jfLibrary(t, sdkMusic)

	movie := jfMovie(t, alien)
	item := must(jfc.GetItem(ctx, movie.Id, jf.GetItemOperationOptions{UserId: adminID})).Model
	album := jfFirst(t, musicID, jf.BaseItemKindMusicAlbum)
	song := jfFirst(t, musicID, jf.BaseItemKindAudio)
	musicArtists := must(jfc.GetArtists(ctx, jf.GetArtistsOperationOptions{ParentId: musicID, UserId: adminID})).Model.Items
	if len(musicArtists) == 0 {
		t.Fatal("the music fixtures hold no artist")
	}
	// Jellyfin 12.1 documents no music genre listing, and /Genres of a music
	// library lists the genres its tags name. Not the first music genre the
	// server holds: those include what MusicBrainz says of an artist only the
	// tags name (The Pink Floyd's acid rock), which no album in the library
	// carries to make the genre's image from
	musicGenres := must(jfc.GetGenres(ctx, jf.GetGenresOperationOptions{ParentId: musicID, UserId: adminID})).Model.Items
	if len(musicGenres) == 0 {
		t.Fatal("the music fixtures carry no genre")
	}
	musicGenre := musicGenres[0]
	series := jfSeries(t, severance)
	people := must(jfc.GetPersons(ctx, jf.GetPersonsOperationOptions{SearchTerm: ridleyScott})).Model.Items
	studios := must(jfc.GetStudios(ctx, jf.GetStudiosOperationOptions{ParentId: moviesID})).Model.Items
	logs := must(jfc.GetServerLogs(ctx)).Model
	plugins := must(jfc.GetPlugins(ctx)).Model
	devices := must(jfc.GetDevices(ctx, jf.GetDevicesOperationOptions{})).Model.Items
	if len(people) == 0 || len(studios) == 0 || len(logs) == 0 || len(plugins) == 0 || len(devices) == 0 || len(item.MediaSources) == 0 {
		t.Fatalf("the fixtures are incomplete: %d people, %d studios, %d logs, %d plugins, %d devices, %d media sources",
			len(people), len(studios), len(logs), len(plugins), len(devices), len(item.MediaSources))
	}

	// the film with a subtitle, for the routes that hand one out
	subtitled := must(jfc.GetItem(ctx, jfMovie(t, thirteenthFloor).Id, jf.GetItemOperationOptions{UserId: adminID})).Model
	if len(subtitled.MediaSources) == 0 {
		t.Fatalf("%s has no media source", thirteenthFloor)
	}
	srt := slices.IndexFunc(subtitled.MediaSources[0].MediaStreams, func(s jf.MediaStream) bool { return s.Type == jf.MediaStreamTypeSubtitle })
	if srt < 0 {
		t.Fatalf("%s lists no subtitle stream: %+v", thirteenthFloor, subtitled.MediaSources[0].MediaStreams)
	}
	srtIndex := strconv.Itoa(subtitled.MediaSources[0].MediaStreams[srt].Index)
	// the catalogue the package routes read
	packages := must(jfc.GetPackages(ctx)).Model
	if len(packages) == 0 {
		t.Fatal("GetPackages listed nothing")
	}
	tmdbPlugin := slices.IndexFunc(plugins, func(p jf.PluginInfo) bool { return p.Name == "TMDb" })
	if tmdbPlugin < 0 {
		t.Fatalf("no TMDb plugin among %+v", plugins)
	}

	// a playlist to read, and one of music for the mix made from a playlist
	created := must(jfc.CreatePlaylist(ctx, jf.CreatePlaylistDto{Name: "SDK Sweep", Ids: []string{movie.Id}, MediaType: jf.MediaTypeVideo, UserId: adminID})).Model
	t.Cleanup(func() { _, _ = jfc.DeleteItem(context.WithoutCancel(ctx), created.Id) })
	music := must(jfc.CreatePlaylist(ctx, jf.CreatePlaylistDto{Name: "SDK Sweep Music", Ids: []string{song.Id}, MediaType: jf.MediaTypeAudio, UserId: adminID})).Model
	t.Cleanup(func() { _, _ = jfc.DeleteItem(context.WithoutCancel(ctx), music.Id) })
	// and a collection holding the film, once its member has landed (a scan's
	// refresh can write over it; see TestJFCollections)
	jfScanIdle(ctx, t)
	collection := must(jfc.CreateCollection(ctx, jf.CreateCollectionOperationOptions{Name: "SDK Sweep", Ids: []string{movie.Id}})).Model.Id
	t.Cleanup(func() { jfDeleteCollection(context.WithoutCancel(ctx), t, collection) })
	if !poll(editPatience, func() bool {
		res := must(jfc.GetItems(ctx, jf.GetItemsOperationOptions{ParentId: collection, UserId: adminID})).Model
		return len(res.Items) == 1 && res.Items[0].Id == movie.Id
	}) {
		t.Fatalf("the sweep's collection never held %s", alien)
	}

	fixtures := sweepFixtures{
		path: map[string]string{
			"itemId":               movie.Id,
			"userId":               adminID,
			"seriesId":             series.Id,
			"playlistId":           created.Id,
			"Playlists/itemId":     created.Id,
			"taskId":               jfScanTask(ctx, t).Id,
			"pluginId":             plugins[0].Id,
			"version":              plugins[0].Version,
			"displayPreferencesId": "usersettings",
			"key":                  "encoding",
			"Persons/name":         ridleyScott,
			"Genres/genreName":     "Science Fiction",
			"Genres/name":          "Science Fiction",
			"Studios/name":         studios[0].Name,
			"Albums/itemId":        album.Id,
			"Artists/itemId":       musicArtists[0].Id,
			"Artists/name":         musicArtists[0].Name,
			"Songs/itemId":         song.Id,
			// the track with the .lrc beside it, so the lyric read has
			// something to answer with
			"Audio/itemId":          jfSong(t, musicID, lyricTrack).Id,
			"MusicGenres/genreName": musicGenre.Name,
			"MusicGenres/name":      musicGenre.Name,
			"year":                  "1982",
			"imageType":             "Primary",
			"imageIndex":            "0",
			"tag":                   item.ImageTags["Primary"],
			"format":                "jpg",
			"maxWidth":              "100",
			"maxHeight":             "100",
			"percentPlayed":         "0",
			"unplayedCount":         "0",
			"container":             "mp4",
			"mediaSourceId":         item.MediaSources[0].Id,
			"language":              "eng",
		},
		options: map[string]string{
			"name":          logs[0].Name,
			"client":        "emby",
			"path":          "/media",
			"searchTerm":    alien,
			"id":            devices[0].Id,
			"mediaSourceId": item.MediaSources[0].Id,
		},
		// an API key acts as no user, and Jellyfin answers 400 to a
		// user-scoped read that does not name one
		always: map[string]string{"userId": adminID},
	}

	cases := maps.Clone(jfSweepCases)
	cases["GetVideoStream"] = sweepCase{Options: map[string]any{"Container": "mp4", "Static": true}}
	cases["GetVideoStreamByContainer"] = sweepCase{Options: map[string]any{"Static": true}}
	cases["GetAudioStream"] = sweepCase{Options: map[string]any{"Container": "mp3", "Static": true}}
	cases["GetAudioStreamByContainer"] = sweepCase{Path: map[string]string{"container": "mp3"}, Options: map[string]any{"Static": true}}
	cases["GetUniversalAudioStream"] = sweepCase{Options: map[string]any{"Container": "mp3", "UserId": adminID}}
	// the genre mix is the one route that takes the genre as a required id
	cases["GetInstantMixFromMusicGenreById"] = sweepCase{Options: map[string]any{"Id": musicGenre.Id}}
	// a mix is made from music, and an album is like another by its
	// artist's other album
	cases["GetInstantMixFromItem"] = sweepCase{Path: map[string]string{"itemId": album.Id}}
	cases["GetInstantMixFromPlaylist"] = sweepCase{Path: map[string]string{"itemId": music.Id}}
	cases["GetSimilarAlbums"] = sweepCase{Path: map[string]string{"itemId": jfAlbum(t, musicID, darkSide).Id}}
	// the subtitle routes read the film with one
	subtitle := map[string]string{"routeItemId": subtitled.Id, "routeMediaSourceId": subtitled.MediaSources[0].Id, "routeIndex": srtIndex, "routeFormat": "srt", "routeStartPositionTicks": "0"}
	cases["GetSubtitle"] = sweepCase{Path: subtitle}
	cases["GetSubtitleWithTicks"] = sweepCase{Path: subtitle}
	cases["GetSubtitlePlaylist"] = sweepCase{
		Path:    map[string]string{"itemId": subtitled.Id, "mediaSourceId": subtitled.MediaSources[0].Id, "index": srtIndex},
		Options: map[string]any{"SegmentLength": 30},
	}
	// a package from the catalogue, and the TMDb plugin's settings (the
	// first plugin's are one false flag)
	cases["GetPackageInfo"] = sweepCase{Path: map[string]string{"name": packages[0].Name}}
	cases["GetPluginConfiguration"] = sweepCase{Path: map[string]string{"pluginId": plugins[tmdbPlugin].Id}}
	// the folder listing lists files only unless asked for folders (/media
	// holds none), and the legacy filters answer for a library
	cases["GetDirectoryContents"] = sweepCase{Options: map[string]any{"IncludeDirectories": true}}
	cases["GetQueryFiltersLegacy"] = sweepCase{Options: map[string]any{"ParentId": moviesID}}

	sweep(t, "jellyfin", jfc, fixtures, cases)
}

// Why a Jellyfin GET does not simply answer in the sweep.
const (
	jfNoTuner         = "Live TV needs a tuner and a guide provider; the container has neither"
	jfNoImage         = "the fixture has no image of that type, which the server answers with an error"
	jfNoLyricProvider = "no lyric provider is installed, so there is nothing to search; the .lrc beside a fixture track covers the local read"
	jfNoChannel       = "no channel plugin is installed, so there is no channel to ask about"
	jfNoSyncPlay      = "SyncPlay needs a group, which needs a second connected client"
	jfKeyNotUser      = "Jellyfin checks a playlist's access against the calling user, and an API key acts as none, whatever userId says"
	jfNoBranding      = "no branding is set on a fresh server"
	jfNoExtras        = "the fixture films have no intros, trailers, extras or additional parts"
)

// jfSweepCases classifies the Jellyfin GETs that do not simply answer.
var jfSweepCases = map[string]sweepCase{
	// Live TV
	"GetChannel":           {Skip: jfNoTuner},
	"GetLiveRecordingFile": {Skip: jfNoTuner},
	"GetLiveStreamFile":    {Skip: jfNoTuner},
	"GetProgram":           {Skip: jfNoTuner},
	"GetRecording":         {Skip: jfNoTuner},
	"GetSeriesTimer":       {Skip: jfNoTuner},
	"GetTimer":             {Skip: jfNoTuner},

	// music: the SDK Music library answers the rest (see TestJFReadSweep)
	// an artist has no image of its own, but a genre answers with one of its
	// items' images, so only the artist route needs classifying
	"GetArtistImage":     {Status: 404, Why: jfNoImage},
	"SearchRemoteLyrics": {Skip: jfNoLyricProvider},
	"GetRemoteLyrics":    {Skip: jfNoLyricProvider + " (and it takes an id from that search)"},

	"GetChannelFeatures": {Skip: jfNoChannel},
	"GetChannelItems":    {Skip: jfNoChannel},
	"SyncPlayGetGroup":   {Skip: jfNoSyncPlay},

	// Live TV reads that reach for a guide provider
	"GetChannelMappingOptions":      {Skip: jfNoTuner},
	"GetLineups":                    {Skip: jfNoTuner},
	"GetSchedulesDirectCountries":   {Skip: jfNoTuner + " (it asks Schedules Direct, through the provider proxy)"},
	"GetRecordingFolders":           {Skip: jfNoTuner},
	"GetQuickConnectState":          {Skip: "QuickConnect needs a secret from a second device's pairing request"},
	"GetFallbackFont":               {Skip: "no fallback font directory is configured, so there is no font to fetch"},
	"GetRemoteSubtitles":            {Skip: "needs a subtitle id from a provider search, and no subtitle provider is installed"},
	"GetTrickplayHlsPlaylist":       {Skip: "no trickplay tiles are generated for the one-second fixtures"},
	"GetTrickplayTileImage":         {Skip: "no trickplay tiles are generated for the one-second fixtures"},
	"GetAttachment":                 {Skip: "the fixture videos carry no attachments"},
	"GetBackup":                     {Status: 404, Why: "no backup has been made, so there is no manifest at the path"},
	"GetDashboardConfigurationPage": {Status: 404, Why: "no plugin configuration page is named; the route answers 404 without one"},
	"GetStudioImage":                {Status: 404, Why: "the fixture studio has no image"},
	"GetStudioImageByIndex":         {Status: 404, Why: "the fixture studio has no image"},
	"GetCurrentUser":                {Status: 400, Why: "an API key acts as no user, so there is no current user"},
	"SyncPlayGetGroups":             {Skip: jfNoSyncPlay},
	"GetDeviceOptions":              {Status: 404, Why: "no options are stored for the fixture device until a client sets them"},
	"GetUserImage":                  {Status: 404, Why: "the fixture user has no image"},
	"GetPlaylist":                   {Status: 400, Why: jfKeyNotUser},
	"GetPlaylistUsers":              {Status: 400, Why: jfKeyNotUser},
	"GetPlaylistUser":               {Status: 400, Why: jfKeyNotUser},
	"GetTrailers":                   {Status: 500, Why: "the trailers route reads the calling user's claims, which an API key does not have (a NullReferenceException in GetIsApiKey), even with a userId"},

	// the GETs that answer, with nothing in it, on a fresh server and the
	// fixtures: what they read is not there to read
	"DiscoverTuners":             {Empty: jfNoTuner},
	"DiscvoverTuners":            {Empty: jfNoTuner},
	"GetLiveTvChannels":          {Empty: jfNoTuner},
	"GetLiveTvPrograms":          {Empty: jfNoTuner},
	"GetRecommendedPrograms":     {Empty: jfNoTuner},
	"GetRecordings":              {Empty: jfNoTuner},
	"GetSeriesTimers":            {Empty: jfNoTuner},
	"GetTimers":                  {Empty: jfNoTuner},
	"GetAllChannelFeatures":      {Empty: jfNoChannel},
	"GetChannels":                {Empty: jfNoChannel},
	"GetLatestChannelItems":      {Empty: jfNoChannel},
	"ListBackups":                {Empty: "no backup has been made"},
	"GetBrandingCss":             {Empty: jfNoBranding},
	"GetBrandingCss2":            {Empty: jfNoBranding},
	"GetBrandingOptions":         {Empty: jfNoBranding},
	"GetDefaultDirectoryBrowser": {Empty: "the default browser path is empty on Linux"},
	"GetDefaultMetadataOptions":  {Empty: "the defaults a new item type starts from are blank"},
	"GetFallbackFontList":        {Empty: "no fallback font folder is set"},
	"GetPublicUsers":             {Empty: "every user is hidden from the login screen (the users test shows one and hides her again)"},
	"GetResumeItems":             {Empty: "nothing is in progress (the played and resume test clears the position it sets)"},
	"GetNextUp":                  {Empty: "no episode is played (the shows test unmarks the one it marks)"},
	"GetUpcomingEpisodes":        {Empty: "the show library has its fetchers off, so no episode is known to be coming"},
	"GetMovieRecommendations":    {Empty: "no film is watched or liked, which recommendations start from"},
	"SearchRemoteSubtitles":      {Empty: "no subtitle provider is installed"},
	"GetItemSegments":            {Empty: "no media segment provider is installed, so no film has an intro or credits marked"},
	"GetIntros":                  {Empty: jfNoExtras},
	"GetLocalTrailers":           {Empty: jfNoExtras},
	"GetSpecialFeatures":         {Empty: jfNoExtras},
	"GetAdditionalPart":          {Empty: jfNoExtras},
}

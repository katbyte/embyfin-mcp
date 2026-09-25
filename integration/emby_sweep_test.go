//go:build integration

package integration

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/emby"
)

// TestEmbyReadSweep calls every Emby GET against the fixtures (see
// sweep_test.go).
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyReadSweep(t *testing.T) {
	ctx := skipUnlessEmby(t)
	moviesID := embyLibrary(t, sdkMovies)
	showsID := embyLibrary(t, sdkShows)
	musicID := embyLibrary(t, sdkMusic)

	movie := embyMovie(t, alien)
	item := must(embyc.GetUsersByUserIdItemsById(ctx, adminID, movie.Id)).Model
	album := embyFirst(t, musicID, "MusicAlbum")
	song := embyFirst(t, musicID, "Audio")
	musicArtists := must(embyc.GetArtists(ctx, emby.GetArtistsOperationOptions{ParentId: musicID, UserId: adminID})).Model.Items
	musicGenres := must(embyc.GetMusicGenres(ctx, emby.GetMusicGenresOperationOptions{ParentId: musicID, UserId: adminID})).Model.Items
	if len(musicArtists) == 0 || len(musicGenres) == 0 {
		t.Fatalf("the music fixtures are incomplete: %d artists, %d genres", len(musicArtists), len(musicGenres))
	}
	series := embySeries(t, severance)
	people := must(embyc.GetPersons(ctx, emby.GetPersonsOperationOptions{NameStartsWith: ridleyScott})).Model.Items
	studios := must(embyc.GetStudios(ctx, emby.GetStudiosOperationOptions{ParentId: moviesID, Recursive: new(true)})).Model.Items
	logs := must(embyc.GetSystemLogsQuery(ctx, emby.GetSystemLogsQueryOperationOptions{})).Model.Items
	plugins := must(embyc.GetPlugins(ctx)).Model
	sessions := must(embyc.GetSessions(ctx, emby.GetSessionsOperationOptions{})).Model
	devices := must(embyc.GetDevices(ctx, emby.GetDevicesOperationOptions{})).Model.Items
	if len(people) == 0 || len(studios) == 0 || len(logs) == 0 || len(plugins) == 0 || len(sessions) == 0 || len(devices) == 0 || len(item.MediaSources) == 0 {
		t.Fatalf("the fixtures are incomplete: %d people, %d studios, %d logs, %d plugins, %d sessions, %d devices, %d media sources",
			len(people), len(studios), len(logs), len(plugins), len(sessions), len(devices), len(item.MediaSources))
	}

	director := people[slices.IndexFunc(people, func(p emby.BaseItemDto) bool { return p.Name == ridleyScott })]
	// the Webhooks plugin Emby bundles is the one notification service, and
	// has strings for its settings page
	webhooks := slices.IndexFunc(plugins, func(p emby.PluginsPluginInfo) bool { return p.Name == "Webhooks" })
	if webhooks < 0 {
		t.Fatalf("no Webhooks plugin among %+v", plugins)
	}
	// the film with a subtitle, for the routes that hand one out
	subtitled := must(embyc.GetUsersByUserIdItemsById(ctx, adminID, embyMovie(t, thirteenthFloor).Id)).Model
	if len(subtitled.MediaSources) == 0 {
		t.Fatalf("%s has no media source", thirteenthFloor)
	}
	srt := slices.IndexFunc(subtitled.MediaSources[0].MediaStreams, func(s emby.MediaStream) bool { return s.Type == "Subtitle" })
	if srt < 0 {
		t.Fatalf("%s lists no subtitle stream: %+v", thirteenthFloor, subtitled.MediaSources[0].MediaStreams)
	}
	subtitle := map[string]string{
		"Id":            subtitled.Id,
		"MediaSourceId": subtitled.MediaSources[0].Id,
		"Index":         strconv.Itoa(subtitled.MediaSources[0].MediaStreams[srt].Index),
		"Format":        "srt",
	}
	// the catalogue the package routes read, once the server will fetch it
	packages := embyPackages(ctx, t)
	// the Live TV folder answers 500 (a null reference as it attaches the
	// folder's people) until something has read the channel manager, and
	// with the folder from then on: whatever the sweep's order, the manager
	// is read first
	if _, err := embyc.GetLiveTvManageChannels(ctx, emby.GetLiveTvManageChannelsOperationOptions{}); err != nil {
		t.Fatal(err)
	}

	// a playlist to read
	var playlist string
	if err := embyRetry500(func() error {
		res, err := embyc.PostPlaylists(ctx, emby.PostPlaylistsOperationOptions{Name: "SDK Sweep", Ids: movie.Id, MediaType: "Video", UserId: adminID})
		if err == nil {
			playlist = res.Model.Id
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = embyc.DeleteItemsById(context.WithoutCancel(ctx), playlist) })

	// and a collection
	var collection string
	if err := embyRetry500(func() error {
		res, err := embyc.PostCollections(ctx, emby.PostCollectionsOperationOptions{Name: "SDK Sweep", Ids: movie.Id})
		if err == nil && res.Model != nil {
			collection = res.Model.Id
		}
		return err
	}); err != nil || collection == "" {
		t.Fatalf("PostCollections = %q, %v", collection, err)
	}
	t.Cleanup(func() { _, _ = embyc.DeleteItemsById(context.WithoutCancel(ctx), collection) })
	sections := must(embyc.GetUsersByUserIdHomeSections(ctx, adminID)).Model
	if len(sections) == 0 {
		t.Fatal("GetUsersByUserIdHomeSections listed nothing")
	}

	fixtures := sweepFixtures{
		path: map[string]string{
			"Id":                    movie.Id,
			"ItemId":                movie.Id,
			"UserId":                adminID,
			"Users/Id":              adminID,
			"Sessions/Id":           sessions[0].Id,
			"ScheduledTasks/Id":     embyScanTask(ctx, t).Id,
			"Plugins/Id":            plugins[0].Id,
			"Playlists/Id":          playlist,
			"Collections/Id":        collection,
			"Sections/SectionId":    sections[0].Id,
			"Logs/Name":             logs[0].Name,
			"StreamFileName":        "stream.mp4",
			"Shows/Id":              series.Id,
			"DisplayPreferences/Id": "usersettings",
			"Configuration/Key":     "encoding",
			"Persons/Name":          director.Name,
			"Persons/Id":            director.Id,
			"Genres/Name":           "Science Fiction",
			"Albums/Id":             album.Id,
			"Artists/Id":            musicArtists[0].Id,
			"Artists/Name":          musicArtists[0].Name,
			"MusicGenres/Name":      musicGenres[0].Name,
			"Songs/Id":              song.Id,
			"Audio/Id":              song.Id,
			"Studios/Name":          studios[0].Name,
			"Type":                  "Primary",
			"Index":                 "0",
			"Tag":                   item.ImageTags["Primary"],
			"Format":                "jpg",
			"MaxWidth":              "100",
			"MaxHeight":             "100",
			"PercentPlayed":         "0",
			"UnplayedCount":         "0",
			"Container":             "mp4",
			"MediaSourceId":         item.MediaSources[0].Id,
			"Language":              "eng",
			"StartPositionTicks":    "0",
		},
		options: map[string]string{
			"UserId":                 adminID,
			"Name":                   logs[0].Name,
			"Client":                 "emby",
			"Path":                   "/media",
			"Container":              "mp4",
			"Width":                  "320",
			"SearchTerm":             alien,
			"IncludeExternalContent": "false",
			"Id":                     devices[0].Id,
			"DeviceId":               devices[0].Id,
			"PackageType":            "UserInstalled",
			"Size":                   "1024",
			"SubtitleSegmentLength":  "30",
			"ManifestSubtitles":      "vtt",
			"MediaSourceId":          item.MediaSources[0].Id,
			"Ids":                    movie.Id,
			"CodecId":                "V-E-libx264",
			"ParameterContext":       "Playback",
			"TargetId":               "originalmediafolder",
		},
	}

	cases := maps.Clone(embySweepCases)
	// the similar-item routes need a user (a 500 without one)
	for _, name := range []string{"GetItemsByIdSimilar", "GetMoviesByIdSimilar", "GetTrailersByIdSimilar"} {
		cases[name] = sweepCase{Options: map[string]any{"UserId": adminID}}
	}
	cases["GetShowsByIdSimilar"] = sweepCase{Options: map[string]any{"UserId": adminID}, Empty: "Emby finds no show like another among the fixtures, two of which are dramas"}
	// an album is like another by its artist's other album, and a mix is
	// made from music
	cases["GetAlbumsByIdSimilar"] = sweepCase{Path: map[string]string{"Id": embyAlbum(t, musicID, darkSide).Id}, Options: map[string]any{"UserId": adminID}}
	cases["GetItemsByIdInstantMix"] = sweepCase{Path: map[string]string{"Id": album.Id}}
	// the by-name lists answer for a library read recursively: without one
	// they list the names empty ({} for each), and /Trailers lists the
	// user's libraries rather than nothing
	for _, name := range []string{"GetOfficialRatings", "GetContainers", "GetYears"} {
		cases[name] = sweepCase{Options: map[string]any{"ParentId": moviesID, "Recursive": true}}
	}
	cases["GetTags"] = sweepCase{Options: map[string]any{"ParentId": showsID, "Recursive": true}, Empty: "nothing tags a show (Emby does not fill tags from TMDB, and the tag tests tag films)"}
	cases["GetTrailers"] = sweepCase{Options: map[string]any{"UserId": adminID}}
	cases["GetDlnaProfilesById"] = sweepCase{Path: map[string]string{"Id": must(embyc.GetDlnaProfileInfos(ctx)).Model[0].Id}}
	cases["GetEnvironmentDirectoryContents"] = sweepCase{Options: map[string]any{"IncludeDirectories": true}}
	cases["GetMoviesRecommendations"] = sweepCase{Options: map[string]any{"UserId": adminID}}
	cases["GetVideosByIdStream"] = sweepCase{Options: map[string]any{"Container": "mp4", "Static": true}}
	cases["GetVideosByIdStreamByContainer"] = sweepCase{Options: map[string]any{"Static": true}}
	cases["GetVideosByIdByStreamFileName"] = sweepCase{Options: map[string]any{"Static": true}}
	cases["GetAudioByIdStream"] = sweepCase{Options: map[string]any{"Container": "mp3", "Static": true}}
	cases["GetAudioByIdStreamByContainer"] = sweepCase{Path: map[string]string{"Container": "mp3"}, Options: map[string]any{"Static": true}}
	cases["GetAudioByIdByStreamFileName"] = sweepCase{Path: map[string]string{"StreamFileName": "stream.mp3"}, Options: map[string]any{"Static": true}}
	// universal audio needs a user, the instant mixes the artist or genre they
	// are made from (UserId and Id, which the emby-undeclared-query workaround
	// declares)
	cases["GetAudioByIdUniversal"] = sweepCase{Options: map[string]any{"UserId": adminID}}
	cases["GetAudioByIdUniversalByContainer"] = sweepCase{Path: map[string]string{"Container": "mp3"}, Options: map[string]any{"UserId": adminID}}
	cases["GetArtistsInstantMix"] = sweepCase{Options: map[string]any{"Id": musicArtists[0].Id}}
	cases["GetMusicGenresInstantMix"] = sweepCase{Options: map[string]any{"Id": musicGenres[0].Id}}
	// the subtitle routes read the film with one, and the HLS subtitle
	// playlist its media source (MediaSourceId, which the workaround declares)
	for _, name := range []string{
		"GetItemsByIdByMediaSourceIdSubtitlesByIndexStreamByFormat", "GetItemsByIdByMediaSourceIdSubtitlesByIndexByStartPositionTicksStreamByFormat",
		"GetVideosByIdByMediaSourceIdSubtitlesByIndexStreamByFormat", "GetVideosByIdByMediaSourceIdSubtitlesByIndexByStartPositionTicksStreamByFormat",
	} {
		cases[name] = sweepCase{Path: subtitle}
	}
	cases["GetVideosByIdSubtitlesM3u8"] = sweepCase{Path: subtitle, Options: map[string]any{"MediaSourceId": subtitle["MediaSourceId"]}}
	// a package from the catalogue, and the web strings of the Webhooks
	// plugin in the language the wizard chose
	cases["GetPackagesByName"] = sweepCase{Path: map[string]string{"Name": packages[0].Name}}
	cases["GetWebStrings"] = sweepCase{Options: map[string]any{"PluginId": plugins[webhooks].Id, "Locale": "en-US"}}
	cases["GetWebStringset"] = sweepCase{Options: map[string]any{"PluginId": plugins[webhooks].Id}}
	// its notifier is keyed by the id GET /Notifications/Services lists, a
	// route the document leaves out
	cases["GetNotificationsServicesDefaults"] = sweepCase{Options: map[string]any{"NotifierKey": "webhooknotifications", "UserId": adminID}}
	// a playlist's sharing is answered only to a user session
	cases["GetUsersItemAccess"] = sweepCase{Status: 400, Options: map[string]any{"ItemId": playlist}, Why: embyNoUserOfKey}

	sweep(t, "emby", embyc, fixtures, cases)
}

// Why an Emby GET does not simply answer in the sweep.
const (
	embyNoTuner     = "Live TV needs a tuner and a guide provider; the container has neither"
	embyNoDLNA      = "DLNA needs a client on the network to describe; nothing answers SSDP in docker"
	embyNoSync      = "Emby Sync needs a sync target device and a job; the container has neither"
	embyNoTranscode = "HLS segments and live streams need a transcoding session to address; the sweep reads files, not streams"
	embyNoGames     = "games are gone from Emby 4.10"
	embyNoImage     = "the fixture has no image of that type, which the server answers with an error"
	embyRemoteImage = "fetches an arbitrary image URL through the server, which the cassettes hold no image for; the provider tests cover the remote image search (GetItemsByIdRemoteImages)"
	embyNoConnect   = "Emby Connect is not linked"
	embyNoSubtitle  = "needs a subtitle id from a provider search, and no subtitle provider is configured"
	embyNoUserOfKey = "an API key acts as no user, and the server needs one for this (a user token would answer)"
	embyNoParty     = "no watch party is running"
	embyNoBranding  = "no branding is set on a fresh server"
	embyNoExtras    = "the fixture films have no intros, trailers, extras or additional parts"
	// the proxy stores an answer this size as a placeholder, and serves the
	// placeholder when it records one as well as when it replays it
	embyReleaseNotes = "the release notes come from GitHub, whose answer is too large for a cassette: the provider proxy hands the server a placeholder in every mode, which it cannot read"
)

// embySweepCases classifies the Emby GETs that do not simply answer.
var embySweepCases = map[string]sweepCase{
	// Live TV
	"GetLiveTvChannelsById":                         {Skip: embyNoTuner},
	"GetLiveTvProgramsById":                         {Skip: embyNoTuner},
	"GetLiveTvRecordingsById":                       {Skip: embyNoTuner},
	"GetLiveTvSeriesTimersById":                     {Skip: embyNoTuner},
	"GetLiveTvTimersById":                           {Skip: embyNoTuner},
	"GetLiveTvChannelMappingOptions":                {Skip: embyNoTuner},
	"GetLiveTvChannelMappings":                      {Skip: embyNoTuner},
	"GetLiveTvListingProviders":                     {Skip: embyNoTuner + " (it takes a channel id)"},
	"GetLiveTvListingProvidersLineups":              {Status: 404, Why: embyNoTuner},
	"GetLiveTvLiveRecordingsByIdStream":             {Skip: embyNoTuner},
	"GetLiveTvLiveRecordingsByIdHlsBySegment":       {Skip: embyNoTuner},
	"GetLiveTvLiveRecordingsByIdHlsLiveM3u8":        {Status: 404, Why: embyNoTuner},
	"GetLiveTvLiveRecordingsByIdHlsMasterM3u8":      {Status: 404, Why: embyNoTuner},
	"GetLiveTvLiveStreamFilesByIdStreamByContainer": {Skip: embyNoTuner},
	"GetLiveTvLiveStreamFilesByIdHlsBySegment":      {Skip: embyNoTuner},
	"GetLiveTvLiveStreamFilesByIdHlsLiveM3u8":       {Status: 404, Why: embyNoTuner},
	"GetLiveTvLiveStreamFilesByIdHlsMasterM3u8":     {Status: 404, Why: embyNoTuner},
	"GetLiveTvTunerHostsDefaultByType":              {Path: map[string]string{"Type": "hdhomerun"}},

	// DLNA
	"GetDlnaByUuIdConnectionmanagerConnectionmanager":    {Skip: embyNoDLNA},
	"GetDlnaByUuIdConnectionmanagerConnectionmanagerXml": {Skip: embyNoDLNA},
	"GetDlnaByUuIdContentdirectoryContentdirectory":      {Skip: embyNoDLNA},
	"GetDlnaByUuIdContentdirectoryContentdirectoryXml":   {Skip: embyNoDLNA},
	"GetDlnaByUuIdDescription":                           {Skip: embyNoDLNA},
	"GetDlnaByUuIdDescriptionXml":                        {Skip: embyNoDLNA},
	"GetDlnaByUuIdIconsByFilename":                       {Skip: embyNoDLNA},
	"GetDlnaIconsByFilename":                             {Skip: embyNoDLNA},

	// music: the SDK Music library answers the rest (see TestEmbyReadSweep)
	"GetArtistsByNameImagesByType":                              {Status: 404, Why: embyNoImage},
	"GetArtistsByNameImagesByTypeByIndex":                       {Status: 404, Why: embyNoImage},
	"GetMusicGenresByNameImagesByType":                          {Status: 404, Why: embyNoImage},
	"GetMusicGenresByNameImagesByTypeByIndex":                   {Status: 404, Why: embyNoImage},
	"GetAudioByIdHlsByPlaylistIdBySegmentIdBySegmentContainer":  {Skip: embyNoTranscode},
	"GetAudioByIdHls1ByPlaylistIdBySegmentIdBySegmentContainer": {Skip: embyNoTranscode},
	"GetAudioByIdLiveM3u8":                                      {Skip: embyNoTranscode + " (a live playlist waits on ffmpeg)"},

	// games
	"GetGameGenresByName":                    {Skip: embyNoGames},
	"GetGameGenresByNameImagesByType":        {Skip: embyNoGames},
	"GetGameGenresByNameImagesByTypeByIndex": {Skip: embyNoGames},
	"GetGamesByIdSimilar":                    {Status: 500, Why: embyNoGames},

	// Sync
	"GetSyncItemsReady":                  {Skip: embyNoSync},
	"GetSyncJobItemsByIdAdditionalFiles": {Status: 500, Why: embyNoSync},
	"GetSyncJobItemsByIdFile":            {Status: 404, Why: embyNoSync},
	"GetSyncOptions":                     {Status: 500, Why: embyNoSync},

	// streaming
	"GetVideosByIdHlsByPlaylistIdBySegmentIdBySegmentContainer":  {Skip: embyNoTranscode},
	"GetVideosByIdHls1ByPlaylistIdBySegmentIdBySegmentContainer": {Skip: embyNoTranscode},
	"GetVideosByIdLiveM3u8":                                {Skip: embyNoTranscode + " (a live playlist waits on ffmpeg)"},
	"GetVideosByIdLiveSubtitlesM3u8":                       {Status: 404, Why: embyNoTranscode + " (a live subtitle playlist is a running job's: Transcoding Job could not be found)"},
	"GetVideosByIdByMediaSourceIdAttachmentsByIndexStream": {Status: 500, Why: "the fixture videos carry no attachments"},
	"GetEnvironmentNetworkShares":                          {Skip: "lists the SMB shares of a host address; there is no SMB server on the docker network"},

	// fixtures the container lacks
	"GetStudiosByNameImagesByType":        {Status: 404, Why: embyNoImage},
	"GetStudiosByNameImagesByTypeByIndex": {Status: 404, Why: embyNoImage},
	"GetUsersByIdImagesByType":            {Status: 404, Why: embyNoImage},
	"GetUsersByIdImagesByTypeByIndex":     {Status: 404, Why: embyNoImage},
	"GetImagesRemote":                     {Skip: embyRemoteImage},
	"GetItemsRemoteSearchImage":           {Skip: embyRemoteImage},
	"GetProvidersSubtitlesSubtitlesById":  {Status: 500, Why: embyNoSubtitle},
	"GetConnectExchange":                  {Skip: embyNoConnect},
	"GetItemsByIdDeleteInfo":              {Status: 400, Why: embyNoUserOfKey},
	"GetUIView":                           {Status: 400, Options: map[string]any{"PageId": "home", "ClientLocale": "en-US"}, Why: embyNoUserOfKey + ", and a page id the web client knows"},
	"GetWebConfigurationPage":             {Status: 404, Why: "no plugin configuration page is named; the route answers 404 without one"},
	"GetSystemReleaseNotes":               {Status: 500, Why: embyReleaseNotes},
	"GetSystemReleaseNotesVersions":       {Status: 500, Why: embyReleaseNotes},

	// the server's routes
	"GetUsersByUserIdTypedSettingsByKey": {Status: 500, Path: map[string]string{"Key": "home"}, Why: "the typed settings keys are undocumented, and the server answers 500 for one it does not have"},

	// the GETs that answer, with nothing in it, on a fresh server and the
	// fixtures: what they read is not there to read
	"GetLiveTvInfo":                               {Empty: embyNoTuner},
	"GetLiveTvChannelTags":                        {Empty: embyNoTuner},
	"GetLiveTvChannelTagsPrefixes":                {Empty: embyNoTuner},
	"GetLiveTvChannels":                           {Empty: embyNoTuner},
	"GetLiveTvEPG":                                {Empty: embyNoTuner},
	"GetLiveTvManageChannels":                     {Empty: embyNoTuner},
	"GetLiveTvPrograms":                           {Empty: embyNoTuner},
	"GetLiveTvProgramsRecommended":                {Empty: embyNoTuner},
	"GetLiveTvRecordings":                         {Empty: embyNoTuner},
	"GetLiveTvRecordingsFolders":                  {Empty: embyNoTuner},
	"GetLiveTvRecordingsGroups":                   {Empty: embyNoTuner},
	"GetLiveTvRecordingsSeries":                   {Empty: embyNoTuner},
	"GetLiveTvSeriesTimers":                       {Empty: embyNoTuner},
	"GetLiveTvTimers":                             {Empty: embyNoTuner},
	"GetLiveTvTunerHosts":                         {Empty: embyNoTuner},
	"GetLiveTvTunersDiscover":                     {Empty: embyNoTuner},
	"GetLiveTvTunersDiscvover":                    {Empty: embyNoTuner},
	"GetSyncJobItems":                             {Empty: embyNoSync},
	"GetSyncJobs":                                 {Empty: embyNoSync},
	"GetSyncJobsById":                             {Empty: embyNoSync},
	"GetParties":                                  {Empty: embyNoParty},
	"GetPartiesInfo":                              {Empty: embyNoParty},
	"GetPartiesMessages":                          {Empty: embyNoParty},
	"GetConnectPending":                           {Empty: embyNoConnect},
	"GetGameGenres":                               {Empty: embyNoGames},
	"GetChannels":                                 {Empty: "no channel plugin is installed"},
	"GetAudioBooksNextUp":                         {Empty: "there is no audiobook library"},
	"GetBackupRestoreBackupInfo":                  {Empty: "no backup has been made"},
	"GetItemsByIdThumbnailSet":                    {Empty: "no thumbnail images are extracted from the one-second fixtures"},
	"GetBrandingConfiguration":                    {Empty: embyNoBranding},
	"GetBrandingCss":                              {Empty: embyNoBranding},
	"GetBrandingCssCss":                           {Empty: embyNoBranding},
	"GetDevicesOptions":                           {Empty: "no options are stored for the device until a client sets them"},
	"GetUserSettingsByUserId":                     {Empty: "a user has no settings stored until a client saves some"},
	"GetEncodingCodecConfigurationDefaults":       {Empty: "no codec has defaults stored on a fresh server ({} for each of the five)"},
	"GetEnvironmentDefaultDirectoryBrowser":       {Empty: "the default browser path is empty on Linux"},
	"GetEnvironmentNetworkDevices":                {Empty: "nothing on the docker network announces itself"},
	"GetPluginsByIdConfiguration":                 {Empty: "the one plugin whose settings this route reads (the rest answer 500) has every one at its default, false or 0"},
	"GetSessionsPlayQueue":                        {Empty: "nothing is playing"},
	"GetUsersByUserIdItemsResume":                 {Empty: "nothing is in progress (the played and resume test clears the position it sets)"},
	"GetShowsNextUp":                              {Empty: "no episode is played (the shows test unmarks the one it marks)"},
	"GetShowsMissing":                             {Empty: "the show library has its fetchers off, so no episode is known to be missing"},
	"GetShowsUpcoming":                            {Empty: "the show library has its fetchers off, so no episode is known to be coming"},
	"GetCollectionsByIdMissing":                   {Empty: "the sweep's collection is none of TMDB's, so it has no parts to be missing"},
	"GetCollectionsByIdProviderItems":             {Empty: "the sweep's collection is none of TMDB's, so it has no parts to list"},
	"GetPlaylistsByIdInstantMix":                  {Empty: "Emby makes no mix from a playlist, even one of music"},
	"GetArtistsByIdSimilar":                       {Empty: "Emby finds no artist like another among the fixtures' four, though two share a genre"},
	"GetStreamLanguages":                          {Empty: "no fixture stream names a language (ffmpeg writes und), and the English .srt beside one film is not counted"},
	"GetItemsByIdCriticReviews":                   {Empty: "the fetchers fill in no critic reviews"},
	"GetItemsByIdRemoteSearchSubtitlesByLanguage": {Empty: embyNoSubtitle},
	"GetItemsIntros":                              {Empty: embyNoExtras},
	"GetUsersByUserIdItemsByIdIntros":             {Empty: embyNoExtras},
	"GetUsersByUserIdItemsByIdLocalTrailers":      {Empty: embyNoExtras},
	"GetUsersByUserIdItemsByIdSpecialFeatures":    {Empty: embyNoExtras},
	"GetVideosByIdAdditionalParts":                {Empty: embyNoExtras},
}

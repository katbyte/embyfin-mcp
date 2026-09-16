//go:build integration

package integration

import (
	"context"
	"maps"
	"slices"
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
	embyLibrary(t, sdkShows)

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
	for _, name := range []string{"GetItemsByIdSimilar", "GetMoviesByIdSimilar", "GetShowsByIdSimilar", "GetTrailersByIdSimilar"} {
		cases[name] = sweepCase{Options: map[string]any{"UserId": adminID}}
	}
	cases["GetMoviesRecommendations"] = sweepCase{Options: map[string]any{"UserId": adminID}}
	cases["GetVideosByIdStream"] = sweepCase{Options: map[string]any{"Container": "mp4", "Static": true}}
	cases["GetVideosByIdStreamByContainer"] = sweepCase{Options: map[string]any{"Static": true}}
	cases["GetVideosByIdByStreamFileName"] = sweepCase{Options: map[string]any{"Static": true}}
	cases["GetAudioByIdStream"] = sweepCase{Options: map[string]any{"Container": "mp3", "Static": true}}
	cases["GetAudioByIdStreamByContainer"] = sweepCase{Path: map[string]string{"Container": "mp3"}, Options: map[string]any{"Static": true}}
	cases["GetAudioByIdByStreamFileName"] = sweepCase{Path: map[string]string{"StreamFileName": "stream.mp3"}, Options: map[string]any{"Static": true}}
	cases["GetAudioByIdUniversalByContainer"] = sweepCase{Path: map[string]string{"Container": "mp3"}, Status: 500, Why: embyUndeclared + " (it answers 200 the moment a UserId is added by hand)"}

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
	embyRemoteImage = "fetches an arbitrary image URL through the server; the provider tests cover remote images"
	embyNoConnect   = "Emby Connect is not linked"
	embyNoSubtitle  = "needs a subtitle id from a provider search, and no subtitle provider is configured"
	embyNoUserOfKey = "an API key acts as no user, and the server needs one for this (a user token would answer)"
	embyUndeclared  = "answers 500 to an API key and to a user session alike (a null reference or an empty lookup): the route needs input its document does not declare"
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
	"GetLiveTvFolder":                               {Status: 500, Sometimes: true, Why: embyNoTuner + " (the folder is made on first use, so a later call answers)"},
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
	"GetArtistsInstantMix":                                      {Status: 500, Why: embyUndeclared + " (the mix is seeded by an Id the route does not declare: /Artists/InstantMix?Id=<artist> answers 200)"},
	"GetMusicGenresInstantMix":                                  {Status: 500, Why: embyUndeclared + " (the same Id, undeclared: /MusicGenres/InstantMix?Id=<genre> answers 200)"},
	"GetAudioByIdUniversal":                                     {Status: 500, Why: embyUndeclared + " (it answers 200 the moment a UserId is added by hand; only DeviceId and StartTimeTicks are declared)"},
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
	"GetVideosByIdSubtitlesM3u8":                           {Status: 500, Why: "the fixture videos carry no subtitle stream to segment"},
	"GetVideosByIdLiveSubtitlesM3u8":                       {Status: 404, Why: "the fixture videos carry no subtitle stream to segment"},
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
	"GetPackagesByName":                   {Skip: "the package catalogue comes from Emby's servers through the provider proxy; GetPackages covers it"},
	"GetPackages":                         {Status: 500, Sometimes: true, Why: "the catalogue is fetched from mb3admin.com through the provider proxy, and the server gives up on a slow fetch with a 500"},
	"GetPackagesUpdates":                  {Status: 500, Sometimes: true, Why: "the updates are read from the same catalogue, and a fetch the server gave up on fails them too"},
	"GetSystemReleaseNotes":               {Status: 500, Sometimes: true, Why: "the release notes come from GitHub through the provider proxy, whose cassette elides the oversized answer"},
	"GetSystemReleaseNotesVersions":       {Status: 500, Sometimes: true, Why: "the release notes come from GitHub through the provider proxy, whose cassette elides the oversized answer"},

	// the server's routes
	"GetUsersItemAccess":                 {Status: 500, Why: embyUndeclared},
	"GetNotificationsServicesDefaults":   {Status: 500, Why: embyUndeclared},
	"GetWebStrings":                      {Status: 500, Why: embyUndeclared},
	"GetWebStringset":                    {Status: 500, Why: embyUndeclared},
	"GetUsersByUserIdTypedSettingsByKey": {Status: 500, Path: map[string]string{"Key": "home"}, Why: "the typed settings keys are undocumented, and the server answers 500 for one it does not have"},
}

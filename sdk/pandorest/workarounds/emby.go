package workarounds

import (
	"errors"
	"fmt"
	"net/http"

	pandorest "github.com/katbyte/pandorest/importer/workarounds"
	"github.com/katbyte/pandorest/openapi"
)

// The Emby document is the one Emby Server 4.10 serves itself at
// /emby/openapi.json (swagger.emby.media still serves 4.1.1). What follows was
// found by the integration suite's read sweep and bespoke tests against that
// server.

const emby = "emby"

// queryResult is the model most of Emby's lists come in.
const queryResult = "QueryResult_BaseItemDto"

// What the GETs without a declared answer give: JSON of no declared shape, or
// a file of a kind.
var (
	anyJSON  = pandorest.JSON()
	image    = pandorest.File("image/*")
	audio    = pandorest.File("audio/*")
	video    = pandorest.File("video/*")
	subtitle = pandorest.File("text/*")
	xml      = pandorest.File("text/xml")
	download = pandorest.File("application/octet-stream")
	playlist = pandorest.File("application/x-mpegURL")
)

// embyUndeclaredResponses declares what the GETs without a response schema
// answer: JSON, the model the JSON decodes into, a file, or, for the one that
// is a word or two, text read whole.
var embyUndeclaredResponses = pandorest.UndeclaredAnswers(pandorest.About{Name: "emby-undeclared-responses", Service: emby, Bug: "about 95 GETs declare a 200 with no content, so nothing says whether they answer JSON (and in what shape) or a file"}, map[string]pandorest.Answer{
	// JSON
	"GET /Auth/Keys":                          anyJSON,
	"GET /Collections/{Id}/Missing":           pandorest.Model(queryResult),
	"GET /Collections/{Id}/ProviderItems":     anyJSON,
	"GET /Connect/Pending":                    anyJSON,
	"GET /LiveTv/ChannelMappingOptions":       anyJSON,
	"GET /LiveTv/ChannelMappings":             anyJSON,
	"GET /LiveTv/Programs":                    pandorest.Model(queryResult),
	"GET /LiveTv/Recordings":                  pandorest.Model(queryResult),
	"GET /Parties":                            anyJSON,
	"GET /Plugins/{Id}/Configuration":         anyJSON,
	"GET /Shows/Missing":                      pandorest.Model(queryResult),
	"GET /Shows/Upcoming":                     pandorest.Model(queryResult),
	"GET /Shows/{Id}/Episodes":                pandorest.Model(queryResult),
	"GET /System/Configuration/{Key}":         anyJSON,
	"GET /Users/{UserId}/TypedSettings/{Key}": anyJSON,
	"GET /web/strings":                        anyJSON,

	// text
	"GET /Branding/Css":          pandorest.File("text/css"),
	"GET /Branding/Css.css":      pandorest.File("text/css"),
	"GET /System/Logs/{Name}":    pandorest.File("text/plain"),
	"GET /System/Ping":           pandorest.Text("text/plain"),
	"GET /web/ConfigurationPage": pandorest.File("text/html"),

	// images
	"GET /Artists/{Name}/Images/{Type}":            image,
	"GET /Artists/{Name}/Images/{Type}/{Index}":    image,
	"GET /Dlna/icons/{Filename}":                   image,
	"GET /Dlna/{UuId}/icons/{Filename}":            image,
	"GET /GameGenres/{Name}/Images/{Type}":         image,
	"GET /GameGenres/{Name}/Images/{Type}/{Index}": image,
	"GET /Genres/{Name}/Images/{Type}":             image,
	"GET /Genres/{Name}/Images/{Type}/{Index}":     image,
	"GET /Images/Remote":                           image,
	"GET /Items/RemoteSearch/Image":                image,
	"GET /Items/{Id}/Images/{Type}":                image,
	"GET /Items/{Id}/Images/{Type}/{Index}":        image,
	"GET /Items/{Id}/Images/{Type}/{Index}/{Tag}/{Format}/{MaxWidth}/{MaxHeight}/{PercentPlayed}/{UnPlayedCount}": image,
	"GET /MusicGenres/{Name}/Images/{Type}":         image,
	"GET /MusicGenres/{Name}/Images/{Type}/{Index}": image,
	"GET /Persons/{Name}/Images/{Type}":             image,
	"GET /Persons/{Name}/Images/{Type}/{Index}":     image,
	"GET /Plugins/{Id}/Thumb":                       image,
	"GET /Studios/{Name}/Images/{Type}":             image,
	"GET /Studios/{Name}/Images/{Type}/{Index}":     image,
	"GET /Users/{Id}/Images/{Type}":                 image,
	"GET /Users/{Id}/Images/{Type}/{Index}":         image,

	// media
	"GET /Audio/{Id}/{StreamFileName}":                    audio,
	"GET /Audio/{Id}/stream":                              audio,
	"GET /Audio/{Id}/stream.{Container}":                  audio,
	"GET /Audio/{Id}/universal":                           audio,
	"GET /Audio/{Id}/universal.{Container}":               audio,
	"GET /LiveTv/LiveRecordings/{Id}/stream":              video,
	"GET /LiveTv/LiveStreamFiles/{Id}/stream.{Container}": video,
	"GET /Videos/{Id}/{StreamFileName}":                   video,
	"GET /Videos/{Id}/stream":                             video,
	"GET /Videos/{Id}/stream.{Container}":                 video,

	// HLS playlists and segments
	"GET /Audio/{Id}/live.m3u8":                                         playlist,
	"GET /Audio/{Id}/main.m3u8":                                         playlist,
	"GET /Audio/{Id}/master.m3u8":                                       playlist,
	"GET /LiveTv/LiveRecordings/{Id}/hls/live.m3u8":                     playlist,
	"GET /LiveTv/LiveRecordings/{Id}/hls/master.m3u8":                   playlist,
	"GET /LiveTv/LiveStreamFiles/{Id}/hls/live.m3u8":                    playlist,
	"GET /LiveTv/LiveStreamFiles/{Id}/hls/master.m3u8":                  playlist,
	"GET /Videos/{Id}/live.m3u8":                                        playlist,
	"GET /Videos/{Id}/live_subtitles.m3u8":                              playlist,
	"GET /Videos/{Id}/main.m3u8":                                        playlist,
	"GET /Videos/{Id}/master.m3u8":                                      playlist,
	"GET /Videos/{Id}/subtitles.m3u8":                                   playlist,
	"GET /Audio/{Id}/hls/{PlaylistId}/{SegmentId}.{SegmentContainer}":   download,
	"GET /Audio/{Id}/hls1/{PlaylistId}/{SegmentId}.{SegmentContainer}":  download,
	"GET /LiveTv/LiveRecordings/{Id}/hls/{Segment}":                     download,
	"GET /LiveTv/LiveStreamFiles/{Id}/hls/{Segment}":                    download,
	"GET /Videos/{Id}/hls/{PlaylistId}/{SegmentId}.{SegmentContainer}":  download,
	"GET /Videos/{Id}/hls1/{PlaylistId}/{SegmentId}.{SegmentContainer}": download,

	// subtitles and attachments
	"GET /Items/{Id}/{MediaSourceId}/Subtitles/{Index}/Stream.{Format}":                       subtitle,
	"GET /Items/{Id}/{MediaSourceId}/Subtitles/{Index}/{StartPositionTicks}/Stream.{Format}":  subtitle,
	"GET /Videos/{Id}/{MediaSourceId}/Subtitles/{Index}/Stream.{Format}":                      subtitle,
	"GET /Videos/{Id}/{MediaSourceId}/Subtitles/{Index}/{StartPositionTicks}/Stream.{Format}": subtitle,
	"GET /Videos/{Id}/{MediaSourceId}/Attachments/{Index}/Stream":                             download,

	// DLNA descriptions
	"GET /Dlna/{UuId}/connectionmanager/connectionmanager":     xml,
	"GET /Dlna/{UuId}/connectionmanager/connectionmanager.xml": xml,
	"GET /Dlna/{UuId}/contentdirectory/contentdirectory":       xml,
	"GET /Dlna/{UuId}/contentdirectory/contentdirectory.xml":   xml,
	"GET /Dlna/{UuId}/description":                             xml,
	"GET /Dlna/{UuId}/description.xml":                         xml,

	// downloads
	"GET /Items/{Id}/Download":                download,
	"GET /Items/{Id}/File":                    download,
	"GET /Playback/BitrateTest":               download,
	"GET /Providers/Subtitles/Subtitles/{Id}": download,
	"GET /Sync/JobItems/{Id}/AdditionalFiles": download,
	"GET /Sync/JobItems/{Id}/File":            download,
	"GET /Videos/{Id}/index.bif":              download,
})

var embyUndeclaredPathParameters = pandorest.UndeclaredParameters(pandorest.About{Name: "emby-undeclared-path-parameters", Service: emby, Bug: "POST /Users/{Id}/Images/{Type}/{Index} has an {Index} placeholder but declares no path parameter for it (the item image route beside it does)"}, []string{"POST /Users/{Id}/Images/{Type}/{Index}"}, param{Name: "Index", In: openapi.InPath, Type: openapi.TypeInteger, Description: "Image Index"})

type embyNoContentStatus struct{}

func (embyNoContentStatus) Name() string    { return "emby-no-content-status" }
func (embyNoContentStatus) Service() string { return emby }
func (embyNoContentStatus) Bug() string {
	return "operations with an empty response declare 200, but the server answers them 204 No Content"
}

func (embyNoContentStatus) Apply(spec *openapi.Spec) error {
	n := 0
	for _, path := range openapi.SortedKeys(spec.Paths) {
		for _, m := range spec.Paths[path].Methods() {
			responses := m.Operation.Responses
			ok := responses["200"]
			if m.Method == http.MethodGet || ok == nil || len(ok.Content) > 0 || responses["204"] != nil {
				continue
			}
			responses["204"] = &openapi.Response{Description: "No Content"}
			n++
		}
	}
	if n == 0 {
		return errors.New("no operation declares an empty 200 without a 204")
	}

	return nil
}

var embyPlaylistCreateUserID = pandorest.UndeclaredParameters(pandorest.About{Name: "emby-playlist-create-user-id", Service: emby, Bug: "POST /Playlists does not declare UserId, which names the playlist's owner"}, []string{"POST /Playlists"}, param{Name: "UserId", In: openapi.InQuery, Type: openapi.TypeString, Description: "The user who owns the playlist"})

var embyLibraryAvailableOptionsQuery = pandorest.UndeclaredParameters(pandorest.About{Name: "emby-library-available-options-query", Service: emby, Bug: "GET /Libraries/AvailableOptions declares no parameters; without LibraryContentType the server answers for every item type at once"}, []string{"GET /Libraries/AvailableOptions"}, param{Name: "LibraryContentType", In: openapi.InQuery, Type: openapi.TypeString, Description: "The collection type of the library (movies, tvshows, music, ...)"}, param{Name: "IsNewLibrary", In: openapi.InQuery, Type: openapi.TypeBoolean, Description: "Whether the options are for a library being created, which is when the defaults are enabled"})

var embyNextUpLegacy = pandorest.UndeclaredParameters(pandorest.About{Name: "emby-next-up-legacy", Service: emby, Bug: "GET /Shows/NextUp does not declare LegacyNextUp, the per-series next-unwatched mode (4.10's default mode lists nothing for an episode marked played through the API)"}, []string{"GET /Shows/NextUp"}, param{Name: "LegacyNextUp", In: openapi.InQuery, Type: openapi.TypeBoolean, Description: "Use the per-series next unwatched episode mode"})

type embyUndeclaredQuery struct{}

// embyUndeclaredQueries are the GETs that answer 500 (a null reference, or a
// lookup that finds nothing) until they are given a query parameter their
// document leaves out, and answer once it is there.
var embyUndeclaredQueries = []struct {
	target string
	params []param
}{
	{"GET /Artists/InstantMix", []param{{Name: "Id", In: openapi.InQuery, Type: openapi.TypeString, Description: "The artist the mix is made from"}}},
	{"GET /MusicGenres/InstantMix", []param{{Name: "Id", In: openapi.InQuery, Type: openapi.TypeString, Description: "The music genre the mix is made from"}}},
	{"GET /Audio/{Id}/universal", []param{{Name: "UserId", In: openapi.InQuery, Type: openapi.TypeString, Description: "The user the stream is for"}}},
	{"GET /Audio/{Id}/universal.{Container}", []param{{Name: "UserId", In: openapi.InQuery, Type: openapi.TypeString, Description: "The user the stream is for"}}},
	{"GET /Videos/{Id}/subtitles.m3u8", []param{{Name: "MediaSourceId", In: openapi.InQuery, Type: openapi.TypeString, Description: "The media source whose subtitles the playlist segments"}}},
	{"GET /web/strings", []param{
		{Name: "PluginId", In: openapi.InQuery, Type: openapi.TypeString, Description: "The plugin whose strings to read"},
		{Name: "Locale", In: openapi.InQuery, Type: openapi.TypeString, Description: "The language of the strings, one of those /web/stringset lists (en-US); without it the answer is empty"},
	}},
	{"GET /web/stringset", []param{{Name: "PluginId", In: openapi.InQuery, Type: openapi.TypeString, Description: "The plugin whose translations to list"}}},
	{"GET /Notifications/Services/Defaults", []param{
		{Name: "NotifierKey", In: openapi.InQuery, Type: openapi.TypeString, Description: "The notification service, by the Id /Notifications/Services lists"},
		{Name: "UserId", In: openapi.InQuery, Type: openapi.TypeString, Description: "The user the defaults are for"},
	}},
	{"GET /Users/ItemAccess", []param{{Name: "ItemId", In: openapi.InQuery, Type: openapi.TypeString, Description: "The playlist or collection whose sharing to list"}}},
}

func (embyUndeclaredQuery) Name() string    { return "emby-undeclared-query" }
func (embyUndeclaredQuery) Service() string { return emby }
func (embyUndeclaredQuery) Bug() string {
	return "nine GETs answer 500 without a query parameter their document does not declare: the instant mixes by artist and music genre need the Id they are made from, universal audio a UserId, the HLS subtitle playlist a MediaSourceId, the web strings a PluginId (and a Locale, without which they are empty), the notification defaults a NotifierKey and UserId, and a playlist's sharing (/Users/ItemAccess) its ItemId"
}

func (embyUndeclaredQuery) Apply(spec *openapi.Spec) error {
	for _, q := range embyUndeclaredQueries {
		if err := addParameters(spec, []string{q.target}, q.params...); err != nil {
			return err
		}
	}

	return nil
}

type embyToneMapOptions struct{}

// embyToneMapOptionsSchema is the tone mapping editor the server answers: the
// visibility the document declares, under OptionsVisibility, beside the
// settings and the editor's own text.
const embyToneMapOptionsSchema = "Configuration.ToneMapping.ToneMapOptions"

func (embyToneMapOptions) Name() string    { return "emby-tone-map-options" }
func (embyToneMapOptions) Service() string { return emby }
func (embyToneMapOptions) Bug() string {
	return "GET /Encoding/ToneMapOptions declares a ToneMapOptionsVisibility; the server answers the tone mapping editor, which holds one under OptionsVisibility beside the settings (EnableSoftwareToneMapping, the software and hardware options) and the editor's title and description"
}

func (embyToneMapOptions) Apply(spec *openapi.Spec) error {
	op, err := operation(spec, http.MethodGet, "/Encoding/ToneMapOptions")
	if err != nil {
		return err
	}
	media, err := jsonResponse(op, "GET /Encoding/ToneMapOptions")
	if err != nil {
		return err
	}
	visibility := "Configuration.ToneMapping.ToneMapOptionsVisibility"
	if media.Schema == nil || media.Schema.RefName() != visibility {
		return errors.New("it no longer declares a " + visibility)
	}
	if spec.Components.Schemas[embyToneMapOptionsSchema] != nil {
		return errors.New("the document declares " + embyToneMapOptionsSchema)
	}
	str, boolean := &openapi.Schema{Type: openapi.TypeString}, &openapi.Schema{Type: openapi.TypeBoolean}
	spec.Components.Schemas[embyToneMapOptionsSchema] = &openapi.Schema{Type: openapi.TypeObject, Properties: map[string]*openapi.Schema{
		"OptionsVisibility":         {Ref: openapi.SchemaRefPrefix + visibility},
		"EditorTitle":               str,
		"EditorDescription":         str,
		"FeatureRequiresPremiere":   boolean,
		"EnableSoftwareToneMapping": boolean,
		"EnableHardwareToneMapping": boolean,
		"IsNewItem":                 boolean,
		// editors of their own, whose shape depends on the hardware found
		"SoftwareToneMapOptions": {Type: openapi.TypeObject},
		"HardwareToneMapOptions": {Type: openapi.TypeObject},
	}}
	ref := &openapi.Schema{Ref: openapi.SchemaRefPrefix + embyToneMapOptionsSchema}
	for _, m := range op.Responses["200"].Content {
		m.Schema = ref
	}

	return nil
}

type embyPlaystateSeekQuery struct{}

func (embyPlaystateSeekQuery) Name() string    { return "emby-playstate-seek-query" }
func (embyPlaystateSeekQuery) Service() string { return emby }
func (embyPlaystateSeekQuery) Bug() string {
	return "POST /Sessions/{Id}/Playing/{Command} declares SeekPositionTicks only in its body, a number the typed model leaves out when it is 0, so a seek to the start cannot be sent; the server also reads it from the query, where Emby's own web client sends it and where a set option is sent even at 0"
}

func (embyPlaystateSeekQuery) Apply(spec *openapi.Spec) error {
	op, err := operation(spec, http.MethodPost, "/Sessions/{Id}/Playing/{Command}")
	if err != nil {
		return err
	}
	if op.Parameter(openapi.InQuery, "SeekPositionTicks") != nil {
		return errors.New("SeekPositionTicks is declared in the query")
	}
	op.Parameters = append(op.Parameters, &openapi.Parameter{
		Name: "SeekPositionTicks", In: openapi.InQuery, Description: "The position to seek to, in ticks",
		Schema: &openapi.Schema{Type: openapi.TypeInteger, Format: "int64"},
	})

	return nil
}

type embyLibraryDeleteID struct{}

func (embyLibraryDeleteID) Name() string    { return "emby-library-delete-id" }
func (embyLibraryDeleteID) Service() string { return emby }
func (embyLibraryDeleteID) Bug() string {
	return "DELETE /Library/VirtualFolders and DELETE /Library/VirtualFolders/Paths declare no parameters; the server identifies the library by Id (a Name is a 500, Unrecognized Guid format)"
}

func (embyLibraryDeleteID) Apply(spec *openapi.Spec) error {
	id := param{Name: "Id", In: openapi.InQuery, Type: openapi.TypeString, Description: "The library's ItemId"}
	refresh := param{Name: "RefreshLibrary", In: openapi.InQuery, Type: openapi.TypeBoolean, Description: "Whether to scan the libraries afterwards"}
	if err := addParameters(spec, []string{"DELETE /Library/VirtualFolders"}, id, refresh); err != nil {
		return err
	}

	return addParameters(spec, []string{"DELETE /Library/VirtualFolders/Paths"}, id,
		param{Name: "Path", In: openapi.InQuery, Type: openapi.TypeString, Description: "The folder to take out of the library"}, refresh)
}

type embyNullResultNoContent struct{}

func (embyNullResultNoContent) Name() string    { return "emby-null-result-no-content" }
func (embyNullResultNoContent) Service() string { return emby }
func (embyNullResultNoContent) Bug() string {
	return "every operation answering JSON declares only 200, but the server answers 204 No Content for a null result (a timer, sync job or DLNA profile that does not exist, and any other lookup that finds nothing), so 204 is added to each of them and a nil Model with no error is that answer"
}

func (embyNullResultNoContent) Apply(spec *openapi.Spec) error {
	n := 0
	for _, path := range openapi.SortedKeys(spec.Paths) {
		for _, m := range spec.Paths[path].Methods() {
			responses := m.Operation.Responses
			ok := responses["200"]
			if ok == nil || responses["204"] != nil {
				continue
			}
			if _, json := ok.Content["application/json"]; !json {
				continue
			}
			responses["204"] = &openapi.Response{Description: "No Content: the result is null"}
			n++
		}
	}
	if n == 0 {
		return errors.New("no JSON operation declares 200 without 204")
	}

	return nil
}

var embyOpenAPIDocuments = pandorest.WrongAnswers(pandorest.About{Name: "emby-openapi-documents", Service: emby, Bug: "GET /openapi, /openapi.json, /swagger and /swagger.json declare a JSON string; they answer the API document itself, a JSON object"}, map[string]pandorest.Correction{
	"GET /openapi":      {Declared: pandorest.JSONString(), Answers: pandorest.JSON()},
	"GET /openapi.json": {Declared: pandorest.JSONString(), Answers: pandorest.JSON()},
	"GET /swagger":      {Declared: pandorest.JSONString(), Answers: pandorest.JSON()},
	"GET /swagger.json": {Declared: pandorest.JSONString(), Answers: pandorest.JSON()},
})

var embyRecordingFoldersQueryResult = pandorest.WrongAnswers(pandorest.About{Name: "emby-recording-folders-query-result", Service: emby, Bug: "GET /LiveTv/Recordings/Folders declares an array of BaseItemDto; the server answers a QueryResult_BaseItemDto"}, map[string]pandorest.Correction{
	"GET /LiveTv/Recordings/Folders": {Declared: pandorest.ListOf("BaseItemDto"), Answers: pandorest.Model(queryResult)},
})

var embyParentPathText = pandorest.WrongAnswers(pandorest.About{Name: "emby-parent-path-text", Service: emby, Bug: "GET /Environment/ParentPath declares a JSON string; the server answers the bare path (/media, not \"/media\") and still labels it application/json, so it is read as text whatever it is called"}, map[string]pandorest.Correction{
	"GET /Environment/ParentPath": {Declared: pandorest.JSONString(), Answers: pandorest.Text("text/plain")},
})

type embyCommaSeparatedArrays struct{}

func (embyCommaSeparatedArrays) Name() string    { return "emby-comma-separated-arrays" }
func (embyCommaSeparatedArrays) Service() string { return emby }
func (embyCommaSeparatedArrays) Bug() string {
	return "array query parameters declare the default form style, one key per value, but the server reads a single comma-delimited value"
}

func (embyCommaSeparatedArrays) Apply(spec *openapi.Spec) error {
	n := 0
	for _, path := range openapi.SortedKeys(spec.Paths) {
		for _, m := range spec.Paths[path].Methods() {
			for _, p := range m.Operation.Parameters {
				if p.In == openapi.InQuery && p.Schema != nil && p.Schema.Type == openapi.TypeArray && p.Exploded() {
					p.Style, p.Explode = "form", new(false)
					n++
				}
			}
		}
	}
	if n == 0 {
		return errors.New("no array query parameter is declared exploded")
	}

	return nil
}

type embyCodecDisplayText struct{}

func (embyCodecDisplayText) Name() string    { return "emby-codec-display-text" }
func (embyCodecDisplayText) Service() string { return emby }
func (embyCodecDisplayText) Bug() string {
	return "GET /Encoding/CodecInformation/Video declares its bit rates as BitRate objects and its resolution rates as ResolutionWithRate objects; the server answers display text (\"781 Mbit/s\", \"8192x4320@120\")"
}

func (embyCodecDisplayText) Apply(spec *openapi.Spec) error {
	for _, target := range []struct{ schema, property string }{
		{"VideoCodecBase", "MaxBitRate"},
		{"LevelInformation", "MaxBitRate"},
	} {
		p, err := property(spec, target.schema, target.property)
		if err != nil {
			return err
		}
		if p.RefName() != "BitRate" {
			return fmt.Errorf("%s.%s no longer refers to BitRate", target.schema, target.property)
		}
		*p = openapi.Schema{Type: openapi.TypeString}
	}
	p, err := property(spec, "LevelInformation", "ResolutionRates")
	if err != nil {
		return err
	}
	if p.Type != openapi.TypeArray || p.Items == nil || p.Items.RefName() != "ResolutionWithRate" {
		return errors.New("LevelInformation.ResolutionRates is no longer a list of ResolutionWithRate")
	}
	// nothing refers to the two schemas once the text stands in for them,
	// so they would be two models no operation answers
	for _, name := range []string{"BitRate", "ResolutionWithRate"} {
		if spec.Components.Schemas[name] == nil {
			return errors.New("schema " + name + " is gone")
		}
		delete(spec.Components.Schemas, name)
	}
	p.Items = &openapi.Schema{Type: openapi.TypeString}

	return nil
}

type embyPropertyConditionValue struct{}

func (embyPropertyConditionValue) Name() string    { return "emby-property-condition-value" }
func (embyPropertyConditionValue) Service() string { return emby }
func (embyPropertyConditionValue) Bug() string {
	return "Conditions.PropertyCondition.Value declares an object; it is the value an editor property is compared with, and the server answers a string or null (the encoding and subtitle option editors)"
}

func (embyPropertyConditionValue) Apply(spec *openapi.Spec) error {
	p, err := property(spec, "Conditions.PropertyCondition", "Value")
	if err != nil {
		return err
	}
	if p.Type != openapi.TypeObject || len(p.Properties) > 0 {
		return errors.New("it no longer declares a bare object")
	}
	*p = openapi.Schema{}

	return nil
}

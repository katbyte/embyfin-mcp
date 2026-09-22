package workarounds

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/katbyte/embyfin-mcp/internal/pandorest/openapi"
)

// The Emby document is the one Emby Server 4.10 serves itself at
// /emby/openapi.json (swagger.emby.media still serves 4.1.1). What follows was
// found by the integration suite's read sweep and bespoke tests against that
// server.

const emby = "emby"

// embyUndeclaredResponses declares what the GETs without a response schema
// answer. A file is a content type; json is JSON of no declared schema; any
// other value is the component schema the JSON decodes into.
type embyUndeclaredResponses struct{}

const (
	answersJSON = "json"
	imageAny    = "image/*"
	audioAny    = "audio/*"
	videoAny    = "video/*"
	textAny     = "text/*"
	textXML     = "text/xml"
	octetStream = "application/octet-stream"
	hlsPlaylist = "application/x-mpegURL"
	queryResult = "QueryResult_BaseItemDto"
)

var embyUndeclared = map[string]string{
	// JSON
	"/Auth/Keys":                          answersJSON,
	"/Collections/{Id}/Missing":           queryResult,
	"/Collections/{Id}/ProviderItems":     answersJSON,
	"/Connect/Pending":                    answersJSON,
	"/LiveTv/ChannelMappingOptions":       answersJSON,
	"/LiveTv/ChannelMappings":             answersJSON,
	"/LiveTv/Programs":                    queryResult,
	"/LiveTv/Recordings":                  queryResult,
	"/Parties":                            answersJSON,
	"/Plugins/{Id}/Configuration":         answersJSON,
	"/Shows/Missing":                      queryResult,
	"/Shows/Upcoming":                     queryResult,
	"/Shows/{Id}/Episodes":                queryResult,
	"/System/Configuration/{Key}":         answersJSON,
	"/Users/{UserId}/TypedSettings/{Key}": answersJSON,
	"/web/strings":                        answersJSON,

	// text
	"/Branding/Css":          "text/css",
	"/Branding/Css.css":      "text/css",
	"/System/Logs/{Name}":    "text/plain",
	"/System/Ping":           "text/plain",
	"/web/ConfigurationPage": "text/html",

	// images
	"/Artists/{Name}/Images/{Type}":            imageAny,
	"/Artists/{Name}/Images/{Type}/{Index}":    imageAny,
	"/Dlna/icons/{Filename}":                   imageAny,
	"/Dlna/{UuId}/icons/{Filename}":            imageAny,
	"/GameGenres/{Name}/Images/{Type}":         imageAny,
	"/GameGenres/{Name}/Images/{Type}/{Index}": imageAny,
	"/Genres/{Name}/Images/{Type}":             imageAny,
	"/Genres/{Name}/Images/{Type}/{Index}":     imageAny,
	"/Images/Remote":                           imageAny,
	"/Items/RemoteSearch/Image":                imageAny,
	"/Items/{Id}/Images/{Type}":                imageAny,
	"/Items/{Id}/Images/{Type}/{Index}":        imageAny,
	"/Items/{Id}/Images/{Type}/{Index}/{Tag}/{Format}/{MaxWidth}/{MaxHeight}/{PercentPlayed}/{UnPlayedCount}": imageAny,
	"/MusicGenres/{Name}/Images/{Type}":         imageAny,
	"/MusicGenres/{Name}/Images/{Type}/{Index}": imageAny,
	"/Persons/{Name}/Images/{Type}":             imageAny,
	"/Persons/{Name}/Images/{Type}/{Index}":     imageAny,
	"/Plugins/{Id}/Thumb":                       imageAny,
	"/Studios/{Name}/Images/{Type}":             imageAny,
	"/Studios/{Name}/Images/{Type}/{Index}":     imageAny,
	"/Users/{Id}/Images/{Type}":                 imageAny,
	"/Users/{Id}/Images/{Type}/{Index}":         imageAny,

	// media
	"/Audio/{Id}/{StreamFileName}":                    audioAny,
	"/Audio/{Id}/stream":                              audioAny,
	"/Audio/{Id}/stream.{Container}":                  audioAny,
	"/Audio/{Id}/universal":                           audioAny,
	"/Audio/{Id}/universal.{Container}":               audioAny,
	"/LiveTv/LiveRecordings/{Id}/stream":              videoAny,
	"/LiveTv/LiveStreamFiles/{Id}/stream.{Container}": videoAny,
	"/Videos/{Id}/{StreamFileName}":                   videoAny,
	"/Videos/{Id}/stream":                             videoAny,
	"/Videos/{Id}/stream.{Container}":                 videoAny,

	// HLS playlists and segments
	"/Audio/{Id}/live.m3u8":                                         hlsPlaylist,
	"/Audio/{Id}/main.m3u8":                                         hlsPlaylist,
	"/Audio/{Id}/master.m3u8":                                       hlsPlaylist,
	"/LiveTv/LiveRecordings/{Id}/hls/live.m3u8":                     hlsPlaylist,
	"/LiveTv/LiveRecordings/{Id}/hls/master.m3u8":                   hlsPlaylist,
	"/LiveTv/LiveStreamFiles/{Id}/hls/live.m3u8":                    hlsPlaylist,
	"/LiveTv/LiveStreamFiles/{Id}/hls/master.m3u8":                  hlsPlaylist,
	"/Videos/{Id}/live.m3u8":                                        hlsPlaylist,
	"/Videos/{Id}/live_subtitles.m3u8":                              hlsPlaylist,
	"/Videos/{Id}/main.m3u8":                                        hlsPlaylist,
	"/Videos/{Id}/master.m3u8":                                      hlsPlaylist,
	"/Videos/{Id}/subtitles.m3u8":                                   hlsPlaylist,
	"/Audio/{Id}/hls/{PlaylistId}/{SegmentId}.{SegmentContainer}":   octetStream,
	"/Audio/{Id}/hls1/{PlaylistId}/{SegmentId}.{SegmentContainer}":  octetStream,
	"/LiveTv/LiveRecordings/{Id}/hls/{Segment}":                     octetStream,
	"/LiveTv/LiveStreamFiles/{Id}/hls/{Segment}":                    octetStream,
	"/Videos/{Id}/hls/{PlaylistId}/{SegmentId}.{SegmentContainer}":  octetStream,
	"/Videos/{Id}/hls1/{PlaylistId}/{SegmentId}.{SegmentContainer}": octetStream,

	// subtitles and attachments
	"/Items/{Id}/{MediaSourceId}/Subtitles/{Index}/Stream.{Format}":                       textAny,
	"/Items/{Id}/{MediaSourceId}/Subtitles/{Index}/{StartPositionTicks}/Stream.{Format}":  textAny,
	"/Videos/{Id}/{MediaSourceId}/Subtitles/{Index}/Stream.{Format}":                      textAny,
	"/Videos/{Id}/{MediaSourceId}/Subtitles/{Index}/{StartPositionTicks}/Stream.{Format}": textAny,
	"/Videos/{Id}/{MediaSourceId}/Attachments/{Index}/Stream":                             octetStream,

	// DLNA descriptions
	"/Dlna/{UuId}/connectionmanager/connectionmanager":     textXML,
	"/Dlna/{UuId}/connectionmanager/connectionmanager.xml": textXML,
	"/Dlna/{UuId}/contentdirectory/contentdirectory":       textXML,
	"/Dlna/{UuId}/contentdirectory/contentdirectory.xml":   textXML,
	"/Dlna/{UuId}/description":                             textXML,
	"/Dlna/{UuId}/description.xml":                         textXML,

	// downloads
	"/Items/{Id}/Download":                octetStream,
	"/Items/{Id}/File":                    octetStream,
	"/Playback/BitrateTest":               octetStream,
	"/Providers/Subtitles/Subtitles/{Id}": octetStream,
	"/Sync/JobItems/{Id}/AdditionalFiles": octetStream,
	"/Sync/JobItems/{Id}/File":            octetStream,
	"/Videos/{Id}/index.bif":              octetStream,
}

func (embyUndeclaredResponses) Name() string    { return "emby-undeclared-responses" }
func (embyUndeclaredResponses) Service() string { return emby }
func (embyUndeclaredResponses) Bug() string {
	return "about 95 GETs declare a 200 with no content, so nothing says whether they answer JSON (and in what shape) or a file"
}

func (embyUndeclaredResponses) Apply(spec *openapi.Spec) error {
	var declared []string
	for _, path := range openapi.SortedKeys(embyUndeclared) {
		op, err := operation(spec, http.MethodGet, path)
		if err != nil {
			return err
		}
		ok := op.Responses["200"]
		if ok == nil {
			return fmt.Errorf("GET %s has no 200 response", path)
		}
		if len(ok.Content) > 0 {
			declared = append(declared, path)
			continue
		}
		switch answer := embyUndeclared[path]; {
		case answer == answersJSON:
			ok.Content = map[string]*openapi.MediaType{"application/json": {}}
		case strings.Contains(answer, "/"):
			ok.Content = map[string]*openapi.MediaType{answer: {Schema: &openapi.Schema{Type: openapi.TypeString, Format: "binary"}}}
		default:
			if spec.Components.Schemas[answer] == nil {
				return fmt.Errorf("GET %s: schema %s is not in the document", path, answer)
			}
			ok.Content = map[string]*openapi.MediaType{"application/json": {Schema: &openapi.Schema{Ref: openapi.SchemaRefPrefix + answer}}}
		}
	}
	if len(declared) > 0 {
		return fmt.Errorf("these now declare their response, so take them out of the table: %s", strings.Join(declared, ", "))
	}

	return nil
}

type embyUndeclaredPathParameters struct{}

func (embyUndeclaredPathParameters) Name() string    { return "emby-undeclared-path-parameters" }
func (embyUndeclaredPathParameters) Service() string { return emby }
func (embyUndeclaredPathParameters) Bug() string {
	return "POST /Users/{Id}/Images/{Type}/{Index} has an {Index} placeholder but declares no path parameter for it (the item image route beside it does)"
}

func (embyUndeclaredPathParameters) Apply(spec *openapi.Spec) error {
	op, err := operation(spec, http.MethodPost, "/Users/{Id}/Images/{Type}/{Index}")
	if err != nil {
		return err
	}
	if op.Parameter(openapi.InPath, "Index") != nil {
		return errors.New("the {Index} path parameter is declared")
	}
	op.Parameters = append(op.Parameters, &openapi.Parameter{
		Name: "Index", In: openapi.InPath, Required: true, Description: "Image Index", Schema: &openapi.Schema{Type: openapi.TypeInteger, Format: "int32"},
	})

	return nil
}

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

type embyPlaylistCreateUserID struct{}

func (embyPlaylistCreateUserID) Name() string    { return "emby-playlist-create-user-id" }
func (embyPlaylistCreateUserID) Service() string { return emby }
func (embyPlaylistCreateUserID) Bug() string {
	return "POST /Playlists does not declare UserId, which names the playlist's owner"
}

func (embyPlaylistCreateUserID) Apply(spec *openapi.Spec) error {
	return addParameters(spec, []string{"POST /Playlists"},
		param{"UserId", openapi.InQuery, openapi.TypeString, "The user who owns the playlist"})
}

type embyLibraryAvailableOptionsQuery struct{}

func (embyLibraryAvailableOptionsQuery) Name() string    { return "emby-library-available-options-query" }
func (embyLibraryAvailableOptionsQuery) Service() string { return emby }
func (embyLibraryAvailableOptionsQuery) Bug() string {
	return "GET /Libraries/AvailableOptions declares no parameters; without LibraryContentType the server answers for every item type at once"
}

func (embyLibraryAvailableOptionsQuery) Apply(spec *openapi.Spec) error {
	return addParameters(spec, []string{"GET /Libraries/AvailableOptions"},
		param{"LibraryContentType", openapi.InQuery, openapi.TypeString, "The collection type of the library (movies, tvshows, music, ...)"},
		param{"IsNewLibrary", openapi.InQuery, openapi.TypeBoolean, "Whether the options are for a library being created, which is when the defaults are enabled"})
}

type embyNextUpLegacy struct{}

func (embyNextUpLegacy) Name() string    { return "emby-next-up-legacy" }
func (embyNextUpLegacy) Service() string { return emby }
func (embyNextUpLegacy) Bug() string {
	return "GET /Shows/NextUp does not declare LegacyNextUp, the per-series next-unwatched mode (4.10's default mode lists nothing for an episode marked played through the API)"
}

func (embyNextUpLegacy) Apply(spec *openapi.Spec) error {
	return addParameters(spec, []string{"GET /Shows/NextUp"},
		param{"LegacyNextUp", openapi.InQuery, openapi.TypeBoolean, "Use the per-series next unwatched episode mode"})
}

type embyLibraryDeleteID struct{}

func (embyLibraryDeleteID) Name() string    { return "emby-library-delete-id" }
func (embyLibraryDeleteID) Service() string { return emby }
func (embyLibraryDeleteID) Bug() string {
	return "DELETE /Library/VirtualFolders and DELETE /Library/VirtualFolders/Paths declare no parameters; the server identifies the library by Id (a Name is a 500, Unrecognized Guid format)"
}

func (embyLibraryDeleteID) Apply(spec *openapi.Spec) error {
	id := param{"Id", openapi.InQuery, openapi.TypeString, "The library's ItemId"}
	refresh := param{"RefreshLibrary", openapi.InQuery, openapi.TypeBoolean, "Whether to scan the libraries afterwards"}
	if err := addParameters(spec, []string{"DELETE /Library/VirtualFolders"}, id, refresh); err != nil {
		return err
	}

	return addParameters(spec, []string{"DELETE /Library/VirtualFolders/Paths"}, id,
		param{"Path", openapi.InQuery, openapi.TypeString, "The folder to take out of the library"}, refresh)
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

type embyOpenAPIDocuments struct{}

func (embyOpenAPIDocuments) Name() string    { return "emby-openapi-documents" }
func (embyOpenAPIDocuments) Service() string { return emby }
func (embyOpenAPIDocuments) Bug() string {
	return "GET /openapi, /openapi.json, /swagger and /swagger.json declare a JSON string; they answer the API document itself, a JSON object"
}

func (embyOpenAPIDocuments) Apply(spec *openapi.Spec) error {
	for _, path := range []string{"/openapi", "/openapi.json", "/swagger", "/swagger.json"} {
		op, err := operation(spec, http.MethodGet, path)
		if err != nil {
			return err
		}
		media, err := jsonResponse(op, "GET "+path)
		if err != nil {
			return err
		}
		if media.Schema == nil || media.Schema.Type != openapi.TypeString {
			return fmt.Errorf("GET %s no longer declares a string", path)
		}
		media.Schema = nil
	}

	return nil
}

type embyRecordingFoldersQueryResult struct{}

func (embyRecordingFoldersQueryResult) Name() string    { return "emby-recording-folders-query-result" }
func (embyRecordingFoldersQueryResult) Service() string { return emby }
func (embyRecordingFoldersQueryResult) Bug() string {
	return "GET /LiveTv/Recordings/Folders declares an array of BaseItemDto; the server answers a QueryResult_BaseItemDto"
}

func (embyRecordingFoldersQueryResult) Apply(spec *openapi.Spec) error {
	op, err := operation(spec, http.MethodGet, "/LiveTv/Recordings/Folders")
	if err != nil {
		return err
	}
	media, err := jsonResponse(op, "GET /LiveTv/Recordings/Folders")
	if err != nil {
		return err
	}
	if media.Schema == nil || media.Schema.Type != openapi.TypeArray {
		return errors.New("it no longer declares an array")
	}
	media.Schema = &openapi.Schema{Ref: openapi.SchemaRefPrefix + queryResult}

	return nil
}

type embyParentPathText struct{}

func (embyParentPathText) Name() string    { return "emby-parent-path-text" }
func (embyParentPathText) Service() string { return emby }
func (embyParentPathText) Bug() string {
	return "GET /Environment/ParentPath declares a JSON string; the server answers the bare path as text"
}

func (embyParentPathText) Apply(spec *openapi.Spec) error {
	op, err := operation(spec, http.MethodGet, "/Environment/ParentPath")
	if err != nil {
		return err
	}
	media, err := jsonResponse(op, "GET /Environment/ParentPath")
	if err != nil {
		return err
	}
	if media.Schema == nil || media.Schema.Type != openapi.TypeString {
		return errors.New("it no longer declares a JSON string")
	}
	op.Responses["200"].Content = map[string]*openapi.MediaType{"text/plain": {Schema: &openapi.Schema{Type: openapi.TypeString, Format: "binary"}}}

	return nil
}

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

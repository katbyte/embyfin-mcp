package importer

import (
	"reflect"
	"testing"
)

func TestCamel(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"QueryResult_BaseItemDto", "QueryResultBaseItemDto"},
		{"Library.AddVirtualFolder", "LibraryAddVirtualFolder"},
		{"QueryResult_UserLibrary.TagItem", "QueryResultUserLibraryTagItem"},
		{"Tuple_Double-Double", "TupleDoubleDouble"},
		{"Some Name+Plus", "SomeNamePlus"},
		{"GetFirstUser_2", "GetFirstUser2"},
		{"master.m3u8", "MasterM3u8"},
		{"user_usage_stats", "UserUsageStats"},
		{"price", "Price"},
		{"_id", "Id"},
		{"UserID", "UserID"},
		{"3d", "N3d"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := camel(tt.in); got != tt.want {
			t.Errorf("camel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestArgName(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"Id", "id"},
		{"itemId", "itemId"},
		{"UuId", "uuId"},
		{"Type", "typeParam"},
		{"Range", "rangeParam"},
		{"Input", "inputParam"},
		{"Options", "optionsParam"},
		{"string", "stringParam"},
		{"", "arg"},
	}
	for _, tt := range tests {
		if got := argName(tt.in); got != tt.want {
			t.Errorf("argName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPathMethodName(t *testing.T) {
	t.Parallel()

	tests := []struct{ method, path, want string }{
		{"GET", "/Items", "GetItems"},
		{"GET", "/Items/{Id}/Similar", "GetItemsByIdSimilar"},
		{"POST", "/Users/AuthenticateByName", "PostUsersAuthenticateByName"},
		{"GET", "/Videos/{Id}/stream.{Container}", "GetVideosByIdStreamByContainer"},
		{"GET", "/Videos/{Id}/master.m3u8", "GetVideosByIdMasterM3u8"},
		{"GET", "/Branding/Css.css", "GetBrandingCssCss"},
		{"GET", "/Audio/{Id}/hls1/{PlaylistId}/{SegmentId}.{SegmentContainer}", "GetAudioByIdHls1ByPlaylistIdBySegmentIdBySegmentContainer"},
		{"DELETE", "/Items/{Id}", "DeleteItemsById"},
	}
	for _, tt := range tests {
		if got := pathMethodName(tt.method, tt.path); got != tt.want {
			t.Errorf("pathMethodName(%s, %s) = %q, want %q", tt.method, tt.path, got, tt.want)
		}
	}
}

func TestSplitTemplate(t *testing.T) {
	t.Parallel()

	got := splitTemplate("{SegmentId}.{SegmentContainer}")
	want := []templatePiece{{text: "SegmentId", param: true}, {text: "."}, {text: "SegmentContainer", param: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitTemplate = %+v, want %+v", got, want)
	}
}

func TestUniqueNamer(t *testing.T) {
	t.Parallel()

	var warned []string
	u := newUniqueNamer(func(s string) { warned = append(warned, s) })
	for i, want := range []string{"GetX", "GetX2", "GetX3"} {
		if got := u.name("GetX", "ctx"); got != want {
			t.Errorf("name %d = %q, want %q", i, got, want)
		}
	}
	if len(warned) != 2 {
		t.Errorf("warnings = %v, want 2", warned)
	}
}

func TestCleanText(t *testing.T) {
	t.Parallel()

	if got := cleanText(" a\r\nb  \n\nc \n"); got != "a\nb\n\nc" {
		t.Errorf("cleanText = %q", got)
	}
}

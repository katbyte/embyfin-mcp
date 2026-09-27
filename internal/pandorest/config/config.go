// Package config lists the services pandorest imports and generates, the
// equivalent of Pandora's resource-manager.hcl. Paths are relative to the
// repository root, which is where the make targets run pandorest from.
package config

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Naming is how operations get their Go method names.
type Naming string

const (
	// PathNaming builds names from the HTTP method and path, for documents
	// whose operationIds are machine-made and lossy (Emby's
	// getAudiocodecs, and duplicates).
	PathNaming Naming = "path"
	// OperationIDNaming uses the operationId (Jellyfin's are hand-written
	// and unique).
	OperationIDNaming Naming = "operationId"
)

// Service is one server API.
//
// Its document and definitions are versioned by the document's own version
// string: api-defs/<name>-openapi-<version>.json is imported into
// api-defs/<name>-<version>/ beside it, and Resolve picks the highest version
// present, which is the one the SDK is generated from. An older document can
// stay beside it for the diff, and a newer one is picked up by being added.
type Service struct {
	// Name is the -service flag and the prefix of the document and
	// definitions names.
	Name string
	// Package is the Go package name of the generated SDK.
	Package string
	// Output is the generated package directory.
	Output string
	// Naming picks how method names are built.
	Naming Naming
	// TagSuffix is trimmed from tag names to make group names ("Service"
	// on Emby: LibraryService is the Library group).
	TagSuffix string
	// Auth names the lib/client authorizer the generated New uses.
	Auth string
	// KeepNull names the model fields, as Schema.property, whose null is
	// not their zero, which the SDK holds as pointers where it holds every
	// other number by value: season 0 is the specials and a parental limit
	// of 0 is the strictest, so a season or a limit the server leaves out
	// must not read as either. Each must be a number the document declares
	// nullable, or the import fails naming it.
	KeepNull []string

	// Version, Spec and Definitions are set by Resolve: the document's
	// version, the vendored OpenAPI document, and where the importer writes
	// and the generator reads, the last two relative to the repository.
	Version     string
	Spec        string
	Definitions string

	// Root is the repository root the paths above are relative to; empty
	// is the working directory.
	Root string
}

// Services is every service, in the order the make targets process them.
var Services = []Service{
	{
		Name:      "emby",
		Package:   "emby",
		Output:    "lib/emby",
		Naming:    PathNaming,
		TagSuffix: "Service",
		Auth:      "Emby",
		KeepNull:  mediaServerKeepNull,
	},
	{
		Name:     "jellyfin",
		Package:  "jf",
		Output:   "lib/jf",
		Naming:   OperationIDNaming,
		Auth:     "Jellyfin",
		KeepNull: mediaServerKeepNull,
	},
	{
		Name:    "tmdb",
		Package: "tmdb",
		Output:  "lib/tmdb",
		Naming:  OperationIDNaming,
		Auth:    "TMDB",
	},
}

// mediaServerKeepNull are the fields both servers leave out for "none" where
// 0 means something: an episode filed with no season or no number of its own
// (Jellyfin reads a file named without SxxEyy that way even in a "Season 01"
// folder, and Emby one at the show's root) is not a special, nor episode 0,
// and an account with no parental limit is not one held to the ratings
// scored 0 (Jellyfin scores G and TV-G 0).
var mediaServerKeepNull = []string{"BaseItemDto.ParentIndexNumber", "BaseItemDto.IndexNumber", "UserPolicy.MaxParentalRating"}

// Select returns the named services, or all of them for an empty list.
func Select(names string) ([]Service, error) {
	if names == "" {
		return Services, nil
	}
	var out []Service
	for name := range strings.SplitSeq(names, ",") {
		svc, ok := Find(strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("unknown service %q (have %s)", name, strings.Join(serviceNames(), ", "))
		}
		out = append(out, svc)
	}

	return out, nil
}

// Find returns a service by name.
func Find(name string) (Service, bool) {
	for _, s := range Services {
		if s.Name == name {
			return s, true
		}
	}

	return Service{}, false
}

// In returns a copy of the service whose files are under root. The paths
// stay as configured, relative to the repository, because they are recorded
// in the definitions and the generated docs; Path resolves them.
func (s Service) In(root string) Service {
	s.Root = root

	return s
}

// Path resolves one of the service's paths under its root.
func (s Service) Path(p string) string { return filepath.Join(s.Root, p) }

// Resolve finds the service's documents under api-defs/ and settles on the
// highest version: Spec is that document and Definitions its definitions
// directory. A service with no document is an error.
func (s Service) Resolve() (Service, error) {
	versions, err := s.Versions()
	if err != nil {
		return s, err
	}
	if len(versions) == 0 {
		return s, fmt.Errorf("%s: no document api-defs/%s-openapi-<version>.json under %q", s.Name, s.Name, cmp.Or(s.Root, "."))
	}
	s.Version = versions[len(versions)-1]
	s.Spec = SpecPath(s.Name, s.Version)
	s.Definitions = DefinitionsPath(s.Name, s.Version)

	return s, nil
}

// Versions lists the versions of the service's vendored documents, lowest
// first.
func (s Service) Versions() ([]string, error) {
	matches, err := filepath.Glob(s.Path(SpecPath(s.Name, "*")))
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		versions = append(versions, strings.TrimSuffix(strings.TrimPrefix(base, s.Name+"-openapi-"), ".json"))
	}
	slices.SortFunc(versions, CompareVersions)

	return versions, nil
}

// SpecPath is where a service's document of one version is vendored.
func SpecPath(name, version string) string {
	return filepath.Join("api-defs", name+"-openapi-"+version+".json")
}

// DefinitionsPath is where one version's definitions are checked in.
func DefinitionsPath(name, version string) string {
	return filepath.Join("api-defs", name+"-"+version)
}

// CompareVersions orders two version strings by their dot-separated parts,
// numerically where both parts are numbers (4.10 above 4.9), otherwise as
// text; a version with more parts is above its prefix.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(as), len(bs)) {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		var c int
		if aErr == nil && bErr == nil {
			c = cmp.Compare(an, bn)
		} else {
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}

	return cmp.Compare(len(as), len(bs))
}

func serviceNames() []string {
	out := make([]string, 0, len(Services))
	for _, s := range Services {
		out = append(out, s.Name)
	}

	return out
}

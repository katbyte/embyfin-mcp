package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/katbyte/embyfin-mcp/sdk/embyfin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// librarySettingsFor picks the libraries asked for, by name or id, in the
// server's order; none asked for is every library.
func librarySettingsFor(all []embyfin.LibrarySettings, asked []string) ([]embyfin.LibrarySettings, error) {
	if len(asked) == 0 {
		return all, nil
	}

	var picked []embyfin.LibrarySettings
	for _, want := range asked {
		want = strings.TrimSpace(want)
		at := slices.IndexFunc(all, func(l embyfin.LibrarySettings) bool { return l.ID == want || strings.EqualFold(l.Name, want) })
		if at < 0 {
			names := make([]string, 0, len(all))
			for _, l := range all {
				names = append(names, l.Name)
			}

			return nil, fmt.Errorf("no library named %q or with that id: the server has %s", want, strings.Join(names, ", "))
		}
		if !slices.ContainsFunc(picked, func(l embyfin.LibrarySettings) bool { return l.ID == all[at].ID }) {
			picked = append(picked, all[at])
		}
	}
	slices.SortStableFunc(picked, func(a, b embyfin.LibrarySettings) int {
		return cmp.Compare(slices.IndexFunc(all, func(l embyfin.LibrarySettings) bool { return l.ID == a.ID }), slices.IndexFunc(all, func(l embyfin.LibrarySettings) bool { return l.ID == b.ID }))
	})

	return picked, nil
}

// optionApplies reports whether a setting is one a library could have: any
// setting but one kept for a kind of item (Movie.MetadataFetchers), which
// only a library of the same kind as one that has it could.
func optionApplies(option string, l *embyfin.LibrarySettings, have []*embyfin.LibrarySettings) bool {
	if !strings.Contains(option, ".") {
		return true
	}

	return slices.ContainsFunc(have, func(h *embyfin.LibrarySettings) bool { return h.CollectionType == l.CollectionType })
}

func registerLibraryOptionsTool(r *registry) {
	client := r.client

	type optionsIn struct {
		Libraries  []string `json:"libraries,omitempty"   jsonschema:"the libraries to compare, by name or id. Empty compares every library"`
		Options    []string `json:"options,omitempty"     jsonschema:"keep only the settings whose name holds one of these, case ignored: trickplay, chapter, SaveLocal, Fetchers. Empty gives every setting"`
		Differing  bool     `json:"differing,omitempty"   jsonschema:"leave out the settings every library compared has alike"`
		DifferFrom string   `json:"differ_from,omitempty" jsonschema:"a library, by name or id, to hold the others against: the answer then says, for each other library, which settings it has differently"`
	}
	type libraryRef struct {
		Name string `json:"name"`
		ID   string `json:"id"`
		Type string `json:"type,omitempty"`
	}
	type optionRow struct {
		Option string         `json:"option"           jsonschema:"the server's own name for the setting. One kept for each kind of item in a library is <Kind>.<Setting>: Movie.MetadataFetchers"`
		Value  any            `json:"value,omitempty"  jsonschema:"what every library that has the setting has it as"`
		Only   []string       `json:"only,omitempty"   jsonschema:"the libraries that have the setting, when not every one compared does: a setting for a kind of item is had only by the libraries of that kind"`
		Values map[string]any `json:"values,omitempty" jsonschema:"set in place of value when the libraries differ: what each has, by library name"`
		Unset  []string       `json:"unset,omitempty"  jsonschema:"with values: the libraries that could have the setting and have no value for it, so that whatever the server does when nothing is set applies to them. On Jellyfin a library with no fetchers listed for a kind of item uses every one the server has on, and one with an empty list uses none"`
	}
	type differRow struct {
		Library string   `json:"library"`
		Differs []string `json:"differs" jsonschema:"the settings this library has differently: another value, or a value where the other has none. A setting for a kind of item is held against a library of the same kind only. Empty when it has every one alike"`
	}
	type differOut struct {
		Library string      `json:"library" jsonschema:"the library the others are held against"`
		Others  []differRow `json:"others"`
	}
	type optionsOut struct {
		Backend    string       `json:"backend"`
		Libraries  []libraryRef `json:"libraries"             jsonschema:"the libraries compared"`
		Alike      int          `json:"alike"                 jsonschema:"how many settings every library that has them has alike, listed or not"`
		Differing  int          `json:"differing"             jsonschema:"how many they have differently"`
		Options    []optionRow  `json:"options"               jsonschema:"the settings, the ones that differ first"`
		DifferFrom *differOut   `json:"differ_from,omitempty" jsonschema:"with differ_from: each other library and what it has differently"`
		Note       string       `json:"note,omitempty"`
	}
	add(r, readTool, &mcp.Tool{
		Name: "library_options",
		Description: "The settings the server keeps for each library, side by side: one row a setting, with the value every library has it as, or each library's own where they differ. It answers what a library is set to do - save metadata beside the media, extract chapter images, make trickplay or thumbnail images and whether during a scan, fetch subtitles, which fetchers each kind of item uses and in what order - and which libraries are set differently from the rest, or from one named with differ_from. " +
			"Every setting the server sends is listed, by its own name, so Emby and Jellyfin differ in what they list; a library's folders and its kind are not among them (libraries has the kind, library_get the folders). Narrow a long answer with options, or with differing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in optionsIn) (*mcp.CallToolResult, optionsOut, error) {
		all, err := client.LibrarySettings(ctx)
		if err != nil {
			return nil, optionsOut{}, err
		}
		if len(all) == 0 {
			return nil, optionsOut{}, errors.New("the server has no libraries")
		}
		libraries, err := librarySettingsFor(all, in.Libraries)
		if err != nil {
			return nil, optionsOut{}, err
		}
		var against *embyfin.LibrarySettings
		if strings.TrimSpace(in.DifferFrom) != "" {
			one, err := librarySettingsFor(all, []string{in.DifferFrom})
			if err != nil {
				return nil, optionsOut{}, fmt.Errorf("differ_from: %w", err)
			}
			against = &one[0]
			// the one the others are held against is compared whether or not it was named among them
			if !slices.ContainsFunc(libraries, func(l embyfin.LibrarySettings) bool { return l.ID == against.ID }) {
				named := make([]string, 0, len(libraries)+1)
				for _, l := range libraries {
					named = append(named, l.ID)
				}
				if libraries, err = librarySettingsFor(all, append(named, against.ID)); err != nil {
					return nil, optionsOut{}, err
				}
			}
		}

		var wanted []string
		for _, w := range in.Options {
			if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
				wanted = append(wanted, w)
			}
		}
		keep := func(option string) bool {
			return len(wanted) == 0 || slices.ContainsFunc(wanted, func(w string) bool { return strings.Contains(strings.ToLower(option), w) })
		}

		out := optionsOut{Backend: string(client.Backend()), Options: []optionRow{}}
		names := map[string]bool{}
		for _, l := range libraries {
			out.Libraries = append(out.Libraries, libraryRef{Name: l.Name, ID: l.ID, Type: l.CollectionType})
			for option := range l.Options {
				// Emby lists a library's kind among its settings: it is in libraries, and no library of another kind has it alike
				if option != "ContentType" {
					names[option] = keep(option)
				}
			}
		}

		var alike, differing []optionRow
		for option, kept := range names {
			if !kept {
				continue
			}
			var have []*embyfin.LibrarySettings
			for i := range libraries {
				if _, ok := libraries[i].Options[option]; ok {
					have = append(have, &libraries[i])
				}
			}
			// a library that could have the setting and has no value for it differs from one that has
			var holders, unset []string
			values := map[string]any{}
			same := true
			for i := range libraries {
				l := &libraries[i]
				v, ok := l.Options[option]
				switch {
				case ok:
					same = same && reflect.DeepEqual(have[0].Options[option], v)
					holders = append(holders, l.Name)
					values[l.Name] = v
				case optionApplies(option, l, have):
					unset = append(unset, l.Name)
				}
			}

			if !same || len(unset) > 0 {
				differing = append(differing, optionRow{Option: option, Values: values, Unset: unset})

				continue
			}
			row := optionRow{Option: option, Value: have[0].Options[option]}
			if len(holders) < len(libraries) {
				row.Only = holders
			}
			alike = append(alike, row)
		}
		byName := func(a, b optionRow) int { return cmp.Compare(a.Option, b.Option) }
		slices.SortFunc(alike, byName)
		slices.SortFunc(differing, byName)
		out.Alike, out.Differing = len(alike), len(differing)
		out.Options = append(out.Options, differing...)
		if !in.Differing {
			out.Options = append(out.Options, alike...)
		}

		var notes []string
		if against != nil {
			out.DifferFrom = &differOut{Library: against.Name, Others: []differRow{}}
			for i := range libraries {
				l := &libraries[i]
				if l.ID == against.ID {
					continue
				}
				row := differRow{Library: l.Name, Differs: []string{}}
				for option, kept := range names {
					if !kept {
						continue
					}
					mine, has := l.Options[option]
					theirs, theyHave := against.Options[option]
					switch {
					case has && theyHave:
						if !reflect.DeepEqual(theirs, mine) {
							row.Differs = append(row.Differs, option)
						}
					case has && optionApplies(option, against, []*embyfin.LibrarySettings{l}), theyHave && optionApplies(option, l, []*embyfin.LibrarySettings{against}):
						row.Differs = append(row.Differs, option)
					}
				}
				slices.Sort(row.Differs)
				out.DifferFrom.Others = append(out.DifferFrom.Others, row)
			}
		}
		switch {
		case len(names) > 0 && len(alike)+len(differing) == 0:
			notes = append(notes, fmt.Sprintf("no setting's name holds any of %s: leave options empty to see every name", strings.Join(in.Options, ", ")))
		case len(libraries) == 1:
			notes = append(notes, "one library compared, so nothing differs: these are its settings")
		case in.Differing && len(differing) == 0:
			notes = append(notes, fmt.Sprintf("the %d libraries have every one of these %d settings alike", len(libraries), len(alike)))
		}
		out.Note = strings.Join(notes, "; ")

		return nil, out, nil
	})
}

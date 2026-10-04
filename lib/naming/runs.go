package naming

import (
	"regexp"
	"strings"
	"unicode"
)

// How a name spells a run of episodes, and which of those spellings the
// servers read as a run.
//
// A file holding two episodes is named a dozen ways: "S01E01-E02", the
// style Sonarr and the servers' own guides prefer; "S01E01E02"; "01x01-02";
// and odder ones ("01x01+02") the servers never read. A server that does not
// read a name's run lists the file as its first episode alone and calls the
// second missing, so what matters to a caller is not only what the name
// says but whether the server will see it.

// RunStyle is how a name spells its run of episodes, its numbers as NN, its
// letters one case, and cut after the first step so "S01E01-E02-E03" and
// "S01E04-E05" are both "SNNENN-ENN". "" for a name with no run.
func (r Release) RunStyle() string {
	if r.EpisodeEnd == 0 || r.Run == "" {
		return ""
	}

	return runStyle(r.Run)
}

// runStyleShape are the canonical marker and first step: the marker and step
// regexes with their digits as NN, over a run canonicalised by canonRun.
var runStyleShape = regexp.MustCompile(`^(?:SNN[. _x-]?ENN(?:[-+]x?E?|[. _x-]?E| - E| ?\+ ?E|[. _-]?SNN[. _x-]?E| - SNN[. _x-]?E)NN|S?NNxNN(?:x|[-+]S?NNx|[-+]x?E?| - (?:x|E|S?NNx)|[. _]S?NNx)NN)`)

// runStyle is RunStyle over a marker as written.
func runStyle(run string) string {
	canon := canonRun(run)
	if m := runStyleShape.FindString(canon); m != "" {
		return m
	}

	return canon
}

// canonRun spells a marker one way: every run of digits NN, S and E upper
// case, x lower, every run of spaces one space.
func canonRun(run string) string {
	var b strings.Builder
	digits, space := false, false
	for _, r := range run {
		switch {
		case unicode.IsDigit(r):
			if !digits {
				b.WriteString("NN")
			}
			digits, space = true, false
			continue
		case unicode.IsSpace(r):
			if !space {
				b.WriteByte(' ')
			}
			digits, space = false, true
			continue
		case r == 's' || r == 'S':
			b.WriteByte('S')
		case r == 'e' || r == 'E':
			b.WriteByte('E')
		case r == 'x' || r == 'X':
			b.WriteByte('x')
		default:
			b.WriteRune(r)
		}
		digits, space = false, false
	}

	return b.String()
}

// Servers a run style can be asked about.
const (
	ServerEmby     = "emby"
	ServerJellyfin = "jellyfin"
)

// runStylesRead is which run styles each server reads as a run of episodes,
// by the style (RunStyle) and the server (ServerEmby, ServerJellyfin). A
// server missing from a style's entry has not been checked for it.
//
// Jellyfin's come from its naming rules (Emby.Naming's
// MultipleEpisodeExpressions) and Emby's from its naming guide
// (emby.media/support/articles/TV-Naming.html), each then held against what
// the server recorded for a file of every style in the live suite
// (TestRunStylesTheServerReads), which is what settles the ones the guide
// does not list, and corrects the one it lists wrongly. A server reads a
// style when the live suite saw it list the file as the whole run.
var runStylesRead = map[string]map[string]bool{
	// listed by Emby's guide, in Jellyfin's rules, and read by both
	"NNxNNxNN":      {ServerEmby: true, ServerJellyfin: true},
	"SNNxNNxNN":     {ServerEmby: true, ServerJellyfin: true},
	"SNNENNENN":     {ServerEmby: true, ServerJellyfin: true},
	"SNNENN-ENN":    {ServerEmby: true, ServerJellyfin: true},
	"NNxNN - NNxNN": {ServerEmby: true, ServerJellyfin: true},
	"NNxNN - xNN":   {ServerEmby: true, ServerJellyfin: true},
	// listed by Emby's guide and read by Emby; not in Jellyfin's rules
	"SNNxENNxENN":     {ServerEmby: true, ServerJellyfin: false},
	"NNxNN NNxNN":     {ServerEmby: true, ServerJellyfin: false}, //nolint:dupword // the style is the marker twice
	"SNNxNN.SNNxNN":   {ServerEmby: true, ServerJellyfin: false},
	"SNNxNN - SNNxNN": {ServerEmby: true, ServerJellyfin: false},
	// listed by Emby's guide as "S01E02-X03", and not read by Emby (4.10 in
	// the live suite): the one place the guide and the server part
	"SNNENN-xNN": {ServerEmby: false, ServerJellyfin: true},
	// in Jellyfin's rules, not in Emby's guide, and read by both all the same
	"NNxNN-NN":     {ServerEmby: true, ServerJellyfin: true},
	"NNxNN-xNN":    {ServerEmby: true, ServerJellyfin: true},
	"NNxNN-NNxNN":  {ServerEmby: true, ServerJellyfin: true},
	"SNNENN - ENN": {ServerEmby: true, ServerJellyfin: true},
	"SNN.ENN-ENN":  {ServerEmby: true, ServerJellyfin: true},
	// in Jellyfin's rules and read by Jellyfin alone: Emby reads a bare
	// number after a hyphen only behind the NNxNN marker, not SxxExx
	"SNNENN-NN": {ServerEmby: false, ServerJellyfin: true},
	// in neither guide nor rules: Emby reads the SxxExx marker again after
	// a dot or a spaced dash, Jellyfin does not
	"SNNENN.SNNENN":   {ServerEmby: true, ServerJellyfin: false},
	"SNNENN - SNNENN": {ServerEmby: true, ServerJellyfin: false},
	// a plus joins nothing on either server
	"NNxNN+NN":    {ServerEmby: false, ServerJellyfin: false},
	"NNxNN+NNxNN": {ServerJellyfin: false},
	"SNNENN+ENN":  {ServerEmby: false, ServerJellyfin: false},
	"SNNENN+NN":   {ServerEmby: false, ServerJellyfin: false},
}

// RunStyleRead says whether a server reads a run style (RunStyle) as a run
// of episodes, and whether that is known: a style, or a server, nobody has
// checked is not known, which is not the same as not read.
func RunStyleRead(style, server string) (read, known bool) {
	read, known = runStylesRead[style][server]

	return read, known
}

// RunStyles lists every run style the servers have been checked for, for a
// test to hold against them.
func RunStyles() []string {
	out := make([]string, 0, len(runStylesRead))
	for style := range runStylesRead {
		out = append(out, style)
	}

	return out
}

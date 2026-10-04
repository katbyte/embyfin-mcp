// Package mediapath reads the paths a media server answers with, which are
// the server's own and not this machine's: a server on Windows answers with
// backslashes and drive letters whatever embyfin-mcp runs on, and
// path/filepath would read "C:\Films\Alien (1979)\Alien.mkv" as one long
// name. Every function here takes either separator as it comes and keeps it,
// so a path is compared, split and rejoined as the server wrote it.
//
// Nothing here resolves a path: "." and ".." are left as written (HasDots
// says when a path holds them), case is kept, and a path is never read off
// the disk. The tools compare paths as the server spells them, and ask the
// server itself when the disk has to be asked.
package mediapath

import "strings"

// IsSep says whether a byte is a path separator, either kind.
func IsSep(b byte) bool { return b == '/' || b == '\\' }

// IsTop is the top of a filesystem - /, C:\, or \\host\share - which has no
// folder above it and is never a folder a tool reports or cleans.
func IsTop(p string) bool {
	switch {
	case p == "/":
		return true
	case len(p) == 3 && p[1] == ':' && IsSep(p[2]):
		return true
	case strings.HasPrefix(p, `\\`):
		return strings.Count(strings.TrimSuffix(p[2:], `\`), `\`) <= 1
	}

	return false
}

// Trim drops a path's trailing separators, keeping the one that is a whole
// top: "/data/films/" is "/data/films", and "/" stays "/".
func Trim(p string) string {
	for len(p) > 1 && IsSep(p[len(p)-1]) && !IsTop(p) {
		p = p[:len(p)-1]
	}

	return p
}

// Dir is the folder holding p, or "" when p is a filesystem's top, or holds
// no separator at all.
func Dir(p string) string {
	p = Trim(p)
	if IsTop(p) {
		return ""
	}

	i := strings.LastIndexAny(p, `/\`)
	switch {
	case i < 0:
		return ""
	case i == 0:
		return p[:1]
	case i == 2 && p[1] == ':':
		return p[:3]
	}

	return p[:i]
}

// Base is the last segment of a path, either separator: the file's or the
// folder's own name. A filesystem's top (/, C:\) has no name, and is "".
func Base(p string) string {
	p = Trim(p)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}

	return p
}

// Within says whether path is root or inside it, a whole path segment at a
// time: /data/docs is not inside /data/doc. An empty root holds nothing.
func Within(path, root string) bool {
	path, root = Trim(path), Trim(root)
	switch {
	case root == "" || !strings.HasPrefix(path, root):
		return false
	case len(path) == len(root):
		return true
	}

	return IsSep(root[len(root)-1]) || IsSep(path[len(root)])
}

// Inside says whether path is strictly inside root: Within, and not the root
// itself.
func Inside(path, root string) bool {
	return Within(path, root) && Trim(path) != Trim(root)
}

// Clean is a path as written with its trailing separators dropped and every
// run of a separator made one: what two paths are compared as (Same). It
// resolves nothing: "." and ".." stay, as does the separator used.
func Clean(p string) string {
	return collapse(Trim(p))
}

// Same says whether two paths name one place as written: the same once
// trailing separators are dropped and runs of a separator are one. A path
// spelled with the other separator, or stepping through . or .., is another
// path here; only the server could say otherwise.
func Same(a, b string) bool {
	return Clean(a) == Clean(b)
}

// collapse folds every run of one separator into one of it: "a//b" is "a/b".
func collapse(p string) string {
	if !strings.Contains(p, "//") && !strings.Contains(p, `\\`) {
		return p
	}
	var b strings.Builder
	b.Grow(len(p))
	for i := range len(p) {
		// a UNC path's leading \\ names the host, and is kept
		if i > 0 && IsSep(p[i]) && p[i] == p[i-1] && (i != 1 || p[0] != '\\') {
			continue
		}
		b.WriteByte(p[i])
	}

	return b.String()
}

// ChildOf is the entry directly inside folder that path is or is under,
// spelled with the path's own separator, or "" for the folder itself. path
// must be within folder.
func ChildOf(folder, path string) string {
	folder, path = Trim(folder), Trim(path)
	if len(path) <= len(folder) {
		return ""
	}
	rest, sep := path[len(folder):], ""
	// a filesystem's top already ends in its separator
	if !IsSep(folder[len(folder)-1]) {
		sep, rest = rest[:1], rest[1:]
	}
	if i := strings.IndexAny(rest, `/\`); i >= 0 {
		rest = rest[:i]
	}

	return folder + sep + rest
}

// OnDisk says whether a path names a place on a filesystem, rather than a URL
// or a name the server made up: only a place can be checked, or left behind.
func OnDisk(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) > 2 && p[1] == ':' && IsSep(p[2]))
}

// HasDots says whether a path steps through . or .., which only the server
// would resolve: every check here compares paths as they are written.
func HasDots(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == "." || seg == ".." {
			return true
		}
	}

	return false
}

// Depth is how many folders deep a path is: how many separators it holds
// once its trailing ones are dropped.
func Depth(p string) int {
	p = Trim(p)

	return strings.Count(p, "/") + strings.Count(p, `\`)
}

// Ext is the extension of a path's last segment with its dot, or "" when the
// segment has no dot past its first character. It reads the segment, so a
// dot in a folder's name above it ("Mr. Robot/S01E01") is not one.
func Ext(p string) string {
	base := Base(p)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return base[i:]
	}

	return ""
}

// Stem is a path's last segment without its extension (see Ext).
func Stem(p string) string {
	base := Base(p)

	return strings.TrimSuffix(base, Ext(base))
}

package mediapath

import "testing"

func TestWithinAndInside(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path, root     string
		within, inside bool
	}{
		{"/data/doc/A (2020)/A (2020).mkv", "/data/doc", true, true},
		{"/data/doc", "/data/doc", true, false},
		{"/data/doc", "/data/doc/", true, false},
		{"/data/doc/", "/data/doc", true, false},
		{"/data/docs/A (2020)/A (2020).mkv", "/data/doc", false, false},
		{"/data/doc2/A.mkv", "/data/doc", false, false},
		{"/data/do", "/data/doc", false, false},
		{"/anything", "/", true, true},
		{"/", "/", true, false},
		{`D:\Video\Docs\A.mkv`, `D:\Video\Docs`, true, true},
		{`D:\Video\Docs2\A.mkv`, `D:\Video\Docs`, false, false},
		{`D:\Video`, `D:\`, true, true},
		{`\\nas\video\docs\A.mkv`, `\\nas\video`, true, true},
		{"/data/films", "", false, false},
		{"", "/data", false, false},
	} {
		if got := Within(tc.path, tc.root); got != tc.within {
			t.Errorf("Within(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.within)
		}
		if got := Inside(tc.path, tc.root); got != tc.inside {
			t.Errorf("Inside(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.inside)
		}
	}
}

func TestDirBaseAndTop(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ path, dir, base string }{
		{"/data/doc/", "/data", "doc"},
		{"/data/doc/A (2020).mkv", "/data/doc", "A (2020).mkv"},
		{"/mnt", "/", "mnt"},
		{"/", "", ""},
		{`C:\Video`, `C:\`, "Video"},
		{`C:\`, "", ""},
		{`C:\Video\A.mkv`, `C:\Video`, "A.mkv"},
		{`\\nas\video\docs`, `\\nas\video`, "docs"},
		{`\\nas\video`, "", "video"},
		{"name.mkv", "", "name.mkv"},
		{"", "", ""},
	} {
		if got := Dir(tc.path); got != tc.dir {
			t.Errorf("Dir(%q) = %q, want %q", tc.path, got, tc.dir)
		}
		if got := Base(tc.path); got != tc.base {
			t.Errorf("Base(%q) = %q, want %q", tc.path, got, tc.base)
		}
	}

	for p, want := range map[string]bool{"/": true, `C:\`: true, `c:/`: true, `\\nas\video`: true, `\\nas\video\docs`: false, "/data": false, "": false} {
		if IsTop(p) != want {
			t.Errorf("IsTop(%q) = %v", p, !want)
		}
	}
}

func TestTrimAndDepth(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path, trimmed string
		depth         int
	}{
		{"/data/films/", "/data/films", 2},
		{"/data/films//", "/data/films", 2},
		{"/", "/", 1},
		{`C:\Films\`, `C:\Films`, 1},
		{`C:\`, `C:\`, 1},
		{"/a.mkv", "/a.mkv", 1},
		{"a.mkv", "a.mkv", 0},
	} {
		if got := Trim(tc.path); got != tc.trimmed {
			t.Errorf("Trim(%q) = %q, want %q", tc.path, got, tc.trimmed)
		}
		if got := Depth(tc.path); got != tc.depth {
			t.Errorf("Depth(%q) = %d, want %d", tc.path, got, tc.depth)
		}
	}
}

func TestSame(t *testing.T) {
	t.Parallel()

	for p, want := range map[string]string{"/data/films/": "/data/films", "/data//films": "/data/films", `C:\Films\\A.mkv`: `C:\Films\A.mkv`, "/": "/", "/data/./a": "/data/./a"} {
		if got := Clean(p); got != want {
			t.Errorf("Clean(%q) = %q, want %q", p, got, want)
		}
	}

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"/data/films/A.mkv", "/data/films/A.mkv", true},
		{"/data/films/", "/data/films", true},
		{"/data//films/A.mkv", "/data/films/A.mkv", true},
		{`C:\Films\\A.mkv`, `C:\Films\A.mkv`, true},
		{`\\nas\films\A.mkv`, `\\nas\films\A.mkv`, true},
		{"/data/films/A.mkv", "/data/films/a.mkv", false},
		{"/data/films/A.mkv", `\data\films\A.mkv`, false},
		{"/data/films/./A.mkv", "/data/films/A.mkv", false},
		{"", "", true},
	} {
		if got := Same(tc.a, tc.b); got != tc.want {
			t.Errorf("Same(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestChildOf(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ folder, path, want string }{
		{"/data", "/data/films/A (2020)/A.mkv", "/data/films"},
		{"/data/", "/data/films/A.mkv", "/data/films"},
		{"/data", "/data/films", "/data/films"},
		{"/data", "/data", ""},
		{"/", "/films/A.mkv", "/films"},
		{`C:\`, `C:\Films\A.mkv`, `C:\Films`},
		{`D:\Video`, `D:\Video\Docs\A.mkv`, `D:\Video\Docs`},
	} {
		if got := ChildOf(tc.folder, tc.path); got != tc.want {
			t.Errorf("ChildOf(%q, %q) = %q, want %q", tc.folder, tc.path, got, tc.want)
		}
	}
}

func TestOnDiskAndDots(t *testing.T) {
	t.Parallel()

	for p, want := range map[string]bool{"/data/a.mkv": true, `C:\a.mkv`: true, `\\nas\a.mkv`: true, "https://example.org/t.mp4": false, "": false, "a.mkv": false} {
		if OnDisk(p) != want {
			t.Errorf("OnDisk(%q) = %v", p, !want)
		}
	}
	for p, want := range map[string]bool{"/data/./a.mkv": true, "/data/../a.mkv": true, `C:\..\a.mkv`: true, "/data/.hidden/a.mkv": false, "/data/a..mkv": false, "/data/a.mkv": false} {
		if HasDots(p) != want {
			t.Errorf("HasDots(%q) = %v", p, !want)
		}
	}
}

func TestExtAndStem(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ path, ext, stem string }{
		{"/data/films/Alien (1979).mkv", ".mkv", "Alien (1979)"},
		{`C:\Films\Alien.1979.1080p.mkv`, ".mkv", "Alien.1979.1080p"},
		{"/data/Mr. Robot/S01E01", "", "S01E01"},
		{"/data/films/.hidden", "", ".hidden"},
		{"/data/films/Alien", "", "Alien"},
		{"", "", ""},
	} {
		if got := Ext(tc.path); got != tc.ext {
			t.Errorf("Ext(%q) = %q, want %q", tc.path, got, tc.ext)
		}
		if got := Stem(tc.path); got != tc.stem {
			t.Errorf("Stem(%q) = %q, want %q", tc.path, got, tc.stem)
		}
	}
}

package acceptance

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Files under the media tree the server's container reads, bind-mounted
// from root on this machine.

// MediaMkdir makes a directory under the bind-mounted media tree at root
// that the media server's own user can write in, and MediaWrite writes a
// file there. The mode asked of MkdirAll and WriteFile is filtered by the
// process umask, which on Linux leaves a directory nobody but the test can
// write to - so the server (uid 2 in Emby's image, root in Jellyfin's)
// cannot delete a file the test laid out, and item_delete fails. chmod is
// not filtered by the umask, so the mode asked for is the mode applied.
// Docker Desktop hides this by mapping every file to the container's user,
// which is why it only bites in CI.
func MediaMkdir(t *testing.T, root, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	for p := dir; strings.HasPrefix(p, root) && p != root; p = filepath.Dir(p) {
		if err := os.Chmod(p, 0o777); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
	}
}

// MediaWrite writes a file the media server's own user can write over or
// remove (see MediaMkdir).
func MediaWrite(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o666); err != nil { //nolint:gosec // the container reads it as another user
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil { //nolint:gosec // same
		t.Fatal(err)
	}
}

// CopyTree copies a folder under the media tree at root, and everything
// under it, to a new place.
func CopyTree(t *testing.T, root, src, dst string) {
	t.Helper()

	if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := filepath.Join(dst, strings.TrimPrefix(path, src))
		if d.IsDir() {
			MediaMkdir(t, root, to)

			return nil
		}
		raw, rerr := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
		if rerr != nil {
			return rerr
		}
		MediaWrite(t, to, raw)

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TreeOf reads every folder and file under root, a folder ending in "/", so
// a test can hold the disk to what it was. skip leaves those paths, and
// whatever is under them, out.
func TreeOf(t *testing.T, root string, skip ...string) map[string][]byte {
	t.Helper()

	out := map[string][]byte{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case slices.Contains(skip, path) && d.IsDir():
			return filepath.SkipDir
		case slices.Contains(skip, path):
		case d.IsDir():
			out[path+"/"] = nil
		default:
			raw, rerr := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
			if rerr != nil {
				return rerr
			}
			out[path] = raw
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return out
}

// SameTree fails for every path that changed, went or appeared between two
// reads of a tree (TreeOf), named without root.
func SameTree(t *testing.T, root string, before, after map[string][]byte) {
	t.Helper()

	for path, raw := range before {
		if now, ok := after[path]; !ok || !bytes.Equal(now, raw) {
			t.Errorf("%s changed or went", strings.TrimPrefix(path, root))
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s appeared", strings.TrimPrefix(path, root))
		}
	}
}

// FilesUnder reads every file under the folders, to hold them to later
// (StillOnDisk); folders with nothing under them fail the test.
func FilesUnder(t *testing.T, dirs ...string) map[string][]byte {
	t.Helper()

	out := map[string][]byte{}
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				raw, rerr := os.ReadFile(path) //nolint:gosec // a fixture under the test data dir
				if rerr != nil {
					t.Fatal(rerr)
				}
				out[path] = raw
			}

			return nil
		})
	}
	if len(out) == 0 {
		t.Fatalf("nothing on disk under %v", dirs)
	}

	return out
}

// StillOnDisk checks every file read by FilesUnder is where it was, as it
// was, after what the test did (after), naming a file without root.
func StillOnDisk(t *testing.T, root string, files map[string][]byte, after string) {
	t.Helper()

	for path, raw := range files {
		if now, err := os.ReadFile(path); err != nil || !bytes.Equal(now, raw) { //nolint:gosec // a fixture under the test data dir
			t.Errorf("%s went or changed with %s: %v", strings.TrimPrefix(path, root), after, err)
		}
	}
}

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/katbyte/embyfin-mcp/sdk/pandorest/services"
	"github.com/katbyte/embyfin-mcp/sdk/pandorest/workarounds"
	"github.com/katbyte/pandorest"
	"github.com/katbyte/pandorest/config"
	"github.com/katbyte/pandorest/generator"
)

// repoRoot is where the config's paths resolve from.
const repoRoot = "../.."

// resolvedServices is every service with its document and definitions
// settled to the highest version checked in.
func resolvedServices(t *testing.T) []config.Service {
	t.Helper()

	out := make([]config.Service, 0, len(services.All))
	for _, svc := range services.All {
		resolved, err := svc.In(repoRoot).Resolve()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, resolved)
	}

	return out
}

func runCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, log bytes.Buffer
	err = pandorest.Run(pandorest.Config{Services: services.All, Workarounds: workarounds.All}, args, &out, &log)

	return out.String(), log.String(), err
}

// The checked-in definitions match the vendored specs and the generated
// packages have every method: make apicheck, as a test.
func TestCheckRepository(t *testing.T) {
	t.Parallel()

	stdout, _, err := runCmd(t, "check", "-root", repoRoot, "-quiet")
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range resolvedServices(t) {
		if !strings.Contains(stdout, svc.Name+": ") || !strings.Contains(stdout, "every one has a method in "+filepath.Join(repoRoot, svc.Output)) {
			t.Errorf("check output lacks %s:\n%s", svc.Name, stdout)
		}
	}
	stdout, _, err = runCmd(t, "diff", "-root", repoRoot, "-quiet", "-exit-code")
	if err != nil || strings.Count(stdout, ": no changes") != len(services.All) {
		t.Errorf("diff against the specs = %v:\n%s", err, stdout)
	}
}

// import then generate in a scratch copy of the repository reproduces the
// checked-in definitions and packages byte for byte: make gencheck, as a test.
func TestImportGenerateReproduces(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolved := resolvedServices(t)
	for _, svc := range resolved {
		src, err := os.ReadFile(filepath.Join(repoRoot, svc.Spec))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, svc.Spec)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, svc.Spec), src, 0o600); err != nil { //nolint:gosec // the config's own spec path under the test's temp dir
			t.Fatal(err)
		}
	}

	if _, stderr, err := runCmd(t, "import", "-root", root); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(stderr, "applied workaround emby-undeclared-responses") {
		t.Errorf("import did not log its workarounds:\n%s", stderr)
	}
	if _, _, err := runCmd(t, "generate", "-root", root); err != nil {
		t.Fatal(err)
	}

	for _, svc := range resolved {
		for _, dir := range []string{svc.Definitions, svc.Output} {
			// (hand-written tests beside the generated code are not regenerated)
			want := listFiles(t, filepath.Join(repoRoot, dir), dir == svc.Output)
			got := listFiles(t, filepath.Join(root, dir), false)
			if len(got) != len(want) {
				t.Errorf("%s: %d files regenerated, %d checked in", dir, len(got), len(want))
			}
			for name, content := range want {
				if got[name] != content {
					t.Errorf("%s/%s differs from a fresh generation; run make generate", dir, name)
				}
			}
		}
	}

	// the diff between two copies of the same definitions is empty
	stdout, _, err := runCmd(t, "diff", "-old", filepath.Join(repoRoot, resolved[0].Definitions), "-new", filepath.Join(root, resolved[0].Definitions), "-exit-code")
	if err != nil || !strings.Contains(stdout, "no changes") {
		t.Errorf("diff -old -new = %v:\n%s", err, stdout)
	}
	if err := os.Remove(filepath.Join(root, resolved[0].Definitions, "Collection.json")); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runCmd(t, "diff", "-old", filepath.Join(repoRoot, resolved[0].Definitions), "-new", filepath.Join(root, resolved[0].Definitions), "-exit-code")
	if !errors.Is(err, pandorest.ErrChanges) || !strings.Contains(stdout, "- operation PostCollections (POST /Collections) [breaking]") {
		t.Errorf("diff with a group gone = %v:\n%s", err, stdout)
	}
}

// listFiles reads a directory's files; generatedOnly skips the hand-written
// ones (no generated header) that live beside generated code.
func listFiles(t *testing.T, dir string, generatedOnly bool) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a directory under test
		if err != nil {
			t.Fatal(err)
		}
		if generatedOnly && !generator.Generated(b) {
			continue
		}
		out[e.Name()] = string(b)
	}

	return out
}

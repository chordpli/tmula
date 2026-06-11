// Package repometa holds tests for repo-level metadata that ships to users:
// the LICENSE file, the README license section, and the composite GitHub
// Action's supply-chain posture. These files live at the repo root, outside
// any Go package, so the checks live here to keep them inside `make test`.
package repometa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the repository root relative to this package
// (server/internal/repometa → three levels up).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod: %v", root, err)
	}
	return root
}

func readRootFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestLicenseIsApache2 pins the LICENSE file to the canonical Apache-2.0 text.
func TestLicenseIsApache2(t *testing.T) {
	license := readRootFile(t, "LICENSE")

	for _, want := range []string{
		"Apache License",
		"Version 2.0, January 2004",
		"http://www.apache.org/licenses/",
		"Licensed under the Apache License, Version 2.0",
	} {
		if !strings.Contains(license, want) {
			t.Errorf("LICENSE missing canonical Apache-2.0 marker %q", want)
		}
	}
}

// TestReadmeLicenseSection makes sure the README's License section names
// Apache-2.0 instead of the old "TBD." placeholder. Only the section between
// the "## License" heading and the next heading/rule is inspected, so the
// rest of the README stays out of scope.
func TestReadmeLicenseSection(t *testing.T) {
	readme := readRootFile(t, "README.md")

	_, after, found := strings.Cut(readme, "## License")
	if !found {
		t.Fatal("README.md has no '## License' section")
	}
	section := after
	for _, stop := range []string{"\n## ", "\n---"} {
		if i := strings.Index(section, stop); i >= 0 {
			section = section[:i]
		}
	}
	if strings.Contains(section, "TBD") {
		t.Error("README License section still says TBD")
	}
	if !strings.Contains(section, "Apache-2.0") {
		t.Error("README License section does not name Apache-2.0")
	}
}

// TestActionInstallsPinnedScript guards the composite action against the
// curl-from-main supply-chain hole: the installer must come from the action's
// own checkout (which GitHub pins to the ref in `uses:`), never piped from
// the mutable main branch.
func TestActionInstallsPinnedScript(t *testing.T) {
	action := readRootFile(t, "action.yml")

	if strings.Contains(action, "raw.githubusercontent.com/chordpli/tmula/main/install.sh") {
		t.Error("action.yml pipes install.sh from the mutable main branch; run the bundled copy instead")
	}
	if !strings.Contains(action, `"$GITHUB_ACTION_PATH/install.sh"`) {
		t.Error(`action.yml should run the bundled installer via "$GITHUB_ACTION_PATH/install.sh"`)
	}
}

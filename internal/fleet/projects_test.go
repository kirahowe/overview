package fleet

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"pgregory.net/rapid"
)

func TestProjectsPinAndUnpin(t *testing.T) {
	h := Home(t.TempDir())
	if got := h.Projects(); len(got) != 0 {
		t.Fatalf("fresh home has projects %v", got)
	}
	for _, dir := range []string{"/work/b", "/work/a", "/work/b", "/work/c/../a/"} {
		if err := h.PinProject(dir); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := h.Projects(), []string{"/work/b", "/work/a"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if err := h.UnpinProject("/nowhere"); err != nil {
		t.Fatal(err)
	}
	if err := h.UnpinProject("/work/b/"); err != nil {
		t.Fatal(err)
	}
	if got, want := h.Projects(), []string{"/work/a"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestProjectsRelativeDirBecomesAbsolute(t *testing.T) {
	h := Home(t.TempDir())
	if err := h.PinProject("some/dir"); err != nil {
		t.Fatal(err)
	}
	got := h.Projects()
	if len(got) != 1 || !filepath.IsAbs(got[0]) || filepath.Base(got[0]) != "dir" {
		t.Fatalf("got %v", got)
	}
}

func TestProjectsReadIgnoresBlanksAndDuplicates(t *testing.T) {
	h := Home(t.TempDir())
	if err := os.MkdirAll(h.dataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "\n/a\n  \n/b\n/a\n\n"
	if err := os.WriteFile(h.projectsFile(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := h.Projects(), []string{"/a", "/b"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestProjectsLeaveNoTempFiles(t *testing.T) {
	h := Home(t.TempDir())
	if err := h.PinProject("/a"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(h.dataDir())
	if err != nil || len(entries) != 1 || entries[0].Name() != "projects" {
		t.Fatalf("data dir holds %v (err %v)", entries, err)
	}
}

func TestPropProjectsMatchAModel(t *testing.T) {
	dirs := []string{"/p/a", "/p/b", "/p/c", "/p/d", "/p/e/f"}
	root, n := t.TempDir(), 0
	rapid.Check(t, func(t *rapid.T) {
		h := newHome(root, &n)
		var model []string
		for _, pin := range rapid.SliceOf(rapid.Bool()).Draw(t, "pins") {
			dir := rapid.SampledFrom(dirs).Draw(t, "dir")
			if pin {
				if err := h.PinProject(dir); err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(model, dir) {
					model = append(model, dir)
				}
			} else {
				if err := h.UnpinProject(dir); err != nil {
					t.Fatal(err)
				}
				model = slices.DeleteFunc(model, func(d string) bool { return d == dir })
			}
			if got := h.Projects(); !slices.Equal(got, model) && (len(got) > 0 || len(model) > 0) {
				t.Fatalf("after %q: got %v, want %v", dir, got, model)
			}
		}
	})
}

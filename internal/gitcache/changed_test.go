package gitcache

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// testRepo is a throwaway Git repository that a Cache mirrors.
type testRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet", "--initial-branch=main")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes the given files ("" removes one) and returns the commit ID.
func (r *testRepo) commit(files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		path := filepath.Join(r.dir, filepath.FromSlash(name))
		if content == "" {
			if err := os.Remove(path); err != nil {
				r.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "--all")
	r.git("commit", "--quiet", "--message", "change")
	return r.git("rev-parse", "HEAD")
}

// mirror returns a Cache holding a mirror of the repository as it is now.
func (r *testRepo) mirror() *Cache {
	r.t.Helper()
	c, err := New(r.t.TempDir())
	if err != nil {
		r.t.Fatal(err)
	}
	if err := c.Sync(r.dir, ""); err != nil {
		r.t.Fatalf("Sync(): %v", err)
	}
	return c
}

func sorted(files []string) []string {
	sort.Strings(files)
	return files
}

func TestChangedSince(t *testing.T) {
	repo := newTestRepo(t)
	first := repo.commit(map[string]string{
		"README.md":            "readme",
		"services/api/main.go": "package main",
		"services/web/app.ts":  "export {}",
		"old name.txt":         "content that is long enough to be detected as a rename",
	})
	second := repo.commit(map[string]string{"services/api/main.go": "package main // v2"})
	repo.git("mv", "old name.txt", "new name.txt")
	third := repo.commit(map[string]string{
		"services/web/app.ts": "", // removed
		"docs/guide.md":       "guide",
	})
	cache := repo.mirror()

	t.Run("one commit", func(t *testing.T) {
		got, err := cache.ChangedSince(repo.dir, first, second)
		if err != nil {
			t.Fatalf("ChangedSince(): %v", err)
		}
		if want := []string{"services/api/main.go"}; !reflect.DeepEqual(got, want) {
			t.Errorf("changed = %v; want %v", got, want)
		}
	})

	t.Run("several commits, with a rename, a removal, and a path with a space", func(t *testing.T) {
		got, err := cache.ChangedSince(repo.dir, first, third)
		if err != nil {
			t.Fatalf("ChangedSince(): %v", err)
		}
		want := []string{"docs/guide.md", "new name.txt", "old name.txt", "services/api/main.go", "services/web/app.ts"}
		if !reflect.DeepEqual(sorted(got), want) {
			t.Errorf("changed = %v; want %v", got, want)
		}
	})

	t.Run("no difference is a known, empty result", func(t *testing.T) {
		got, err := cache.ChangedSince(repo.dir, third, third)
		if err != nil || len(got) != 0 {
			t.Errorf("ChangedSince(same, same) = %v, %v; want no files and no error", got, err)
		}
	})

	t.Run("nothing to compare against", func(t *testing.T) {
		bases := []string{
			"",
			"0000000000000000000000000000000000000000", // the branch was created by this push
			"1234567890abcdef1234567890abcdef12345678", // not in the repository, e.g. after a force push
			"--output=/tmp/x",
			"main",
		}
		for _, base := range bases {
			if _, err := cache.ChangedSince(repo.dir, base, third); !errors.Is(err, ErrNoBase) {
				t.Errorf("ChangedSince(base %q) = %v; want ErrNoBase", base, err)
			}
		}
	})

	t.Run("a head that is not a commit ID is an error", func(t *testing.T) {
		for _, head := range []string{"", "HEAD", "--output=/tmp/x"} {
			if _, err := cache.ChangedSince(repo.dir, first, head); err == nil || errors.Is(err, ErrNoBase) {
				t.Errorf("ChangedSince(head %q) = %v; want an error other than ErrNoBase", head, err)
			}
		}
	})
}

func TestChangedFromBranch(t *testing.T) {
	repo := newTestRepo(t)
	repo.commit(map[string]string{"README.md": "readme", "services/api/main.go": "package main"})

	repo.git("checkout", "--quiet", "-b", "feature/login")
	repo.commit(map[string]string{"services/web/login.ts": "export {}"})
	head := repo.commit(map[string]string{"services/web/login.css": "body {}"})

	// The target branch moves on after the feature branch was created. Its
	// changes are not part of the merge request.
	repo.git("checkout", "--quiet", "main")
	repo.commit(map[string]string{"services/api/main.go": "package main // hotfix"})
	cache := repo.mirror()

	got, err := cache.ChangedFromBranch(repo.dir, "main", head)
	if err != nil {
		t.Fatalf("ChangedFromBranch(): %v", err)
	}
	want := []string{"services/web/login.css", "services/web/login.ts"}
	if !reflect.DeepEqual(sorted(got), want) {
		t.Errorf("changed = %v; want only the feature branch's files %v", got, want)
	}

	for _, branch := range []string{"", "no-such-branch", "--all", "main..feature/login", "main:x"} {
		if _, err := cache.ChangedFromBranch(repo.dir, branch, head); !errors.Is(err, ErrNoBase) {
			t.Errorf("ChangedFromBranch(branch %q) = %v; want ErrNoBase", branch, err)
		}
	}
}

func TestChanged_RepositoryNotCached(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	head := "1234567890abcdef1234567890abcdef12345678"
	if _, err := c.ChangedSince("https://example.com/org/missing.git", head, head); err == nil {
		t.Error("ChangedSince on an uncached repository succeeded")
	}
}

func TestResolveBranchOrTag(t *testing.T) {
	repo := newTestRepo(t)
	first := repo.commit(map[string]string{"README.md": "one"})
	repo.git("tag", "v1.0.0")                                       // lightweight tag
	repo.git("tag", "--annotate", "--message", "release", "v1.1.0") // annotated tag
	repo.git("checkout", "--quiet", "-b", "release/1.2")
	second := repo.commit(map[string]string{"README.md": "two"})
	repo.git("tag", "both") // a tag ...
	repo.git("checkout", "--quiet", "main")
	repo.git("branch", "both") // ... and a branch of the same name, on another commit
	cache := repo.mirror()

	cases := []struct {
		name, wantRef, wantCommit string
	}{
		{"main", "refs/heads/main", first},
		{"release/1.2", "refs/heads/release/1.2", second},
		{"v1.0.0", "refs/tags/v1.0.0", first},
		{"v1.1.0", "refs/tags/v1.1.0", first}, // peeled to the commit, not the tag object
		{"refs/heads/release/1.2", "refs/heads/release/1.2", second},
		{"refs/tags/v1.0.0", "refs/tags/v1.0.0", first},
		{"both", "refs/heads/both", first}, // the branch wins
		{"refs/tags/both", "refs/tags/both", second},
	}
	for _, tc := range cases {
		ref, commit, err := cache.ResolveBranchOrTag(repo.dir, tc.name)
		if err != nil || ref != tc.wantRef || commit != tc.wantCommit {
			t.Errorf("ResolveBranchOrTag(%q) = %q, %q, %v; want %q, %q", tc.name, ref, commit, err, tc.wantRef, tc.wantCommit)
		}
	}

	for _, name := range []string{"", "no-such-branch", "refs/remotes/origin/main", "refs/", first, "HEAD"} {
		if _, _, err := cache.ResolveBranchOrTag(repo.dir, name); !errors.Is(err, ErrRefNotFound) {
			t.Errorf("ResolveBranchOrTag(%q) = %v; want ErrRefNotFound", name, err)
		}
	}
}

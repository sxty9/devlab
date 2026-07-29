package it

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/git"
	"devlab/backend/internal/links"
	"devlab/backend/internal/statepath"
)

// B-12 — the read-only git plumbing, against a REAL repository. The package shells out to git, so
// only a real repository proves it: tree, changes, branches, commits, worktrees, default branch,
// diff and file reading, plus the two guards that make reading safe (the size cap and the refusal
// to follow a path out of the repository).
func TestGitPlumbingOverARealRepository(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, "", "init", "--quiet", "--initial-branch=main", repo)
	gitRun(t, repo, "config", "user.name", "t")
	gitRun(t, repo, "config", "user.email", "t@t")

	writeIn(t, repo, "README.md", "# title\n\nfirst line\n")
	writeIn(t, repo, "src/app.ts", "export const a = 1;\n")
	writeIn(t, repo, "vision/sketch.png", "\x89PNG\r\n\x1a\nfake")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "--quiet", "-m", "first")

	if b := git.DefaultBranch(repo); b != "main" {
		t.Errorf("default branch = %q, want main", b)
	}

	tree := git.Tree(repo)
	if len(tree) == 0 {
		t.Fatal("the tree is empty over a repository with three files")
	}
	names := map[string]bool{}
	for _, n := range tree {
		names[n.Name] = true
	}
	for _, want := range []string{"README.md", "src", "vision"} {
		if !names[want] {
			t.Errorf("the tree does not carry %q (got %v)", want, keysOf(names))
		}
	}

	// A tracked file reads back with its language resolved.
	fc := git.FileAt(repo, "src/app.ts")
	if fc.Code != "export const a = 1;\n" {
		t.Errorf("file content = %q", fc.Code)
	}
	if fc.Lang != "typescript" {
		t.Errorf("language = %q, want typescript", fc.Lang)
	}

	// The 2-MiB editor cap: a bigger file is REFUSED with a named answer, never streamed.
	big := strings.Repeat("x", (2<<20)+1)
	writeIn(t, repo, "huge.txt", big)
	capped := git.FileAt(repo, "huge.txt")
	if strings.Contains(capped.Code, "xxxx") {
		t.Error("the size cap did not hold — a file beyond the cap was returned in full")
	}
	if !strings.Contains(capped.Code, "too large") {
		t.Errorf("the cap answer does not name the reason: %q", capped.Code)
	}

	// Binary content is named as binary rather than mangled into the editor.
	if bin := git.FileAt(repo, "vision/sketch.png"); !strings.Contains(bin.Code, "binary") {
		t.Errorf("binary file answer = %q", bin.Code)
	}

	// Reading out of the repository is refused — with a path and through a symlink.
	for _, escape := range []string{"../outside.txt", "../../etc/hostname"} {
		if out := git.FileAt(repo, escape); !strings.Contains(out.Code, "refused") && !strings.Contains(out.Code, "not found") {
			t.Errorf("reading %q was not refused: %q", escape, out.Code)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if leaked := git.FileAt(repo, "leak.txt"); strings.Contains(leaked.Code, "top secret") {
		t.Error("a committed symlink exfiltrated a file from outside the repository")
	}

	// The working tree's changes are reported: one modification, one new file.
	writeIn(t, repo, "README.md", "# title\n\nfirst line\nsecond line\n")
	writeIn(t, repo, "src/new.ts", "export const b = 2;\n")
	changes := git.Changes(repo)
	if len(changes) < 2 {
		t.Fatalf("changes = %d, want at least the modification and the new file (%+v)", len(changes), changes)
	}
	byPath := map[string]string{}
	for _, c := range changes {
		byPath[c.Path] = c.Status
	}
	if byPath["README.md"] == "" || byPath["src/new.ts"] == "" {
		t.Errorf("the change set does not name both files: %+v", byPath)
	}

	// The diff of the modification carries the added line.
	d := git.DiffFor(repo, "README.md")
	if !strings.Contains(d.After, "second line") {
		t.Errorf("the diff lost the change: %+v", d)
	}

	// Branches and commits come from the repository itself.
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "--quiet", "-m", "second")
	gitRun(t, repo, "branch", "feature/x")
	branches := git.Branches(repo)
	found := map[string]bool{}
	for _, b := range branches {
		found[b.Name] = true
	}
	if !found["main"] || !found["feature/x"] {
		t.Errorf("branches = %+v, want main and feature/x", branches)
	}
	commits := git.Commits(repo, "main", 10)
	if len(commits) != 2 {
		t.Fatalf("commits = %d, want 2", len(commits))
	}
	if commits[0].Message != "second" {
		t.Errorf("the newest commit is %q, want \"second\" (newest first)", commits[0].Message)
	}
	if commits[0].Hash == "" || commits[0].Author == "" {
		t.Errorf("a commit without hash or author: %+v", commits[0])
	}

	// The vision catalogue is derived from the repository, not from a store.
	vis := git.VisionFiles(repo)
	if len(vis) != 1 || !strings.HasSuffix(vis[0].Path, "sketch.png") {
		t.Errorf("vision files = %+v, want the one committed image", vis)
	}

	// A path that is not a repository must answer empty, never panic — the surface asks about
	// repositories that may have vanished.
	empty := t.TempDir()
	if len(git.Branches(empty)) != 0 || len(git.Commits(empty, "main", 5)) != 0 {
		t.Error("a directory that is no repository answered with data")
	}
	if git.DefaultBranch(empty) == "" {
		t.Log("a non-repository answers an empty default branch — acceptable, the caller checks")
	}
}

// B-14 — the per-user GitHub token is encrypted at rest, the key comes from a file, and the pool's
// permissions keep it to the service account. The identity itself stays the Linux account: the
// pool is keyed by username and holds no identity of its own.
func TestLinkedTokensAreEncryptedAtRestAndPrivate(t *testing.T) {
	root := t.TempDir()
	keyFile := filepath.Join(root, "link.key")
	if err := os.WriteFile(keyFile, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_LINK_ENC_KEY_FILE", keyFile)
	t.Setenv("DEVLAB_LINKS", "")
	paths := &statepath.Paths{Root: root}

	store, err := links.NewStore(paths)
	if err != nil {
		t.Fatal(err)
	}
	const token = "gho_thisIsTheSecretAccessToken"
	if err := store.Save("ada", "ada-gh", 4242, token, "repo,read:org", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	// The token round-trips…
	got, err := store.Token("ada")
	if err != nil || got != token {
		t.Fatalf("token round-trip: %q (%v)", got, err)
	}
	if !store.Linked("ada") || store.Linked("nobody-else") {
		t.Error("the link state is not answered per user")
	}
	link, err := store.Get("ada")
	if err != nil || link == nil || link.GHLogin != "ada-gh" {
		t.Fatalf("link record: %+v (%v)", link, err)
	}

	// …but the plaintext is nowhere on disk, and the pool is not world-readable.
	var files []string
	err = filepath.WalkDir(paths.Links(), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if fi, ferr := d.Info(); ferr == nil && fi.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s is mode %o — the link pool must not be readable beyond its owner", p, fi.Mode().Perm())
			}
			return nil
		}
		files = append(files, p)
		fi, ferr := d.Info()
		if ferr != nil {
			return ferr
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is mode %o — a token file must not be readable beyond its owner", p, fi.Mode().Perm())
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), token) {
			t.Errorf("%s carries the token in plaintext", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("nothing was written at all")
	}

	// A store with a DIFFERENT key cannot read the token: the secret is the key file, not the disk.
	otherKey := filepath.Join(root, "other.key")
	if err := os.WriteFile(otherKey, []byte("fedcba9876543210fedcba9876543210"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_LINK_ENC_KEY_FILE", otherKey)
	other, err := links.NewStore(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := other.Token("ada"); err == nil {
		t.Errorf("a foreign key decrypted the token (%q) — the pool is not encrypted at rest", got)
	}

	// Without a key the store refuses to open at all, rather than storing tokens in the clear.
	t.Setenv("DEVLAB_LINK_ENC_KEY_FILE", "")
	if _, err := links.NewStore(paths); err == nil {
		t.Error("the link store opened without an encryption key — tokens would sit in the clear")
	}

	// Deleting removes the record for that user only.
	t.Setenv("DEVLAB_LINK_ENC_KEY_FILE", keyFile)
	back, err := links.NewStore(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := back.Delete("ada"); err != nil {
		t.Fatal(err)
	}
	if back.Linked("ada") {
		t.Error("the link survived its deletion")
	}
}

func writeIn(t *testing.T, repo, rel, body string) {
	t.Helper()
	p := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

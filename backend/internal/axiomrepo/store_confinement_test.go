package axiomrepo

// Two security properties of the constitution store, each pinned by the attack it must survive:
// the push credential never becomes an argument (it would be world-readable in /proc), and a record
// path can never leave the clone through a symlink the repository itself carries.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubGit puts a fake `git` first on PATH. It records every argument it was handed and the token it
// found in its environment, then fails — the store's error path is uninteresting here; what the
// invocation LOOKED like is the whole point. Returns the two log paths.
func stubGit(t *testing.T) (argvLog, envLog string) {
	t.Helper()
	bin := t.TempDir()
	logs := t.TempDir()
	argvLog = filepath.Join(logs, "argv")
	envLog = filepath.Join(logs, "env")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + argvLog + "'; done\n" +
		"printf '%s\\n' \"${DEVLAB_GH_TOKEN-}\" > '" + envLog + "'\n" +
		"exit 128\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvLog, envLog
}

// TestCredentialNeverReachesTheCommandLine pins that the push token travels in the ENVIRONMENT.
// /proc/<pid>/cmdline is world-readable, so a token handed to git as `-c http.…extraheader=…` is
// exposed to every local account for as long as the command runs (a plain `ps auxww` shows it);
// /proc/<pid>/environ is readable by its owner alone. Both git-invoking paths are covered: the
// initial clone and every later command in the clone.
func TestCredentialNeverReachesTheCommandLine(t *testing.T) {
	const token = "ghp_ThisIsTheSecretPushToken0123456789"
	argvLog, envLog := stubGit(t)
	ctx := context.Background()

	// 1) the clone path — no .git in the work dir yet.
	cloneStore := New(filepath.Join(t.TempDir(), "work"), "owner/axioms",
		func() (string, error) { return token, nil })
	if _, err := cloneStore.List(ctx, ""); err == nil {
		t.Fatal("the stub git exits non-zero — List must report the failure honestly")
	}

	// 2) the in-clone path — a pre-existing .git makes ensure() skip the clone and run `git fetch`.
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(filepath.Join(work, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	inClone := New(work, "owner/axioms", func() (string, error) { return token, nil })
	if err := inClone.Put(ctx, "axiome/x.md", "body", "msg", "tester", false); err == nil {
		t.Fatal("the stub git exits non-zero — Put must report the failure honestly")
	}

	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("the stub git was never invoked: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(argv), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected both git invocations to be recorded, got %q", argv)
	}
	for i, arg := range lines {
		if strings.Contains(arg, token) {
			t.Errorf("argv[%d] carries the raw token — visible in `ps auxww` to every local user: %q", i, arg)
		}
		if strings.Contains(arg, basicAuth(token)) {
			t.Errorf("argv[%d] carries the base64 credential — equally world-readable: %q", i, arg)
		}
		if strings.Contains(arg, "Authorization") {
			t.Errorf("argv[%d] still builds the Authorization header on the command line: %q", i, arg)
		}
	}

	// The credential must actually have REACHED git — otherwise the test would also pass for a
	// store that simply lost its authentication.
	seen, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatalf("the stub git recorded no environment: %v", err)
	}
	if strings.TrimSpace(string(seen)) != token {
		t.Errorf("git saw %s=%q, want the token — the credential must travel in the environment",
			tokenEnvVar, strings.TrimSpace(string(seen)))
	}
	// …and git must have been told to ASK for it: the helper pointer is tokenless, so argv is fine.
	if !strings.Contains(string(argv), "credential.helper=!f()") {
		t.Errorf("git was never pointed at the inline credential helper: %q", argv)
	}
}

// plantEscape commits a symlink INSIDE the constitution repository that points out of it, and
// returns the outside directory. This is the hostile-content case: whoever can commit to the
// (deliberately unprotected) constitution repository can plant such a link.
func plantEscape(t *testing.T, s *Store, name, target string) {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "plant")
	run(t, "", "git", "clone", "--quiet", s.fullName, seed)
	if err := os.MkdirAll(filepath.Join(seed, "axiome"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(seed, "axiome", name)); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "plant "+name)
	run(t, seed, "git", "push", "--quiet", "origin", "HEAD:main")
}

// TestRecordPathCannotEscapeThroughSymlink pins the second half of the confinement. safePath is a
// string check: `axiome/escape/secret.md` contains no "..", is not absolute and does not start with
// ".git", so it passed — and every access then followed the link. A read served a file that is not a
// record (any file the service user can read), and a write landed outside the repository, where no
// commit and no history would ever show it.
func TestRecordPathCannotEscapeThroughSymlink(t *testing.T) {
	ctx := context.Background()
	s := newLocalStore(t)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("NOT A RECORD"), 0o600); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(outside, "not-there-yet")
	plantEscape(t, s, "escape", outside)
	plantEscape(t, s, "later", dangling)

	// A read THROUGH the link must fail — never serve foreign bytes as a record.
	data, found, err := s.Get(ctx, "axiome/escape/secret.md")
	if err == nil {
		t.Errorf("Get through the escaping link returned found=%v data=%q — it must refuse", found, data)
	}
	if strings.Contains(string(data), "NOT A RECORD") {
		t.Errorf("Get served a file from outside the repository: %q", data)
	}
	// The link itself is not a record either.
	if _, _, err := s.Get(ctx, "axiome/escape"); err == nil {
		t.Error("Get of the escaping link itself must refuse")
	}

	// A write THROUGH the link must fail and must leave nothing outside.
	if err := s.Put(ctx, "axiome/escape/planted.md", "PLANTED", "plant", "tester", true); err == nil {
		t.Error("Put through the escaping link must refuse")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.md")); err == nil {
		t.Error("Put wrote a file OUTSIDE the constitution repository")
	}
	// A DANGLING link is refused too: following it would land the write at the target the moment
	// the target appears.
	if err := s.Put(ctx, "axiome/later/planted.md", "PLANTED", "plant", "tester", true); err == nil {
		t.Error("Put through a dangling link must refuse")
	}
	if _, err := os.Stat(filepath.Join(dangling, "planted.md")); err == nil {
		t.Error("Put wrote through a dangling symlink, outside the constitution repository")
	}
	// Delete and Move take the same confinement.
	if err := s.Delete(ctx, "axiome/escape/secret.md", "del", "tester"); err == nil {
		t.Error("Delete through the escaping link must refuse")
	}
	if _, err := os.Stat(secret); err != nil {
		t.Errorf("Delete removed a file outside the repository: %v", err)
	}
	if err := s.Move(ctx, "axiome/escape/secret.md", "axiome/moved.md", "mv", "tester"); err == nil {
		t.Error("Move out of the escaping link must refuse")
	}

	// The confinement must not swallow ordinary records: a real one still round-trips.
	if err := s.Put(ctx, "axiome/ok.md", "OK", "write", "tester", false); err != nil {
		t.Fatalf("an ordinary record must still be writable: %v", err)
	}
	if b, found, err := s.Get(ctx, "axiome/ok.md"); err != nil || !found || string(b) != "OK" {
		t.Fatalf("ordinary record round-trip: b=%q found=%v err=%v", b, found, err)
	}
}

// A path segment named .git is refused wherever it sits, not just at the front: the string guard
// used to look at the prefix only.
func TestSafePathRejectsGitSegmentAnywhere(t *testing.T) {
	for _, p := range []string{".git/config", "axiome/.git/hooks/pre-commit", "axiome//x.md", "./x.md", ""} {
		if err := safePath(p); err == nil {
			t.Errorf("safePath(%q) = nil, want a refusal", p)
		}
	}
	for _, p := range []string{"README.md", "axiome/architecture/minimalism/keine-redundanz.md"} {
		if err := safePath(p); err != nil {
			t.Errorf("safePath(%q) = %v, want nil — ordinary records must pass", p, err)
		}
	}
}

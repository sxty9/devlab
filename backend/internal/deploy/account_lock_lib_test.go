package deploy

// Tests for the account-creation choke point in devlab-setup-lib.sh: creating a service account is
// serialised host-wide and, when the account database (/etc/passwd) is only MOMENTARILY locked by a
// concurrent writer, retried instead of read as a verdict. `useradd` names that condition "cannot lock
// /etc/passwd; try again later." — a busy signal, not a fault of the account. A single shared path
// (setup_account_cmd) carries this for every caller, so no site re-guesses what "momentary" means.
//
// The tests drive the library the way the real consumers do — by sourcing it — and stand a FAKE
// `useradd` on PATH so they exercise the create logic unprivileged and deterministically, without
// touching the host's real /etc/passwd.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin writes an executable script named tool into its own directory and returns that directory, to
// be prepended to PATH so the library resolves the fake instead of the real tool.
func fakeBin(t *testing.T, tool, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, tool)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// accountEnv builds the env for sourceLib: the fake tool on PATH, a temp lock file, and a fast, small
// retry budget so a persistently-locked case fails in well under a second instead of the 30s default.
func accountEnv(t *testing.T, fakeDir string, extra map[string]string) map[string]string {
	t.Helper()
	env := map[string]string{
		"PATH":                    fakeDir + ":" + os.Getenv("PATH"),
		"SETUP_ACCOUNT_LOCK":      filepath.Join(t.TempDir(), "account.lock"),
		"SETUP_ACCOUNT_LOCK_WAIT": "1",
		"SETUP_ACCOUNT_LOCK_STEP": "0",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

// TASK TEST a: two setups creating a system account on the SAME host at the SAME time BOTH come through;
// neither is rejected because of the lock. The fake `useradd` simulates the real /etc/passwd lock (an
// exclusive, non-blocking flock on a shared file): if two ran truly concurrently the loser would emit
// the busy message. The host-wide serialisation makes them take turns, so both records land.
func TestAccountConcurrentCreatesBothSucceed(t *testing.T) {
	db := filepath.Join(t.TempDir(), "passwd")
	fake := fakeBin(t, "useradd", `#!/bin/bash
# Simulate useradd's exclusive lock on the account database: hold it, and refuse (busy) if held.
exec 8>"`+db+`.lock"
if ! flock -n 8; then
  echo "useradd: cannot lock /etc/passwd; try again later." >&2
  exit 10
fi
sleep 0.3
printf '%s\n' "${@: -1}" >> "`+db+`"
exit 0
`)
	// A high wait so serialisation (not the deadline) is what carries the second through.
	env := accountEnv(t, fake, map[string]string{"SETUP_ACCOUNT_LOCK_WAIT": "10", "SETUP_ACCOUNT_LOCK_STEP": "0"})
	snippet := `
setup_account_cmd svcA useradd --system svcA & p1=$!
setup_account_cmd svcB useradd --system svcB & p2=$!
wait $p1; echo "A=$?"
wait $p2; echo "B=$?"
`
	res := sourceLib(t, env, snippet)
	if !strings.Contains(res.out, "A=0") || !strings.Contains(res.out, "B=0") {
		t.Fatalf("both concurrent creates must succeed, got:\n%s", res.out)
	}
	got, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "svcA") || !strings.Contains(string(got), "svcB") {
		t.Errorf("both accounts must have been created, database holds:\n%s", got)
	}
}

// TASK TEST b: a lock held PERMANENTLY makes the create fail after the named deadline with a clear
// reason — it does not hang. The fake `useradd` always reports the database as locked.
func TestAccountPersistentLockFailsAfterDeadline(t *testing.T) {
	fake := fakeBin(t, "useradd", `#!/bin/bash
echo "useradd: cannot lock /etc/passwd; try again later." >&2
exit 10
`)
	env := accountEnv(t, fake, nil) // WAIT=1, STEP=0 → bounded and fast
	res := sourceLib(t, env, `setup_account_cmd svcX useradd --system svcX; echo "exit=$?"`)
	if strings.Contains(res.out, "exit=0") {
		t.Fatalf("a permanently locked database must fail the create, got:\n%s", res.out)
	}
	if !strings.Contains(res.out, "stayed locked") || !strings.Contains(res.out, "svcX") {
		t.Errorf("the failure must name that the database stayed locked, for which account, got:\n%s", res.out)
	}
	if !strings.Contains(res.out, "1s") {
		t.Errorf("the failure must name the deadline it waited (1s), got:\n%s", res.out)
	}
}

// TASK TEST c: a FACTUAL failure (an invalid name) fails IMMEDIATELY and is NOT retried — repetition
// cannot cure it. A counter file proves the create was attempted exactly once, and the tool's own words
// are surfaced.
func TestAccountFactualFailureNotRetried(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	fake := fakeBin(t, "useradd", `#!/bin/bash
echo x >> "`+count+`"
echo "useradd: invalid user name 'bad name'" >&2
exit 3
`)
	// A generous budget: if the factual failure were (wrongly) treated as transient, it would retry
	// many times before the deadline — the counter would then be > 1.
	env := accountEnv(t, fake, map[string]string{"SETUP_ACCOUNT_LOCK_WAIT": "5", "SETUP_ACCOUNT_LOCK_STEP": "0"})
	res := sourceLib(t, env, `setup_account_cmd 'bad name' useradd --system 'bad name'; echo "exit=$?"`)
	if strings.Contains(res.out, "exit=0") {
		t.Fatalf("a factual failure must fail the create, got:\n%s", res.out)
	}
	if !strings.Contains(res.out, "invalid user name") {
		t.Errorf("the tool's own factual message must be surfaced, got:\n%s", res.out)
	}
	c, err := os.ReadFile(count)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSpace(string(c)), "x"); n != 1 {
		t.Errorf("a factual failure must be attempted exactly once (no retry), got %d attempts", n)
	}
}

// The create is idempotent and reads the database under the lock: an account that already exists is left
// as is and `useradd` is never invoked. `root` always exists, so it stands in for a present account.
func TestAccountLockedCreateIdempotent(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	fake := fakeBin(t, "useradd", `#!/bin/bash
echo x >> "`+count+`"
exit 0
`)
	env := accountEnv(t, fake, nil)
	if res := sourceLib(t, env, `setup_ensure_account_locked root; echo "exit=$?"`); !strings.Contains(res.out, "exit=0") {
		t.Fatalf("an existing account must satisfy the create, got:\n%s", res.out)
	}
	if _, err := os.Stat(count); err == nil {
		t.Errorf("useradd must NOT be invoked for an already-present account")
	}
}

package links

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestStore builds a Store backed by a temp dir and a fixed 32-byte key.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	// Exactly 32 bytes → loadKey uses the raw bytes as the AES-256 key.
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	t.Setenv("DEVLAB_LINKS", filepath.Join(dir, "links"))
	t.Setenv("DEVLAB_LINK_ENC_KEY_FILE", keyPath)
	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// TestSaveGetTokenRoundTrip is the happy path: a saved link reads back and its token decrypts.
func TestSaveGetTokenRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save("alice", "alice-gh", 42, "tok-secret", "repo read:org", time.Unix(0, 0)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	l, err := s.Get("alice")
	if err != nil || l == nil {
		t.Fatalf("Get: err=%v link=%v", err, l)
	}
	if l.GHLogin != "alice-gh" || l.GHID != 42 || l.Scopes != "repo read:org" {
		t.Fatalf("unexpected link: %+v", l)
	}
	tok, err := s.Token("alice")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "tok-secret" {
		t.Fatalf("token mismatch: %q", tok)
	}
	if s.Linked("bob") {
		t.Fatal("bob should not be linked")
	}
	if err := s.Delete("alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.Linked("alice") {
		t.Fatal("alice should be unlinked after Delete")
	}
	if err := s.Delete("alice"); err != nil {
		t.Fatalf("Delete is idempotent: %v", err)
	}
}

// TestConcurrentSaveIsAtomic hammers one user's link with concurrent Saves interleaved with reads.
// Because every access is serialized and each write is a tmp-write+rename, the stored file must
// ALWAYS parse to a complete, decryptable link whose token is one of the ones actually written —
// never a half-written or interleaved (corrupt) intermediate. Run under -race to also catch any
// unsynchronized access to the lock table. This is the regression guard for the missing per-user
// mutex: without it, concurrent Saves share one <user>.json.tmp and publish a corrupt file.
func TestConcurrentSaveIsAtomic(t *testing.T) {
	s := newTestStore(t)
	const n = 64
	written := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		written[token(i)] = true
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Save("alice", "alice-gh", int64(i), token(i), "repo", time.Unix(0, 0)); err != nil {
				t.Errorf("concurrent Save: %v", err)
			}
			_, _ = s.Get("alice")
			_, _ = s.Token("alice")
		}(i)
	}
	wg.Wait()

	// The surviving file must be a complete, decryptable link with one of the written tokens.
	l, err := s.Get("alice")
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	if l == nil {
		t.Fatal("final link missing")
	}
	tok, err := s.Token("alice")
	if err != nil {
		t.Fatalf("final Token (corrupt file?): %v", err)
	}
	if !written[tok] {
		t.Fatalf("final token %q was never written — file corruption", tok)
	}
	assertNoTmpResidue(t, s.dir)
}

// TestConcurrentSaveDelete churns Save against Delete for the same user. Regardless of the race
// order the store stays consistent: Get returns either nil (last op deleted) or a link whose token
// still decrypts (never a truncated/interleaved file), and no temp file is left behind.
func TestConcurrentSaveDelete(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = s.Save("alice", "gh", int64(i), token(i), "repo", time.Unix(0, 0))
		}(i)
		go func() {
			defer wg.Done()
			_ = s.Delete("alice")
		}()
	}
	wg.Wait()

	l, err := s.Get("alice")
	if err != nil {
		t.Fatalf("Get after churn: %v", err)
	}
	if l != nil {
		if _, err := s.Token("alice"); err != nil {
			t.Fatalf("surviving link does not decrypt (corruption): %v", err)
		}
	}
	assertNoTmpResidue(t, s.dir)
}

func token(i int) string { return "tok-" + strconv.Itoa(i) }

// assertNoTmpResidue fails if any .tmp file leaked into the links dir (an aborted atomic write).
func assertNoTmpResidue(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

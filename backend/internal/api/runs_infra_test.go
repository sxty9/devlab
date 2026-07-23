package api

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIsInfraError pins the classification that decides "carry over vs fail the repo": a
// connectivity failure (DNS/network) is infrastructure and must be retried/carried over, while a
// genuine repo or work error is terminal for that repo.
func TestIsInfraError(t *testing.T) {
	infra := []string{
		"fatal: unable to access 'https://github.com/sxty9/aigentic.git/': Could not resolve host: github.com",
		"Get \"https://api.github.com/repos/sxty9/hosuto\": dial tcp: lookup api.github.com on 127.0.0.53:53: server misbehaving",
		"dial tcp 140.82.121.3:443: connect: connection refused",
		"read tcp: i/o timeout",
		"net/http: TLS handshake timeout",
		"temporary failure in name resolution",
	}
	for _, m := range infra {
		if !isInfraError(errors.New(m)) {
			t.Errorf("expected infra=true for %q", m)
		}
	}

	notInfra := []string{
		"github: 404 Not Found",
		"github: 403 Forbidden: rate limit or permission",
		"commit: nothing to commit",
		"push: protected branch hook declined",
	}
	for _, m := range notInfra {
		if isInfraError(errors.New(m)) {
			t.Errorf("expected infra=false for %q", m)
		}
	}
	if isInfraError(nil) {
		t.Error("nil must not be infra")
	}
}

// TestRetryInfra pins the retry contract: infra errors are retried up to the cap, a non-infra error
// fails fast (no retries), and a success on a later attempt returns nil.
func TestRetryInfra(t *testing.T) {
	// Infra error every time → exactly `attempts` calls, returns the infra error.
	calls := 0
	err := retryInfra(context.Background(), 3, time.Millisecond, func() error {
		calls++
		return errors.New("could not resolve host: github.com")
	})
	if calls != 3 || err == nil {
		t.Errorf("infra: got %d calls err=%v, want 3 calls and an error", calls, err)
	}

	// Non-infra error → fails fast after 1 call.
	calls = 0
	err = retryInfra(context.Background(), 3, time.Millisecond, func() error {
		calls++
		return errors.New("github: 404 Not Found")
	})
	if calls != 1 || err == nil {
		t.Errorf("non-infra: got %d calls err=%v, want 1 call and the error", calls, err)
	}

	// Transient infra then success → retries, then returns nil.
	calls = 0
	err = retryInfra(context.Background(), 5, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return errors.New("i/o timeout")
		}
		return nil
	})
	if calls != 3 || err != nil {
		t.Errorf("recovery: got %d calls err=%v, want 3 calls and nil", calls, err)
	}

	// A cancelled context stops the backoff wait promptly.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	_ = retryInfra(ctx, 5, time.Hour, func() error {
		calls++
		return errors.New("connection reset")
	})
	if calls != 1 {
		t.Errorf("cancelled ctx: got %d calls, want 1 (no long backoff wait)", calls)
	}
}

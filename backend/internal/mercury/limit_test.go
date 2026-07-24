package mercury

import (
	"errors"
	"testing"
	"time"
)

func TestDetectUsageLimit(t *testing.T) {
	epoch := int64(1735689600) // 2025-01-01T00:00:00Z
	cases := []struct {
		name     string
		out      string
		err      error
		limited  bool
		hasReset bool
		reset    time.Time
	}{
		{
			name:    "success report mentioning rate limit is NOT a limit",
			out:     `{"is_error":false,"result":"I added rate limiting to the login endpoint."}`,
			err:     nil,
			limited: false,
		},
		{
			name:     "is_error result with piped epoch",
			out:      `{"is_error":true,"result":"Claude AI usage limit reached|1735689600"}`,
			err:      nil,
			limited:  true,
			hasReset: true,
			reset:    time.Unix(epoch, 0).UTC(),
		},
		{
			name:     "millisecond epoch",
			out:      `{"is_error":true,"result":"usage limit reached|1735689600000"}`,
			limited:  true,
			hasReset: true,
			reset:    time.Unix(epoch, 0).UTC(),
		},
		{
			name:    "error text, no reset time",
			out:     ``,
			err:     errors.New("Claude AI usage limit reached, please wait"),
			limited: true,
		},
		{
			name:    "429 too many requests via error",
			out:     ``,
			err:     errors.New("API error 429: too many requests"),
			limited: true,
		},
		{
			name:     "ISO reset timestamp",
			out:      `{"is_error":true,"result":"rate limit reached, resets at 2025-01-01T00:00:00Z"}`,
			limited:  true,
			hasReset: true,
			reset:    time.Unix(epoch, 0).UTC(),
		},
		{
			name:    "ordinary failure is not a limit",
			out:     `{"is_error":true,"result":"could not compile: syntax error"}`,
			limited: false,
		},
		{
			name:    "plain nil/empty is not a limit",
			out:     `{"is_error":false,"result":"done"}`,
			limited: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			limited, reset, hasReset := DetectUsageLimit([]byte(c.out), c.err)
			if limited != c.limited {
				t.Fatalf("limited=%v, want %v", limited, c.limited)
			}
			if hasReset != c.hasReset {
				t.Fatalf("hasReset=%v, want %v", hasReset, c.hasReset)
			}
			if c.hasReset && !reset.Equal(c.reset) {
				t.Fatalf("reset=%v, want %v", reset, c.reset)
			}
		})
	}
}

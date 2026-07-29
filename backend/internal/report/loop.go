package report

import (
	"context"
	"time"
)

// ticker is the ONE background-pass driver of this package: the daily reporter and the delivery
// self-check both need "run one pass now, then every interval, and never let a bad pass take the
// daemon down", so that behavior lives here once instead of in each of them.
type ticker struct {
	label    string
	interval time.Duration
	logf     func(string, ...any)
	tick     func(context.Context)
}

// run performs one pass immediately (so a pass due right after a restart is not delayed a full
// interval) and then one per interval until ctx is done. Each pass is panic-isolated: a broken
// pass is logged as contained and the loop keeps its rhythm.
func (t ticker) run(ctx context.Context) {
	if t.interval <= 0 {
		t.interval = 10 * time.Minute
	}
	tk := time.NewTicker(t.interval)
	defer tk.Stop()
	t.logf("devlabd: %s started (interval=%s)", t.label, t.interval)
	t.safeTick(ctx)
	for {
		select {
		case <-ctx.Done():
			t.logf("devlabd: %s stopped", t.label)
			return
		case <-tk.C:
			t.safeTick(ctx)
		}
	}
}

func (t ticker) safeTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			t.logf("devlabd: %s pass panicked (contained): %v", t.label, r)
		}
	}()
	t.tick(ctx)
}

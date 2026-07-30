package api

import (
	"context"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/mailer"
	"devlab/backend/internal/report"
	"devlab/backend/internal/runs"
)

// StartReporter arms the service's REPORTING side (S14) — the one boot step that makes DevLab
// speak up about itself:
//
//   - the delivery self-check (REQ-031.4), which writes persistent hints and therefore runs
//     always: it needs no mailbox, only the executions it can observe;
//   - the daily run report by mail (REQ-042), which needs a provisioned run user to have an
//     owner to report to.
//
// There are NO operating modes (REQ-027.1): the mail report runs whenever a run user is
// provisioned, never under dev-bypass. The recipient is the run OWNER — the linked account the
// runner acts on behalf of (DEVLAB_RUNS_TOKEN_USER, else DEVLAB_RUNS_USER) — whose mailbox the
// mail service resolves from their landscape identity, so no address is configured here. Wired
// from main() with a cancelable context.
func (s *Server) StartReporter(ctx context.Context) {
	s.startSelfCheck(ctx)

	user := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_USER"))
	if user == "" {
		log.Printf("devlabd: daily-report reporter OFF (no run user provisioned)")
		return
	}
	if s.v.DevBypass() {
		log.Printf("devlabd: daily-report reporter OFF under dev-bypass")
		return
	}
	if s.results == nil || s.reportLedger == nil {
		log.Printf("devlabd: daily-report reporter OFF — stores unavailable")
		return
	}
	// The recipient is the owner the runs are assigned to, mirroring the executor's tokenUser default.
	recipient := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_TOKEN_USER"))
	if recipient == "" {
		recipient = user
	}

	lookback := 1
	if v := strings.TrimSpace(os.Getenv("DEVLAB_MERCURY_REPORT_LOOKBACK_DAYS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lookback = n
		}
	}
	interval := 10 * time.Minute
	if d := strings.TrimSpace(os.Getenv("DEVLAB_MERCURY_REPORT_INTERVAL")); d != "" {
		if pd, err := time.ParseDuration(d); err == nil && pd > 0 {
			interval = pd
		}
	}

	rp := report.NewReporter(report.Config{
		Recipient: recipient,
		Execs:     s.results,
		// The pause lives on the state documents, so an unfinished execution is named as
		// "paused" only when a document says so (nil-safe: without the machinery the report
		// claims no pause).
		Paused:   s.pausedExecutions(),
		Notices:  s.runNotices, // the day's alarms, overrides and deviations (REQ-042.6)
		Ledger:   s.reportLedger,
		Sender:   mailSender{},
		Lookback: lookback,
		LinkBase: strings.TrimSpace(os.Getenv("DEVLAB_PUBLIC_URL")),
	})
	log.Printf("devlabd: daily-report reporter ENABLED — recipient=%s lookback=%dd interval=%s", recipient, lookback, interval)
	go rp.Run(ctx, interval)
}

// startSelfCheck arms the delivery self-check (REQ-031.4): the service watching its own outcome
// over a period. It writes a persistent hint, so it needs no mailbox and no run user — only the
// executions and the notice pool. The window and the interval are tunable for operation; the
// defaults are the ones the requirement describes.
func (s *Server) startSelfCheck(ctx context.Context) {
	if s.results == nil || s.runNotices == nil {
		log.Printf("devlabd: delivery self-check OFF — stores unavailable")
		return
	}
	window := envDuration("DEVLAB_MERCURY_SELFCHECK_WINDOW", 72*time.Hour)
	interval := envDuration("DEVLAB_MERCURY_SELFCHECK_INTERVAL", time.Hour)
	sc := report.NewSelfCheck(report.SelfCheckConfig{
		Execs:   s.results,
		Notices: s.runNotices,
		Pub:     s.publisher(),
		Window:  window,
	})
	log.Printf("devlabd: delivery self-check ENABLED — window=%s interval=%s", window, interval)
	go sc.Run(ctx, interval)
}

// pausedExecutions hands out the execution documents as the report's pause seam, or nil while the
// machinery is not wired (a typed nil would read as "present" through the interface).
func (s *Server) pausedExecutions() report.PausedExecutions {
	if s.docs == nil {
		return nil
	}
	return s.docs
}

// publisher hands out the SSE broker as a live.Publisher, or nil when none is wired — so a
// background pass ticks the stream exactly like a request handler does.
func (s *Server) publisher() live.Publisher {
	if s.broker == nil {
		return nil
	}
	return s.broker
}

// NoticeFunc hands out the ONE bridge for sources that raise a finding as (kind, text) rather than
// as a record — the scheduler's operator one-liners (restart requested, restart completed, a
// deferred restart, an abandoned execution). cmd/devlabd passes it to sched.SetNoticeFunc, so those
// events land in the same pool the panel and the daily report read, and the notices topic ticks
// after the write. Without a pool it returns nil and the scheduler keeps its logging default.
func (s *Server) NoticeFunc() func(kind, text string) {
	if s.runNotices == nil {
		return nil
	}
	return func(kind, text string) {
		if _, err := s.runNotices.Coalesce(runs.Notice{Kind: kind, Text: text}); err != nil {
			log.Printf("devlabd: notice [%s] not recorded: %v", kind, err)
			return
		}
		s.publish(live.TopicNotices)
	}
}

// envDuration reads a duration from the environment, falling back to def for an unset or
// unparseable value (operation tunes it; a typo never silently disables the pass).
func envDuration(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		log.Printf("devlabd: %s=%q is not a positive duration — using %s", key, v, def)
	}
	return def
}

// mailSender adapts the landscape mail client to report.Sender. It resolves the client (and thus the
// internal secret) per send rather than caching it: a send is rare (once a day), and re-reading means
// a mail service that becomes reachable — or a secret that becomes readable — after startup simply
// starts working, while an unreachable one surfaces as a visible, retried failure in the ledger.
type mailSender struct{}

func (mailSender) Send(ctx context.Context, recipient string, c report.Content) error {
	cl, err := mailer.New()
	if err != nil {
		return err
	}
	return cl.Send(ctx, mailer.Message{
		Username: recipient,
		Subject:  c.Subject,
		Body:     c.Text,
		HTMLBody: c.HTML,
	})
}

// runsReportStatus returns the recent delivery records of the daily run report (newest day first), so
// the UI can surface a failed send instead of it vanishing silently. Read-only.
func (s *Server) runsReportStatus(w http.ResponseWriter, _ *http.Request) {
	recs := []report.Record{}
	if s.reportLedger != nil {
		all, err := s.reportLedger.List()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not read report status")
			return
		}
		if all != nil {
			recs = all
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Day > recs[j].Day })
	if len(recs) > 30 {
		recs = recs[:30]
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": recs})
}

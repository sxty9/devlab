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

	"devlab/backend/internal/mailer"
	"devlab/backend/internal/report"
)

// StartReporter arms the daily run-report emailer. There are NO operating modes (REQ-027.1):
// it runs whenever a run user is provisioned, never under dev-bypass. The recipient is the run
// OWNER — the linked account the runner acts on behalf of (DEVLAB_RUNS_TOKEN_USER, else
// DEVLAB_RUNS_USER) — whose mailbox the mail service resolves from their landscape identity,
// so no address is configured here. Wired from main() with a cancelable context.
func (s *Server) StartReporter(ctx context.Context) {
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
		Ledger:    s.reportLedger,
		Sender:    mailSender{},
		Lookback:  lookback,
		LinkBase:  strings.TrimSpace(os.Getenv("DEVLAB_PUBLIC_URL")),
	})
	log.Printf("devlabd: daily-report reporter ENABLED — recipient=%s lookback=%dd interval=%s", recipient, lookback, interval)
	go rp.Run(ctx, interval)
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

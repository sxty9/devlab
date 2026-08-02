package api

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"devlab/backend/internal/notify"
	"devlab/backend/internal/runs"
)

// This file is DevLab's outward VOICE: it turns a disturbance the service just recorded about itself
// into a delivery that reaches the user, instead of leaving it unread in the pool. It closes the gap
// the notice surface always had — the service was never blind, only mute.
//
// Three rules the axioms demand shape it, all of them enforced here and NOT in the passive pool:
//   - One delivery per disturbance: the pool fires its on-new hook only for a genuinely new record,
//     so a fault recurring every 20s is delivered once and only bundled afterwards.
//   - Not every notice is a disturbance: routine operational noise (a restart, a startup reconcile,
//     an automatic assignment) is recorded but not delivered — the distinction comes from the KIND
//     (runs.IsDisturbance), so a flood nobody reads is avoided as much as silence is.
//   - Best-effort, never blocking: the emit runs in its own goroutine with a short deadline, and a
//     notify outage costs a logged skip — never the write path that recorded the notice.

// noticeEmitTimeout bounds a single delivery so a slow or hung notify service costs one goroutine for
// a moment, never a stuck dispatcher.
const noticeEmitTimeout = 15 * time.Second

// noticeEmitter is the seam the dispatcher delivers through — *notify.Client in production, a fake in
// tests. It is resolved PER delivery (newEmitter) rather than cached, so a secret that becomes
// readable, or a notify service that becomes reachable, after startup simply starts working.
type noticeEmitter interface {
	Emit(ctx context.Context, e notify.Event) error
}

// noticeDispatcher delivers a newly-recorded disturbance to one recipient through the notify service.
type noticeDispatcher struct {
	recipient  string
	linkBase   string
	newEmitter func() (noticeEmitter, error)
	// async wraps the network delivery. Production sets it to `go`, so the pool's write path never
	// blocks on an HTTP call; a test leaves it nil to run the delivery synchronously and observe it.
	async bool
}

// StartNoticeDelivery arms the outward delivery of disturbances (S14): it installs the pool's on-new
// hook so every new disturbance reaches the run owner via the notify service. It mirrors the daily
// report's recipient resolution — the run OWNER the runner acts for (DEVLAB_RUNS_TOKEN_USER, else
// DEVLAB_RUNS_USER) — so no address is configured here. Wired from StartReporter at boot.
//
// Without a run user there is nobody to deliver to, and under dev-bypass the service is not acting for
// a real owner, so in both cases the hook stays uninstalled and the reason is logged once. The notices
// still accumulate in the pool and remain visible in the dashboard; only the outward push is off.
func (s *Server) StartNoticeDelivery() {
	if s.runNotices == nil {
		log.Printf("devlabd: notice delivery OFF — notice pool unavailable")
		return
	}
	user := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_USER"))
	if user == "" {
		log.Printf("devlabd: notice delivery OFF (no run user provisioned)")
		return
	}
	if s.v.DevBypass() {
		log.Printf("devlabd: notice delivery OFF under dev-bypass")
		return
	}
	recipient := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_TOKEN_USER"))
	if recipient == "" {
		recipient = user
	}
	d := &noticeDispatcher{
		recipient:  recipient,
		linkBase:   strings.TrimSpace(os.Getenv("DEVLAB_PUBLIC_URL")),
		newEmitter: func() (noticeEmitter, error) { return notify.New() },
		async:      true,
	}
	s.runNotices.SetOnNew(d.dispatch)
	log.Printf("devlabd: notice delivery ENABLED — recipient=%s (disturbances only)", recipient)
}

// dispatch is the on-new hook: it delivers a disturbance and drops operational noise. Operational
// noise returns immediately (nothing to deliver). A disturbance is handed to the network emit, in a
// goroutine in production so the pool's write path returns at once.
func (d *noticeDispatcher) dispatch(n runs.Notice) {
	if !runs.IsDisturbance(n.Kind) {
		return
	}
	if d.async {
		go d.deliver(n)
		return
	}
	d.deliver(n)
}

// deliver performs the single outward emit. Every failure — an unreadable secret, an unreachable
// service, a rejected request — is a LOGGED SKIP, not a crash and not a retry: the notice is already
// safely in the pool and visible in the dashboard, so a lost push degrades the reach, never the record.
func (d *noticeDispatcher) deliver(n runs.Notice) {
	cl, err := d.newEmitter()
	if err != nil {
		log.Printf("devlabd: notice [%s] not delivered: %v", n.Kind, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), noticeEmitTimeout)
	defer cancel()
	if err := cl.Emit(ctx, d.event(n)); err != nil {
		log.Printf("devlabd: notice [%s] not delivered: %v", n.Kind, err)
	}
}

// event maps a notice onto notify's vocabulary: a title (the humanized kind, with the repository when
// the finding names one), the finding's own wording as the body, a deep link into the dashboard, the
// warning level a fault carries, and the record id as a defensive dedupe key.
func (d *noticeDispatcher) event(n runs.Notice) notify.Event {
	e := notify.Event{
		User:    d.recipient,
		Service: "devlab",
		Title:   noticeTitle(n),
		Body:    n.Message(),
		Level:   notify.LevelWarning,
		Dedupe:  n.ID,
	}
	if e.Body == "" {
		e.Body = e.Title
	}
	if d.linkBase != "" {
		e.URL = strings.TrimRight(d.linkBase, "/") + "/#/mercury"
	}
	return e
}

// noticeTitle is the short headline of a disturbance: the humanized kind, plus the repository it
// concerns when it names one ("Delivery blocked — o/x"). notifyd requires a title, and the kind is
// always present, so this is never empty.
func noticeTitle(n runs.Notice) string {
	title := humanizeKind(n.Kind)
	if repo := strings.TrimSpace(n.Repo); repo != "" {
		title += " — " + repo
	}
	return title
}

// humanizeKind turns a wire kind into a readable headline ("delivery-blocked" → "Delivery blocked"),
// the same rule the dashboard applies to an unlabeled kind, kept in step so both sides read alike.
func humanizeKind(kind string) string {
	words := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(kind, "-", " "), "_", " "))
	if words == "" {
		return "Notice"
	}
	return strings.ToUpper(words[:1]) + words[1:]
}

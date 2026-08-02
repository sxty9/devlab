package runs

import "testing"

// The disturbance/noise split is drawn from the KIND, the safe way round: the named routine
// transitions are noise, and everything else — including a fault kind this build has never seen — is
// a disturbance that gets delivered, so a new fault is never swallowed silently.
func TestIsDisturbance(t *testing.T) {
	faults := []string{
		NoticeDeliveryAlarm, NoticeDeliveryBlocked, NoticeDeliverySelfCheck, NoticeStructureViolation,
		NoticeDeliveryHeld, NoticeGitHubQuota, NoticeProtectionDeviation, NoticeAdminOverride, NoticeFailed,
		"execution-abandoned", "restart-queued-start-failed", "a-kind-from-the-future",
	}
	for _, k := range faults {
		if !IsDisturbance(k) {
			t.Errorf("kind %q must count as a disturbance (delivered)", k)
		}
	}
	noise := []string{"restart-requested", "restart-completed", "startup-reconcile", NoticeAssigned}
	for _, k := range noise {
		if IsDisturbance(k) {
			t.Errorf("kind %q is routine operational noise and must NOT be delivered", k)
		}
	}
}

// The on-new hook fires ONCE per genuinely new record and NOT for a coalesced repeat — the structural
// fact the outward delivery relies on to send a recurring fault exactly once.
func TestNoticeStoreOnNewFiresOncePerNewRecord(t *testing.T) {
	s := newTestNotices(t)
	var fired []Notice
	s.SetOnNew(func(n Notice) { fired = append(fired, n) })

	blocked := func() Notice {
		return Notice{Kind: NoticeDeliveryBlocked, Repo: "o/x", Text: "pull request #7 is blocked: 502"}
	}
	// The same finding three times: one new record, then two bundled repeats.
	for i := 0; i < 3; i++ {
		if _, err := s.Coalesce(blocked()); err != nil {
			t.Fatal(err)
		}
	}
	// A DIFFERENT finding is a second new record.
	if _, err := s.Coalesce(Notice{Kind: NoticeDeliveryAlarm, Repo: "o/y", Text: "stale wrapper"}); err != nil {
		t.Fatal(err)
	}

	if len(fired) != 2 {
		t.Fatalf("hook must fire once per NEW record (2), got %d: %+v", len(fired), fired)
	}
	if fired[0].Kind != NoticeDeliveryBlocked || fired[1].Kind != NoticeDeliveryAlarm {
		t.Errorf("hook must carry the new records in order, got %+v", fired)
	}
}

package api

import (
	"sync"
	"testing"
	"time"
)

// The spend ceiling binds the SUM across concurrent runs, not each run on its own: two runs that each
// stay under the ceiling still trip it together once their combined spend crosses it (task point 5).
func TestRunWaveSpendIsAggregate(t *testing.T) {
	var w runWave
	w.enter() // run A
	w.enter() // run B, same wave

	w.addSpend(6) // A
	if w.overBudget(10) {
		t.Fatal("6 alone must not exceed a ceiling of 10")
	}
	w.addSpend(5) // B — neither run alone reached 10, but 6+5 does
	if !w.overBudget(10) {
		t.Fatalf("aggregate 11 must exceed a ceiling of 10, got spend %.0f", w.spendSnapshot())
	}
	if w.overBudget(0) {
		t.Fatal("a ceiling of 0 disables the cap")
	}

	w.leave()
	w.leave()
}

// A fresh wave (active goes 0→1 again) resets the aggregate spend and the limit gate, so a new wave is
// not held down by a previous wave's spend or a limit it already waited out.
func TestRunWaveResetsBetweenWaves(t *testing.T) {
	var w runWave
	w.enter()
	w.addSpend(50)
	w.tripLimit(time.Now().Add(time.Hour))
	w.leave() // wave ends

	w.enter() // new wave
	defer w.leave()
	if s := w.spendSnapshot(); s != 0 {
		t.Fatalf("spend must reset to 0 on a new wave, got %.0f", s)
	}
	if tripped, _ := w.limitTripped(); tripped {
		t.Fatal("the limit gate must reset on a new wave")
	}
}

// Once one run trips the subscription limit, every run in the wave sees the gate and the LATEST reset
// wins (resuming earlier would just re-hit the limit).
func TestRunWaveLimitGateSharedLatestResetWins(t *testing.T) {
	var w runWave
	w.enter()
	w.enter()
	defer func() { w.leave(); w.leave() }()

	if tripped, _ := w.limitTripped(); tripped {
		t.Fatal("gate must start closed")
	}
	early := time.Now().Add(1 * time.Hour)
	late := time.Now().Add(3 * time.Hour)
	w.tripLimit(early)
	w.tripLimit(late)  // a second run reports a later reset
	w.tripLimit(early) // an earlier reset must not pull the resume time back in
	tripped, resumeAt := w.limitTripped()
	if !tripped {
		t.Fatal("gate must be tripped after a run hit the limit")
	}
	if !resumeAt.Equal(late) {
		t.Fatalf("the latest reset must win: got %s want %s", resumeAt, late)
	}
}

// The wave is safe under concurrent access (run with -race).
func TestRunWaveConcurrentAccess(t *testing.T) {
	var w runWave
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.enter()
			w.addSpend(1)
			_ = w.overBudget(5)
			w.tripLimit(time.Now().Add(time.Hour))
			_, _ = w.limitTripped()
			w.leave()
		}()
	}
	wg.Wait()
}

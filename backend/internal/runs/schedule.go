package runs

import (
	"fmt"
	"regexp"
	"time"
)

// Kind is a recurrence kind. Deliberately minimal — no cron dependency; daily or weekly covers the
// intended cadence and computes trivially with time.Now.
type ScheduleKind string

const (
	Daily  ScheduleKind = "daily"
	Weekly ScheduleKind = "weekly"
)

// ScheduleSpec is a recurring schedule: a time-of-day, either every day or on selected weekdays. Times
// are interpreted in the server's local timezone.
type ScheduleSpec struct {
	Kind      ScheduleKind   `json:"kind"`
	TimeOfDay string         `json:"timeOfDay"`          // "HH:MM", 24h
	Weekdays  []time.Weekday `json:"weekdays,omitempty"` // weekly only; 0=Sunday … 6=Saturday
}

var timeOfDayRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// Valid reports whether the schedule is well-formed.
func (s ScheduleSpec) Valid() error {
	if !timeOfDayRe.MatchString(s.TimeOfDay) {
		return fmt.Errorf("timeOfDay %q is not HH:MM (24h)", s.TimeOfDay)
	}
	switch s.Kind {
	case Daily:
		if len(s.Weekdays) != 0 {
			return fmt.Errorf("a daily schedule must carry no weekdays")
		}
	case Weekly:
		if len(s.Weekdays) == 0 {
			return fmt.Errorf("a weekly schedule needs at least one weekday")
		}
		for _, d := range s.Weekdays {
			if d < time.Sunday || d > time.Saturday {
				return fmt.Errorf("invalid weekday %d", d)
			}
		}
	default:
		return fmt.Errorf("schedule kind %q is not daily|weekly", s.Kind)
	}
	return nil
}

func (s ScheduleSpec) hourMin() (int, int) {
	var h, m int
	_, _ = fmt.Sscanf(s.TimeOfDay, "%d:%d", &h, &m)
	return h, m
}

// Next returns the first fire time strictly after `after`, in after's location. Always computed
// forward from the given instant — so after downtime the scheduler fires once (catch-up), never N
// times for the missed periods.
func (s ScheduleSpec) Next(after time.Time) (time.Time, error) {
	if err := s.Valid(); err != nil {
		return time.Time{}, err
	}
	h, m := s.hourMin()
	loc := after.Location()
	base := time.Date(after.Year(), after.Month(), after.Day(), h, m, 0, 0, loc)
	switch s.Kind {
	case Daily:
		if !base.After(after) {
			base = base.AddDate(0, 0, 1)
		}
		return base, nil
	case Weekly:
		want := map[time.Weekday]bool{}
		for _, d := range s.Weekdays {
			want[d] = true
		}
		for i := 0; i < 8; i++ { // today .. +7 days covers every weekday exactly once
			c := base.AddDate(0, 0, i)
			if want[c.Weekday()] && c.After(after) {
				return c, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("kein nächster Zeitpunkt bestimmbar")
}

// NextFire returns the first fire time strictly after `after` — the one place fire times come
// from. The schedule pointer on a run is advanced only when an execution ACTUALLY starts, so a
// missed window fires once (catch-up), never N times. An invalid spec yields the zero time.
func NextFire(spec ScheduleSpec, after time.Time) time.Time {
	t, err := spec.Next(after)
	if err != nil {
		return time.Time{}
	}
	return t
}

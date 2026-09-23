package domain

import (
	"fmt"
	"strings"
	"time"
)

// ActivitySpec is how a mine is worked: where its seismicity comes from and
// when. Mining-induced seismicity is not spread evenly through a mine around
// the clock. It concentrates around the few faces being worked — drilling,
// stress change as a stope opens — and above all follows blasts, each followed
// by a sequence of events decaying over hours (Vallejos & McKinnon 2009: 90 %
// of re-entry incidents in 18 surveyed mines were triggered by blasting, and
// the events triggering them lay 50–100 m from mining). Quiet ground
// elsewhere has a low background.
//
// The day's total rate stays the mine's; this says only where and when its
// events happen. Every number is a parameter to sweep.
type ActivitySpec struct {
	// Areas is how many faces are worked at once.
	Areas int
	// Rotate is how long a set of faces stays worked before work moves on —
	// a shift, say.
	Rotate time.Duration
	// Mix is the share of the day's events from each source.
	Mix ActivityMix
	// Blasting is when blasts are fired.
	Blasting BlastSchedule
	// Spread is how far from a face, in metres, its events fall: the standard
	// deviation of their distance along each axis.
	Spread float64
}

// ActivityMix is the share of events from blasts and the sequences after
// them, from work around the active faces, and from background elsewhere. The
// shares are relative; they need not sum to one.
type ActivityMix struct {
	Blast      float64 `json:"blast"`
	Work       float64 `json:"work"`
	Background float64 `json:"background"`
}

// BlastSchedule fires blasts in a window each day: the first at Start after
// midnight, then one every Every until Window has passed — each at one of the
// faces being worked then, in turn. A window of zero is one blast a day.
type BlastSchedule struct {
	Start  time.Duration
	Window time.Duration
	Every  time.Duration
	// OmoriP and OmoriC shape each blast's sequence: n(t) = K/(c+t)^p. p
	// ranges 0.4–1.6 across 250 mining sequences, site means 0.74–1.05
	// (Vallejos & McKinnon 2009); c is how soon the decay sets in.
	OmoriP float64
	OmoriC time.Duration
	// Length is how long after a blast its sequence is drawn over — past it
	// the rate is taken to be back to background.
	Length time.Duration
	// Clear is how long before the first blast of the window the production
	// areas are cleared of people, and ReEntry how long after the last blast
	// they stay closed: LKAB evacuates before blasting and ventilates for
	// several hours after; the surveyed re-entry protocols wait from 2 to 12.
	Clear   time.Duration
	ReEntry time.Duration
}

// DefaultActivity is a starting point, not a finding: four faces worked in
// twelve-hour shifts, a third of the events from blasting, half around the
// working faces, a night blasting window as LKAB's Kiruna fires, sequences of
// the average decay the surveyed mines showed.
func DefaultActivity() ActivitySpec {
	return ActivitySpec{
		Areas:  4,
		Rotate: 12 * time.Hour,
		Mix:    ActivityMix{Blast: 0.3, Work: 0.5, Background: 0.2},
		Blasting: BlastSchedule{
			Start: time.Hour + 15*time.Minute, Window: 30 * time.Minute, Every: 10 * time.Minute,
			OmoriP: 1.0, OmoriC: 5 * time.Minute, Length: 12 * time.Hour,
			Clear: 30 * time.Minute, ReEntry: 3 * time.Hour,
		},
		Spread: 75,
	}
}

// Validate rejects an activity that could not be replayed.
func (a ActivitySpec) Validate() error {
	var problems []string
	if a.Areas < 1 {
		problems = append(problems, fmt.Sprintf("activity.areas must be >= 1, got %d", a.Areas))
	}
	if a.Rotate <= 0 {
		problems = append(problems, fmt.Sprintf("activity.rotate_seconds must be > 0, got %v", a.Rotate.Seconds()))
	}
	m := a.Mix
	if m.Blast < 0 || m.Work < 0 || m.Background < 0 || m.Blast+m.Work+m.Background <= 0 {
		problems = append(problems, fmt.Sprintf("activity.mix must be shares >= 0 with some above 0, got "+
			"blast %v, work %v, background %v", m.Blast, m.Work, m.Background))
	}
	b := a.Blasting
	if b.Start < 0 || b.Start >= 24*time.Hour {
		problems = append(problems, fmt.Sprintf("activity.blasting.start_seconds is a time of day: "+
			"0 to 86400 exclusive, got %v", b.Start.Seconds()))
	}
	if b.Window < 0 {
		problems = append(problems, fmt.Sprintf("activity.blasting.window_seconds must be >= 0, got %v", b.Window.Seconds()))
	}
	if b.Window > 0 && b.Every <= 0 {
		problems = append(problems, fmt.Sprintf("activity.blasting.every_seconds must be > 0 for a window "+
			"of blasts, got %v", b.Every.Seconds()))
	}
	if b.OmoriP <= 0 || b.OmoriP > 3 {
		problems = append(problems, fmt.Sprintf("activity.blasting.omori_p must be in (0, 3], got %v; "+
			"mining sequences range 0.4–1.6", b.OmoriP))
	}
	if b.OmoriC <= 0 {
		problems = append(problems, fmt.Sprintf("activity.blasting.omori_c_seconds must be > 0, got %v", b.OmoriC.Seconds()))
	}
	if b.Length <= 0 {
		problems = append(problems, fmt.Sprintf("activity.blasting.length_seconds must be > 0, got %v", b.Length.Seconds()))
	}
	if b.Clear < 0 {
		problems = append(problems, fmt.Sprintf("activity.blasting.clear_seconds must be >= 0, got %v", b.Clear.Seconds()))
	}
	if b.ReEntry < 0 {
		problems = append(problems, fmt.Sprintf("activity.blasting.reentry_seconds must be >= 0, got %v", b.ReEntry.Seconds()))
	}
	if a.Spread <= 0 {
		problems = append(problems, fmt.Sprintf("activity.spread_m must be > 0, got %v", a.Spread))
	}
	if len(problems) > 0 {
		return fmt.Errorf("activity is not usable: %s", strings.Join(problems, "; "))
	}
	return nil
}

package workload

import (
	"math/rand/v2"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/activity"
	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/mineplan"
)

// activityStream drives how a worked mine's events are timed and where they
// come from. A stream of its own: a scenario with no activity draws nothing
// from it, and so replays exactly as it did before activity existed.
const activityStream = 0xC2B2AE3D27D4EB4F

// happening is an event before it is placed: when, and what produced it —
// the mine's working, or a scenario burst.
type happening struct {
	at     time.Duration
	burst  *int
	source activity.Source
}

// workedEvents is a worked mine's events, in time order: what its activity
// produces at the mine's own rate, and any bursts on top.
func workedEvents(mine domain.Mine, scenario domain.Scenario, plan activity.Plan, random *rand.Rand) []happening {
	var out []happening
	for _, s := range plan.Sources(mine.BackgroundRate, scenario.Duration, random) {
		out = append(out, happening{at: s.At, source: s})
	}
	out = append(out, burstHappenings(mine, scenario, random)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// planOf is how a worked mine is worked over the scenario: its faces, from the
// plan of its tunnels, worked and blasted as the activity says.
func planOf(scenario domain.Scenario, layout domain.Layout, random *rand.Rand) (activity.Plan, error) {
	return activity.NewPlan(*scenario.Activity, mineplan.Faces(layout.Tunnels), scenario.Duration, random)
}

// burstHappenings is the scenario's bursts alone: each one's aftershock rate,
// on top of the working, thinned from their peak, and each event given to the
// burst in proportion to its rate at that moment.
func burstHappenings(mine domain.Mine, scenario domain.Scenario, random *rand.Rand) []happening {
	rate := func(at time.Duration) float64 {
		total := 0.0
		for _, b := range scenario.Bursts {
			total += burstRate(mine.BackgroundRate, b, at)
		}
		return total
	}
	peak := 0.0
	for _, b := range scenario.Bursts {
		peak = max(peak, rate(b.At))
	}
	if peak <= 0 {
		return nil
	}
	var out []happening
	perSecond := peak / 3600
	for at := random.ExpFloat64() / perSecond; at < scenario.Duration.Seconds(); at += random.ExpFloat64() / perSecond {
		elapsed := time.Duration(at * float64(time.Second))
		total := rate(elapsed)
		if random.Float64() >= total/peak {
			continue
		}
		draw := random.Float64() * total
		for i, b := range scenario.Bursts {
			draw -= burstRate(mine.BackgroundRate, b, elapsed)
			if draw < 0 || i == len(scenario.Bursts)-1 {
				index := i
				out = append(out, happening{at: elapsed, burst: &index, source: activity.Source{Blast: -1}})
				break
			}
		}
	}
	return out
}

// placeWorked is where a worked mine's event happened: a burst's around its
// epicentre, a background event along the tunnels, and work or a blast's
// sequence around its face.
func placeWorked(h happening, spread float64, epicentres []domain.Point, layout domain.Layout, random *rand.Rand) domain.Point {
	if h.burst != nil || h.source.Kind == activity.Background {
		return placeEvent(h.burst, epicentres, layout, random)
	}
	return layout.Extent.Clamp(domain.Point{
		X: h.source.Near.X + random.NormFloat64()*spread,
		Y: h.source.Near.Y + random.NormFloat64()*spread,
		Z: h.source.Near.Z + random.NormFloat64()*spread,
	})
}

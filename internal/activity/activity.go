// Package activity is how a mine is worked, as a source of seismicity: which
// faces are worked when, when blasts are fired and where, and the events all
// of that produces — around the working faces, in the sequence after each
// blast, and a low background elsewhere.
//
// Pure functions, drawing only from the random source they are given, so a
// scenario's seed replays the same activity. It knows the mine only as the
// faces it is handed; placing each event is left to whoever generates the
// world.
package activity

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// Kind is where an event comes from.
type Kind int

const (
	Background Kind = iota
	Work
	Blast
)

func (k Kind) String() string {
	switch k {
	case Work:
		return "work"
	case Blast:
		return "blast"
	default:
		return "background"
	}
}

// BlastAt is one blast: when, and at which face.
type BlastAt struct {
	At   time.Duration
	Face domain.Point
}

// Plan is a mine's activity over a scenario.
type Plan struct {
	spec        domain.ActivitySpec
	shifts      [][]domain.Point
	blasts      []BlastAt
	evacuations [][2]time.Duration
}

// NewPlan decides which faces are worked in each shift of the scenario, and
// fires the day's blasts at them in turn.
func NewPlan(spec domain.ActivitySpec, faces []domain.Point, duration time.Duration, random *rand.Rand) (Plan, error) {
	if err := spec.Validate(); err != nil {
		return Plan{}, err
	}
	if spec.Areas > len(faces) {
		return Plan{}, fmt.Errorf("activity.areas is %d, but the mine has %d faces to work", spec.Areas, len(faces))
	}
	p := Plan{spec: spec}
	for start := time.Duration(0); start < duration || start == 0; start += spec.Rotate {
		order := random.Perm(len(faces))
		shift := make([]domain.Point, spec.Areas)
		for i := range shift {
			shift[i] = faces[order[i]]
		}
		p.shifts = append(p.shifts, shift)
		if spec.Rotate <= 0 {
			break
		}
	}

	b := spec.Blasting
	turn := 0
	for day := time.Duration(0); day < duration; day += 24 * time.Hour {
		first := len(p.blasts)
		for offset := time.Duration(0); offset <= b.Window; offset += b.Every {
			at := day + b.Start + offset
			if at >= duration {
				break
			}
			active := p.ActiveAt(at)
			p.blasts = append(p.blasts, BlastAt{At: at, Face: active[turn%len(active)]})
			turn++
			if b.Every <= 0 {
				break // a window of zero is one blast
			}
		}
		if len(p.blasts) > first {
			p.evacuations = append(p.evacuations, [2]time.Duration{
				p.blasts[first].At - b.Clear, p.blasts[len(p.blasts)-1].At + b.ReEntry,
			})
		}
	}
	return p, nil
}

// Evacuation is the production areas' closure for blasting that is under way
// at a moment, or the next one if none is: cleared from start, closed until
// end — from Clear before the day's first blast to ReEntry after its last.
// False when none is under way or to come.
func (p Plan) Evacuation(at time.Duration) (start, end time.Duration, ok bool) {
	for _, e := range p.evacuations {
		if e[1] > at {
			return e[0], e[1], true
		}
	}
	return 0, 0, false
}

// ActiveAt is the faces being worked at a moment.
func (p Plan) ActiveAt(at time.Duration) []domain.Point {
	i := int(at / p.spec.Rotate)
	if i >= len(p.shifts) {
		i = len(p.shifts) - 1
	}
	if i < 0 {
		i = 0
	}
	return p.shifts[i]
}

// Blasts is every blast fired, in time order.
func (p Plan) Blasts() []BlastAt { return p.blasts }

// Source is where one event comes from: when it happens, what produced it,
// and the face it happens around — none for background. Blast is the index of
// the blast it follows, -1 otherwise.
type Source struct {
	At    time.Duration
	Kind  Kind
	Near  domain.Point
	Blast int
}

// Sources is the events this activity produces over duration at rate events
// an hour, in time order. The rate is the mine's, so the day keeps its total;
// the mix divides it. With no blast fired in the scenario — one too short to
// reach the blasting window — the blast share is worked instead, so the total
// still holds.
func (p Plan) Sources(rate float64, duration time.Duration, random *rand.Rand) []Source {
	m := p.spec.Mix
	total := m.Blast + m.Work + m.Background
	expected := rate * duration.Hours()
	blast, work, background := expected*m.Blast/total, expected*m.Work/total, expected*m.Background/total
	if len(p.blasts) == 0 {
		work += blast
		blast = 0
	}

	var out []Source
	for n := poisson(background, random); n > 0; n-- {
		out = append(out, Source{At: uniform(duration, random), Kind: Background, Blast: -1})
	}
	for n := poisson(work, random); n > 0; n-- {
		at := uniform(duration, random)
		active := p.ActiveAt(at)
		out = append(out, Source{At: at, Kind: Work, Near: active[random.IntN(len(active))], Blast: -1})
	}
	b := p.spec.Blasting
	for i, fired := range p.blasts {
		length := b.Length
		if fired.At+length > duration {
			length = duration - fired.At
		}
		for n := poisson(blast/float64(len(p.blasts)), random); n > 0; n-- {
			since := omori(b.OmoriP, b.OmoriC.Seconds(), length.Seconds(), random.Float64())
			out = append(out, Source{At: fired.At + time.Duration(since*float64(time.Second)),
				Kind: Blast, Near: fired.Face, Blast: i})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// omori is the time, in seconds after a blast, at which a fraction u of its
// sequence has happened: the inverse of the modified Omori law's cumulative
// count, n(t) = K/(c+t)^p, truncated at length.
func omori(p, c, length, u float64) float64 {
	if length <= 0 {
		return 0
	}
	if p == 1 {
		return c*math.Pow((c+length)/c, u) - c
	}
	a, b := math.Pow(c, 1-p), math.Pow(c+length, 1-p)
	return math.Pow(a-u*(a-b), 1/(1-p)) - c
}

func uniform(duration time.Duration, random *rand.Rand) time.Duration {
	return time.Duration(random.Float64() * float64(duration))
}

// poisson draws a count with mean lambda: exactly for small means, by the
// normal approximation for the large ones a day of a mine gives.
func poisson(lambda float64, random *rand.Rand) int {
	if lambda <= 0 {
		return 0
	}
	if lambda < 50 {
		limit, product, n := math.Exp(-lambda), random.Float64(), 0
		for product > limit {
			product *= random.Float64()
			n++
		}
		return n
	}
	return max(0, int(math.Round(lambda+math.Sqrt(lambda)*random.NormFloat64())))
}

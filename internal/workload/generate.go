// Package workload turns a mine and a scenario into the jobs a run replays.
//
// The model is small on purpose. It is not trying to be a seismology paper —
// it is trying to produce load with the shape that makes autoscaling hard: a
// steady baseline that shift patterns modulate, a burst that delivers an hour
// of work in minutes, and an aftershock tail that keeps the queue elevated
// long after the event itself is over.
package workload

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
)

// maxJobs bounds what a single scenario may produce.
//
// A scenario is specified by hand and can easily ask for hundreds of millions
// of jobs by accident. Refusing is far better than exhausting memory while
// somebody waits for a page to load.
const maxJobs = 5_000_000

// detectionProbability is the share of a mine's sensors that register a given
// event and therefore contribute a pick. It is the term that turns array size
// into work per event.
const detectionProbability = 0.6

// diurnalAmplitude is how much shift patterns swing the background rate. Real
// mines are busier on shift; the point here is only that the baseline is not
// perfectly flat, so a controller cannot get away with a single fixed number.
const diurnalAmplitude = 0.35

// omoriExponent is the p in Omori's law, the empirical rule that aftershock
// rate falls off as 1/t^p after a main shock. Values near 1 are what is
// observed in practice.
const omoriExponent = 1.1

// Job is one unit of work arriving at the queue.
type Job struct {
	// ID is this job's identity within the run, so the mine can direct intent
	// at it after it has been submitted. Assigned by position once the jobs
	// are ordered, which makes it a function of the seed alone.
	ID domain.JobID

	// SubmittedAt is the offset from the start of the scenario.
	SubmittedAt time.Duration

	Priority domain.Priority

	// Seconds is how long this job occupies an executor.
	Seconds float64
}

// Generate produces the jobs for one scenario, deterministically from its
// seed.
//
// Determinism is the point. Two runs of the same scenario replay exactly the
// same jobs, so a difference in results can be attributed to the settings that
// changed rather than to the workload having been different. The generator
// uses an explicitly seeded PCG source rather than the package-level random
// number generator, which shares global state and would make one run's output
// depend on what else the process happened to be doing.
func Generate(mine domain.Mine, scenario domain.Scenario) ([]Job, error) {
	if err := mine.Validate(); err != nil {
		return nil, err
	}
	if err := scenario.Validate(); err != nil {
		return nil, err
	}

	random := rand.New(rand.NewPCG(uint64(scenario.Seed), 0x9E3779B97F4A7C15))

	events, err := seismicEvents(mine, scenario, random)
	if err != nil {
		return nil, err
	}

	priorities, weights := priorityTable(scenario)

	// Each detecting sensor contributes one pick, so array size is what turns
	// an event into an amount of work.
	estimated := len(events) * int(math.Ceil(float64(mine.Sensors)*detectionProbability))
	if estimated > maxJobs {
		return nil, fmt.Errorf("this scenario would generate about %d jobs, which is too "+
			"large to replay: reduce the duration, the burst magnitude, the sensor "+
			"count or the background rate", estimated)
	}

	jobs := make([]Job, 0, estimated)
	for _, at := range events {
		picks := 0
		for sensor := 0; sensor < mine.Sensors; sensor++ {
			if random.Float64() < detectionProbability {
				picks++
			}
		}
		if picks == 0 {
			// An event nothing detected is an event that produced no work.
			continue
		}

		for pick := 0; pick < picks; pick++ {
			jobs = append(jobs, Job{
				SubmittedAt: at,
				Priority:    samplePriority(priorities, weights, random),
				Seconds:     sampleDuration(scenario.JobSeconds, random),
			})
			if len(jobs) > maxJobs {
				return nil, fmt.Errorf("this scenario generated more than %d jobs, which is "+
					"too large to replay", maxJobs)
			}
		}
	}

	// Identity by position, assigned once the whole list exists. Jobs are
	// appended in event order and never reordered here, so this is a function
	// of the seed and nothing else — which is what lets two runs of one
	// scenario be compared job for job.
	for i := range jobs {
		jobs[i].ID = domain.JobID(i)
	}
	return jobs, nil
}

// seismicEvents draws event times from a rate that varies over the scenario.
//
// The method is thinning: generate candidates at the highest rate the scenario
// ever reaches, then keep each one with probability rate(t)/peak. It is the
// standard way to sample a non-homogeneous Poisson process, and unlike
// stepping through time in fixed increments it neither loses events inside a
// burst nor spends most of its work on the quiet hours.
func seismicEvents(mine domain.Mine, scenario domain.Scenario, random *rand.Rand) ([]time.Duration, error) {
	peak := peakRate(mine, scenario)
	if peak <= 0 {
		// Nothing ever happens: a mine with no background activity and no
		// bursts is a valid, if dull, scenario.
		return nil, nil
	}
	perSecond := peak / 3600

	var events []time.Duration
	at := 0.0
	limit := scenario.Duration.Seconds()

	for {
		// Exponential inter-arrival at the peak rate.
		at += random.ExpFloat64() / perSecond
		if at >= limit {
			break
		}
		elapsed := time.Duration(at * float64(time.Second))
		if random.Float64() < rateAt(mine, scenario, elapsed)/peak {
			events = append(events, elapsed)
		}
		if len(events) > maxJobs {
			return nil, fmt.Errorf("this scenario generates more than %d seismic events, "+
				"which is too large to replay", maxJobs)
		}
	}
	return events, nil
}

// rateAt is the event rate, in events per hour, at one instant.
func rateAt(mine domain.Mine, scenario domain.Scenario, elapsed time.Duration) float64 {
	rate := mine.BackgroundRate * diurnal(elapsed)
	for _, burst := range scenario.Bursts {
		rate += burstRate(mine.BackgroundRate, burst, elapsed)
	}
	return rate
}

// diurnal modulates the background rate over a working day. Blasting and
// heavy extraction cluster on shift, and the baseline follows.
func diurnal(elapsed time.Duration) float64 {
	hours := elapsed.Hours()
	return 1 + diurnalAmplitude*math.Sin(2*math.Pi*(hours/24-0.25))
}

// burstRate is one event's contribution: a spike at the moment it happens,
// followed by an Omori-law aftershock tail.
func burstRate(background float64, burst domain.Burst, elapsed time.Duration) float64 {
	if elapsed < burst.At {
		return 0
	}
	// A burst on a mine with no background activity must still produce work,
	// so the magnitude is scaled against a floor rather than against zero.
	base := math.Max(background, 1)
	excess := base * (burst.Magnitude - 1)
	if excess <= 0 {
		return 0
	}

	since := elapsed - burst.At
	if burst.AftershockDecay <= 0 {
		// No aftershock sequence: the event is over almost at once. A minute
		// of elevated rate, so the flood is not a single instant nothing can
		// be sampled from.
		if since <= time.Minute {
			return excess
		}
		return 0
	}

	// Omori's law: the aftershock rate falls off as 1/t^p. The decay setting
	// is the timescale, so a three-hour decay still reads as clearly elevated
	// an hour later and close to background after a day.
	scaled := since.Seconds() / burst.AftershockDecay.Seconds()
	return excess / math.Pow(1+scaled*10, omoriExponent)
}

// peakRate is the highest rate the scenario reaches, which thinning needs as
// its envelope. It is computed by evaluating at the moments a peak can occur —
// each burst's own instant — plus the diurnal maximum, rather than by scanning
// the whole scenario.
func peakRate(mine domain.Mine, scenario domain.Scenario) float64 {
	peak := mine.BackgroundRate * (1 + diurnalAmplitude)
	for _, burst := range scenario.Bursts {
		if rate := rateAt(mine, scenario, burst.At); rate > peak {
			peak = rate
		}
	}
	return peak
}

// priorityTable turns the mix into a sorted, cumulative table so sampling is
// reproducible. Iterating the map directly would make the workload depend on
// Go's randomised map order, and two runs of one scenario would differ.
func priorityTable(scenario domain.Scenario) ([]domain.Priority, []float64) {
	priorities := scenario.SortedPriorities()

	cumulative := make([]float64, len(priorities))
	total := 0.0
	for i, priority := range priorities {
		total += scenario.PriorityMix[priority]
		cumulative[i] = total
	}
	for i := range cumulative {
		cumulative[i] /= total
	}
	return priorities, cumulative
}

func samplePriority(priorities []domain.Priority, cumulative []float64, random *rand.Rand) domain.Priority {
	draw := random.Float64()
	for i, threshold := range cumulative {
		if draw <= threshold {
			return priorities[i]
		}
	}
	return priorities[len(priorities)-1]
}

// sampleDuration spreads job durations around the scenario's nominal figure.
//
// Log-normal, because processing time is bounded below by zero and has a long
// right tail — a few jobs take far longer than typical, and those are the ones
// that hold an executor while the queue behind them ages.
func sampleDuration(nominal float64, random *rand.Rand) float64 {
	const sigma = 0.35
	// Shifting by -sigma^2/2 keeps the mean at the nominal value rather than
	// letting the tail drag it upward.
	return nominal * math.Exp(sigma*random.NormFloat64()-sigma*sigma/2)
}

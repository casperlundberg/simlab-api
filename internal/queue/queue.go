// Package queue is the job-level simulation a run replays through.
//
// The division of labour matters: this package owns the queue mechanics — jobs
// arriving, executors serving them, deadlines being missed — and the
// autoscaler owns the scaling decisions. Neither knows how the other works,
// which is what makes a run a genuine test of the autoscaler rather than a
// test of two halves of the same model agreeing with each other.
package queue

import (
	"math"
	"sort"
	"time"

	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/orchestrator"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// Deadlines is the SLA per priority level, with a fallback.
//
// These are the same numbers the autoscaler is configured with. A run whose
// deadlines differ from its target's is measuring one controller against a
// different rule from the one it was given, and the results mean nothing.
type Deadlines struct {
	Levels  map[domain.Priority]time.Duration
	Default time.Duration
}

// For returns the deadline for a level.
func (d Deadlines) For(priority domain.Priority) time.Duration {
	if deadline, ok := d.Levels[priority]; ok {
		return deadline
	}
	return d.Default
}

// Progress and Stats are the seam's types. Aliases rather than copies, so an
// orchestrator adapter for a real system does not have to import this package
// — which is the simulated implementation — to speak the interface.
type Progress = orchestrator.Progress

// Stats is what a whole run amounted to.
type Stats = orchestrator.Stats

// queued is a job waiting for an executor.
type queued struct {
	job workload.Job

	// remaining is how much execution time is still needed. It is less than
	// the job's own duration once an executor has started on it.
	remaining float64

	// started is when an executor first picked it up, which is what the wait
	// is measured to.
	started    time.Duration
	hasStarted bool

	// breached records that this job has already been counted, so a job that
	// waits ten times its deadline is still one missed SLA.
	breached bool
}

// Simulator replays a job log against a changing number of executors.
type Simulator struct {
	deadlines Deadlines

	// arrivals is the job log, ordered by submission, and next is how far
	// through it the clock has reached.
	arrivals []workload.Job
	next     int

	// levels is the waiting work, FIFO within each priority.
	levels map[domain.Priority][]*queued

	// running is work an executor has started but not finished. It is kept
	// separate so partial progress survives into the next interval — losing it
	// would let a queue of long jobs appear to be worked on forever without
	// anything ever completing.
	running []*queued

	now time.Duration

	// arrivalWindow is how far back the reported arrival rate looks.
	arrivalWindow time.Duration
	recent        []time.Duration

	submitted int
	completed int
	breached  int
	waits     []float64
	peakDepth int
}

// defaultArrivalWindow is how far back the reported arrival rate looks. Long
// enough not to be noise, short enough that a burst is visible while it is
// still happening rather than after it is over.
const defaultArrivalWindow = 5 * time.Minute

// New prepares a simulator over a job log. The log is sorted by submission
// time, so a caller may hand over jobs in any order.
func New(jobs []workload.Job, deadlines Deadlines) *Simulator {
	ordered := append([]workload.Job(nil), jobs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].SubmittedAt < ordered[j].SubmittedAt
	})

	return &Simulator{
		deadlines:     deadlines,
		arrivals:      ordered,
		levels:        map[domain.Priority][]*queued{},
		arrivalWindow: defaultArrivalWindow,
	}
}

// Advance moves the clock to an instant, admitting whatever arrived and
// serving what the given number of executors could do in between.
//
// Executors is the count that is *ready*: the autoscaler's pending capacity is
// on its way but cannot run anything, and counting it here would make the
// simulation kinder to the controller than reality is.
func (s *Simulator) Advance(to time.Duration, executors int) Progress {
	if to <= s.now {
		// Time in a replay only moves forward. A caller that went backwards
		// would double-count arrivals and produce a run nobody could explain,
		// so this does nothing rather than corrupting the state.
		return Progress{}
	}
	if executors < 0 {
		executors = 0
	}

	interval := (to - s.now).Seconds()
	s.now = to

	s.admitArrivals(to)

	// Measured here, after arrivals and before service: the peak is how deep
	// the backlog actually got, not what survived being worked on. Sampling it
	// afterwards would report zero for any interval the executors happened to
	// clear, which is exactly the interval worth knowing about.
	if depth := s.depth(); depth > s.peakDepth {
		s.peakDepth = depth
	}

	progress := Progress{Completed: s.serve(interval, executors)}
	progress.Breached = s.countBreaches(to)
	return progress
}

// admitArrivals moves everything submitted by now into the waiting queues.
func (s *Simulator) admitArrivals(to time.Duration) {
	for s.next < len(s.arrivals) && s.arrivals[s.next].SubmittedAt <= to {
		job := s.arrivals[s.next]
		s.levels[job.Priority] = append(s.levels[job.Priority], &queued{
			job: job, remaining: job.Seconds,
		})
		s.recent = append(s.recent, job.SubmittedAt)
		s.submitted++
		s.next++
	}

	// Trim the arrival-rate window from the front; it is ordered, so this is
	// cheap however long the run gets.
	cutoff := to - s.arrivalWindow
	drop := 0
	for drop < len(s.recent) && s.recent[drop] < cutoff {
		drop++
	}
	s.recent = s.recent[drop:]
}

// serve spends the interval's executor time on work, highest priority first.
//
// The budget is executor-seconds, with one rule that keeps it physical: no
// single job may receive more than the interval's own length, because an
// executor cannot give one job more than the time that actually passed. Within
// that, an executor is free to finish several short jobs in one interval,
// which is exactly what a real one does.
func (s *Simulator) serve(interval float64, executors int) int {
	budget := float64(executors) * interval
	if budget <= 0 {
		return 0
	}
	completed := 0

	// Work already in progress comes first: an executor that has started a job
	// stays on it.
	stillRunning := s.running[:0]
	for i, item := range s.running {
		if budget <= 0 {
			// The budget is spent, but the work still in progress is not
			// finished — it is merely untouched this interval, which is what
			// a scale-down looks like from in here. It has to be carried
			// forward: dropping it loses those jobs outright, never completed
			// and never breaching, in a run that claims to account for every
			// job it admitted.
			stillRunning = append(stillRunning, s.running[i:]...)
			break
		}

		work := math.Min(math.Min(interval, item.remaining), budget)
		item.remaining -= work
		budget -= work

		if item.remaining <= 1e-9 {
			s.complete(item)
			completed++
			continue
		}
		stillRunning = append(stillRunning, item)
	}
	s.running = stillRunning

	// Then whatever is waiting, most urgent level first. Strict priority is
	// what makes low-priority starvation a real outcome rather than a
	// theoretical one — and it is the behaviour the autoscaler predicts.
	for _, priority := range s.sortedLevels() {
		for budget > 1e-9 && len(s.levels[priority]) > 0 {
			item := s.levels[priority][0]

			work := math.Min(math.Min(interval, item.remaining), budget)
			if work <= 0 {
				break
			}
			if !item.hasStarted {
				item.started = s.now
				item.hasStarted = true
			}
			item.remaining -= work
			budget -= work
			s.levels[priority] = s.levels[priority][1:]

			if item.remaining <= 1e-9 {
				s.complete(item)
				completed++
				continue
			}
			s.running = append(s.running, item)
		}
		if budget <= 1e-9 {
			break
		}
	}

	s.pruneEmptyLevels()
	return completed
}

func (s *Simulator) complete(item *queued) {
	// Wait is submission to service, not submission to completion: it is the
	// queueing delay, which is what a deadline is actually about and what the
	// autoscaler's own breach test measures.
	wait := (item.started - item.job.SubmittedAt).Seconds()
	if wait < 0 {
		wait = 0
	}
	s.waits = append(s.waits, wait)
	s.completed++
}

// countBreaches marks jobs that have now waited longer than their level
// allows.
//
// Each level is FIFO and therefore ordered by age, so scanning stops at the
// first job that has not breached. A job is counted once however long it goes
// on waiting: ten cycles of lateness is still one missed SLA.
func (s *Simulator) countBreaches(now time.Duration) int {
	breached := 0

	for priority, level := range s.levels {
		deadline := s.deadlines.For(priority)
		for _, item := range level {
			if item.breached {
				continue
			}
			if now-item.job.SubmittedAt <= deadline {
				break
			}
			item.breached = true
			breached++
		}
	}

	// Work in progress can breach too: an executor picking a job up after its
	// deadline has already missed it.
	for _, item := range s.running {
		if item.breached {
			continue
		}
		if now-item.job.SubmittedAt > s.deadlines.For(item.job.Priority) {
			item.breached = true
			breached++
		}
	}

	s.breached += breached
	return breached
}

// Snapshot is the per-priority aggregate the autoscaler decides against.
//
// Only levels with something to report appear. A level with nothing waiting
// and nothing arriving is not part of the picture, and reporting it as depth
// zero would have the autoscaler keep carrying levels that no longer exist.
func (s *Simulator) Snapshot(now time.Duration) map[domain.Priority]domain.QueueSnapshot {
	arrivalsByLevel := s.recentArrivalsByLevel()

	out := map[domain.Priority]domain.QueueSnapshot{}
	for priority, level := range s.levels {
		if len(level) == 0 {
			continue
		}
		// FIFO, so the head is the oldest.
		oldest := (now - level[0].job.SubmittedAt).Seconds()
		if oldest < 0 {
			oldest = 0
		}
		out[priority] = domain.QueueSnapshot{
			Depth:               len(level),
			OldestJobAgeSeconds: oldest,
			ArrivalRate:         arrivalsByLevel[priority] / s.arrivalWindow.Seconds(),
		}
	}

	// A level that is empty but still receiving work has to be visible, or the
	// autoscaler cannot see the load coming.
	for priority, count := range arrivalsByLevel {
		if _, present := out[priority]; present || count == 0 {
			continue
		}
		out[priority] = domain.QueueSnapshot{
			ArrivalRate: count / s.arrivalWindow.Seconds(),
		}
	}
	return out
}

// recentArrivalsByLevel counts arrivals inside the rate window, per level.
func (s *Simulator) recentArrivalsByLevel() map[domain.Priority]float64 {
	counts := map[domain.Priority]float64{}
	cutoff := s.now - s.arrivalWindow

	// Walk backwards from where the clock has reached; the log is ordered, so
	// this touches only the window rather than the whole run.
	for i := s.next - 1; i >= 0; i-- {
		if s.arrivals[i].SubmittedAt < cutoff {
			break
		}
		counts[s.arrivals[i].Priority]++
	}
	return counts
}

// Done reports whether everything has arrived and been served.
func (s *Simulator) Done() bool {
	return s.next >= len(s.arrivals) && s.depth() == 0 && len(s.running) == 0
}

// Stats is the run's totals.
func (s *Simulator) Stats() Stats {
	stats := Stats{
		Submitted: s.submitted,
		Completed: s.completed,
		Breached:  s.breached,
		PeakDepth: s.peakDepth,
	}
	if len(s.waits) == 0 {
		return stats
	}

	sorted := append([]float64(nil), s.waits...)
	sort.Float64s(sorted)

	total := 0.0
	for _, wait := range sorted {
		total += wait
	}
	stats.MeanWaitSeconds = total / float64(len(sorted))
	stats.MaxWaitSeconds = sorted[len(sorted)-1]

	index := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	stats.P95WaitSeconds = sorted[index]
	return stats
}

func (s *Simulator) depth() int {
	total := len(s.running)
	for _, level := range s.levels {
		total += len(level)
	}
	return total
}

// sortedLevels is the priority levels present, most urgent first. Map
// iteration is randomised, and serving in a random order would make a replay
// non-reproducible.
func (s *Simulator) sortedLevels() []domain.Priority {
	out := make([]domain.Priority, 0, len(s.levels))
	for priority := range s.levels {
		out = append(out, priority)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

func (s *Simulator) pruneEmptyLevels() {
	for priority, level := range s.levels {
		if len(level) == 0 {
			delete(s.levels, priority)
		}
	}
}

// The simulated queue is one orchestrator among the intended several. Asserted
// here so that a change to either side is a compile error rather than something
// discovered when a real adapter is written.
var _ orchestrator.Orchestrator = (*Simulator)(nil)

// Kind names this implementation, as a platform adapter does.
func (s *Simulator) Kind() string { return "simulation" }

// Capabilities is what this queue admits being done to work already in it.
//
// Mutable priority is the one that matters: it is what the mine's intent needs,
// and it is the capability a real orchestrator has to be checked for before a
// result obtained here can be claimed to hold there.
//
// Preemption is deliberately absent. Most orchestrators only reorder what is
// still waiting, and a long low-priority job that has already started will
// finish first regardless of what happens to the queue behind it — which is
// precisely the effect an operator notices, so modelling it away would flatter
// the result.
func (s *Simulator) Capabilities() orchestrator.Capabilities {
	return orchestrator.Capabilities{
		MutablePriority:     true,
		PreemptsOnPromotion: false,
	}
}

// Reprioritise applies the mine's intent to work that is still waiting.
//
// Work already running is reported as TooLate rather than changed. Without
// preemption an executor that has started a job will finish it, so moving its
// priority would alter which deadline it is judged against without altering
// when it actually completes — the queue would report an improvement it did
// not make.
//
// A job that has already been counted as breached stays counted. Promotion is
// meant to prevent a missed deadline, not to erase one.
func (s *Simulator) Reprioritise(at time.Duration, updates []orchestrator.PriorityUpdate) orchestrator.Applied {
	applied := orchestrator.Applied{}

	for _, update := range updates {
		item, from, index := s.findWaiting(update.JobID)
		if item == nil {
			applied.TooLate++
			continue
		}
		if from == update.Priority {
			continue
		}

		s.levels[from] = append(s.levels[from][:index], s.levels[from][index+1:]...)
		item.job.Priority = update.Priority
		s.insertByArrival(update.Priority, item)
		applied.Changed++
	}
	return applied
}

// findWaiting locates a job that has not yet been dispatched.
func (s *Simulator) findWaiting(id domain.JobID) (*queued, domain.Priority, int) {
	// Iterating the map is safe here because the search is exhaustive and its
	// result does not depend on the order: a job is in exactly one level.
	for priority, level := range s.levels {
		for i, item := range level {
			if item.job.ID == id {
				return item, priority, i
			}
		}
	}
	return nil, 0, 0
}

// insertByArrival keeps a level FIFO by submission time, so a promoted job
// takes its rightful place among work of its new priority rather than jumping
// ahead of jobs that have been waiting at that level for longer.
func (s *Simulator) insertByArrival(priority domain.Priority, item *queued) {
	level := s.levels[priority]
	at := len(level)
	for i, existing := range level {
		if existing.job.SubmittedAt > item.job.SubmittedAt {
			at = i
			break
		}
	}
	level = append(level, nil)
	copy(level[at+1:], level[at:])
	level[at] = item
	s.levels[priority] = level
}

// Package queue is the job-level simulation a run replays through.
//
// The division of labour matters: this package owns the queue mechanics — jobs
// arriving, executors serving them, deadlines being missed — and the
// autoscaler owns the scaling decisions. Neither knows how the other works,
// which is what makes a run a genuine test of the autoscaler rather than a
// test of two halves of the same model agreeing with each other.
package queue

import (
	"fmt"
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

	// moving marks a job being taken out of its level by a reprioritisation,
	// and reprioritised one whose priority has ever been changed.
	moving        bool
	reprioritised bool

	// breached records that this job has already been counted, so a job that
	// waits ten times its deadline is still one missed SLA.
	breached bool

	// submitted is the priority the job arrived with. job.Priority is where
	// it sits now, which a reprioritisation moves; this never changes.
	submitted domain.Priority

	// deadlineFrom is what the job's deadline is measured from: its
	// submission, unless a reprioritisation restarted the clock. A level is
	// kept in this order, so its head is always the job closest to breaching.
	deadlineFrom time.Duration

	// exempt is whether the job may not be the reason cloud capacity is
	// bought. It changes only by reprioritisation.
	exempt bool

	// waiting is whether the job is in a level, as opposed to not yet
	// arrived, running, or done.
	waiting bool

	// breachedAsSubmitted records that the job waited past the deadline of
	// the level it was submitted at, measured from submission — the SLA it
	// was submitted under, whatever happened to it since.
	breachedAsSubmitted bool
}

// before is the order within a level: by deadline origin, then by identity,
// which for jobs of one submission time is the order they were submitted in.
func before(a, b *queued) bool {
	if a.deadlineFrom != b.deadlineFrom {
		return a.deadlineFrom < b.deadlineFrom
	}
	return a.job.ID < b.job.ID
}

// Simulator replays a job log against a changing number of executors.
type Simulator struct {
	deadlines Deadlines

	// arrivals is the job log, ordered by submission, and next is how far
	// through it the clock has reached.
	arrivals []workload.Job
	next     int

	// submitted work is what the mine handed over while the run was in
	// flight, which is how the pipeline's own stages arrive: a locate does not
	// exist until an associate sweep says so.
	//
	// Kept as a second stream rather than spliced into arrivals, because
	// splicing into a sorted slice costs a copy of its tail per submission and
	// a run submits thousands of times.
	submittedWork []workload.Job
	nextSubmitted int

	// levels is the waiting work, FIFO within each priority.
	levels map[domain.Priority][]*queued

	// running is work an executor has started but not finished. It is kept
	// separate so partial progress survives into the next interval — losing it
	// would let a queue of long jobs appear to be worked on forever without
	// anything ever completing.
	running []*queued

	now time.Duration

	// jobs is every admitted job by identity, which is how a reprioritisation
	// finds one without searching every level for it.
	jobs map[domain.JobID]*queued

	// exemptWaiting is how many waiting jobs are exempt from cloud burst.
	exemptWaiting int

	// arrivalWindow is how far back the reported arrival rate looks.
	arrivalWindow time.Duration
	recent        []time.Duration

	submitted int
	completed int
	breached  int

	breachedAsSubmitted int
	reprioritised       int
	waits               []float64
	peakDepth           int
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
		jobs:          map[domain.JobID]*queued{},
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

	finished, lateStarts := s.serve(interval, executors)
	progress := Progress{Completed: len(finished), Finished: finished}
	progress.Breached = lateStarts + s.countBreaches(to)
	return progress
}

// Submit hands the queue work that did not exist when the run began.
//
// This is how the mine's own workflow arrives: an associate sweep groups what
// is ready and submits a locate for each group, so the locate's deadline runs
// from the moment the sweep produced it rather than from the start of the run.
func (s *Simulator) Submit(jobs ...workload.Job) error {
	for _, job := range jobs {
		if _, taken := s.jobs[job.ID]; taken {
			return fmt.Errorf("job %d cannot be submitted: a job with that identity is already "+
				"in the queue, and identity is how intent and completions are attributed", job.ID)
		}
		for _, pending := range s.submittedWork[s.nextSubmitted:] {
			if pending.ID == job.ID {
				return fmt.Errorf("job %d cannot be submitted twice", job.ID)
			}
		}
		if job.SubmittedAt < s.now {
			// Admitting it late would give it a deadline that had already
			// started running somewhere the queue never saw it.
			job.SubmittedAt = s.now
		}
		s.submittedWork = append(s.submittedWork, job)
	}
	// Submissions are made as a run advances, so they arrive in time order
	// already; this only guards a caller that batches them out of order.
	sort.SliceStable(s.submittedWork[s.nextSubmitted:], func(i, j int) bool {
		return s.submittedWork[s.nextSubmitted+i].SubmittedAt <
			s.submittedWork[s.nextSubmitted+j].SubmittedAt
	})
	return nil
}

// admitArrivals moves everything submitted by now into the waiting queues.
func (s *Simulator) admitArrivals(to time.Duration) {
	for s.next < len(s.arrivals) && s.arrivals[s.next].SubmittedAt <= to {
		s.admit(s.arrivals[s.next])
		s.next++
	}
	for s.nextSubmitted < len(s.submittedWork) && s.submittedWork[s.nextSubmitted].SubmittedAt <= to {
		s.admit(s.submittedWork[s.nextSubmitted])
		s.nextSubmitted++
	}

	// Trim the arrival-rate window from the front; it is ordered, so this is
	// cheap however long the run gets. Submitted work is appended out of
	// order relative to the static log, so the window is re-sorted before it
	// is trimmed from the front.
	sort.Slice(s.recent, func(i, j int) bool { return s.recent[i] < s.recent[j] })
	cutoff := to - s.arrivalWindow
	drop := 0
	for drop < len(s.recent) && s.recent[drop] < cutoff {
		drop++
	}
	s.recent = s.recent[drop:]
}

// admit puts one job into its level and starts its deadline running.
func (s *Simulator) admit(job workload.Job) {
	item := &queued{
		job: job, remaining: job.Seconds, submitted: job.Priority,
		deadlineFrom: job.SubmittedAt, waiting: true,
	}
	s.levels[job.Priority] = append(s.levels[job.Priority], item)
	s.jobs[job.ID] = item
	s.recent = append(s.recent, job.SubmittedAt)
	s.submitted++
}

// serve spends the interval's executor time on work, highest priority first.
//
// The budget is executor-seconds, with one rule that keeps it physical: no
// single job may receive more than the interval's own length, because an
// executor cannot give one job more than the time that actually passed. Within
// that, an executor is free to finish several short jobs in one interval,
// which is exactly what a real one does.
//
// It also returns how many jobs breached by starting late. A job judged only
// while waiting or running at the end of an interval is missed if it starts
// past its deadline and finishes inside the same interval — a short job, late,
// is still late.
func (s *Simulator) serve(interval float64, executors int) ([]domain.JobID, int) {
	budget := float64(executors) * interval
	if budget <= 0 {
		return nil, 0
	}
	var completed []domain.JobID
	lateStarts := 0

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
			completed = append(completed, item.job.ID)
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
				if s.judgeStart(item) {
					lateStarts++
				}
			}
			item.remaining -= work
			budget -= work
			s.levels[priority] = s.levels[priority][1:]
			item.waiting = false
			if item.exempt {
				s.exemptWaiting--
			}

			if item.remaining <= 1e-9 {
				s.complete(item)
				completed = append(completed, item.job.ID)
				continue
			}
			s.running = append(s.running, item)
		}
		if budget <= 1e-9 {
			break
		}
	}

	s.pruneEmptyLevels()
	return completed, lateStarts
}

// judgeStart settles whether a job starting now has breached: against the
// level it holds, from its deadline origin, and against the level it was
// submitted at, from its submission. Nothing about either can change once it
// has started. It reports whether the first is a breach not already counted.
func (s *Simulator) judgeStart(item *queued) bool {
	if s.now-item.job.SubmittedAt > s.deadlines.For(item.submitted) && !item.breachedAsSubmitted {
		item.breachedAsSubmitted = true
		s.breachedAsSubmitted++
	}
	if item.breached || s.now-item.deadlineFrom <= s.deadlines.For(item.job.Priority) {
		return false
	}
	item.breached = true
	s.breached++
	return true
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
			if now-item.deadlineFrom <= deadline {
				break
			}
			item.breached = true
			breached++
		}
	}

	// Work in progress is not judged here. A job picked up after its deadline
	// was judged when it started, by judgeStart; one picked up in time has not
	// breached however long it then runs, because a deadline is on the wait
	// and not on the work. Judging running work by its age since submission
	// counted every long job as late.

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
		// In deadline order, so the head is the one closest to breaching, and
		// its age is the one the deadline is judged on.
		out[priority] = domain.QueueSnapshot{
			Depth:               len(level),
			OldestJobAgeSeconds: ageOf(level[0], now),
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

func ageOf(item *queued, now time.Duration) float64 {
	return max(0, (now - item.deadlineFrom).Seconds())
}

// SnapshotByBurst is Snapshot split in two: work that may be the reason cloud
// capacity is bought, and work exempt from that.
//
// Arrival rates stay with the counted work. Work arrives as it was submitted,
// and exemption is something intent does to it later; the load arriving is
// what brings it. With nothing exempt the counted half is Snapshot exactly and
// the exempt half is empty, so the autoscaler is shown what it always was.
func (s *Simulator) SnapshotByBurst(now time.Duration) (counted, exempt map[domain.Priority]domain.QueueSnapshot) {
	if s.exemptWaiting == 0 {
		return s.Snapshot(now), map[domain.Priority]domain.QueueSnapshot{}
	}
	arrivalsByLevel := s.recentArrivalsByLevel()
	counted = map[domain.Priority]domain.QueueSnapshot{}
	exempt = map[domain.Priority]domain.QueueSnapshot{}

	for priority, level := range s.levels {
		var kinds [2]domain.QueueSnapshot
		for _, item := range level {
			kind := &kinds[0]
			if item.exempt {
				kind = &kinds[1]
			}
			if kind.Depth == 0 {
				kind.OldestJobAgeSeconds = ageOf(item, now)
			}
			kind.Depth++
		}
		if kinds[0].Depth > 0 {
			kinds[0].ArrivalRate = arrivalsByLevel[priority] / s.arrivalWindow.Seconds()
			counted[priority] = kinds[0]
		}
		if kinds[1].Depth > 0 {
			exempt[priority] = kinds[1]
		}
	}
	for priority, count := range arrivalsByLevel {
		if _, present := counted[priority]; present || count == 0 {
			continue
		}
		counted[priority] = domain.QueueSnapshot{ArrivalRate: count / s.arrivalWindow.Seconds()}
	}
	return counted, exempt
}

// Waiting is what intent has done to the work still waiting: how many jobs sit
// below and above the priority they were submitted with, and which are exempt
// from cloud burst, by the priority they hold now. Like
// DepthBySubmittedPriority, the mine's record rather than part of the seam.
func (s *Simulator) Waiting() (decayed, promoted int, exempt map[domain.Priority]int) {
	exempt = map[domain.Priority]int{}
	for priority, level := range s.levels {
		for _, item := range level {
			switch {
			case priority < item.submitted:
				decayed++
			case priority > item.submitted:
				promoted++
			}
			if item.exempt {
				exempt[priority]++
			}
		}
	}
	return decayed, promoted, exempt
}

// DepthBySubmittedPriority is the waiting work counted by the priority each
// job was submitted at, where Snapshot counts it by the priority it holds now.
//
// Waiting only, as in Snapshot, so the two always total the same: work an
// executor has started is no longer in the queue either way it is counted.
// Never nil, because an empty queue and a count that was not taken are
// different statements.
//
// Not part of the orchestrator seam. A real orchestrator knows where work sits
// now; what it was submitted at is the mine's own record, and this simulator
// reports it only because it happens to hold both.
func (s *Simulator) DepthBySubmittedPriority() map[domain.Priority]int {
	out := map[domain.Priority]int{}
	for _, level := range s.levels {
		for _, item := range level {
			out[item.submitted]++
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
	return s.next >= len(s.arrivals) && s.nextSubmitted >= len(s.submittedWork) &&
		s.depth() == 0 && len(s.running) == 0
}

// Stats is the run's totals.
func (s *Simulator) Stats() Stats {
	stats := Stats{
		Submitted: s.submitted,
		Completed: s.completed,
		Breached:  s.breached,
		PeakDepth: s.peakDepth,

		BreachedAsSubmitted: s.breachedAsSubmitted,
		Reprioritised:       s.reprioritised,
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
//
// Updates arrive by the thousand once intent is on, so a batch is applied in
// one pass: every moving job is taken out of its level, and the movers are
// merged into their new levels in deadline order.
func (s *Simulator) Reprioritise(at time.Duration, updates []orchestrator.PriorityUpdate) orchestrator.Applied {
	applied := orchestrator.Applied{}
	leaving := map[domain.Priority]bool{}
	var moving []*queued

	for _, update := range updates {
		item := s.jobs[update.JobID]
		if item == nil || !item.waiting {
			applied.TooLate++
			continue
		}

		changed := false
		if update.BurstExempt != item.exempt {
			item.exempt = update.BurstExempt
			if item.exempt {
				s.exemptWaiting++
			} else {
				s.exemptWaiting--
			}
			changed = true
		}
		if update.Priority != item.job.Priority {
			if !item.moving {
				leaving[item.job.Priority] = true
				item.moving = true
				moving = append(moving, item)
			}
			item.job.Priority = update.Priority
			if !item.reprioritised {
				item.reprioritised = true
				s.reprioritised++
			}
			if update.RestartDeadline {
				item.deadlineFrom = at
			}
			changed = true
		}
		if changed {
			applied.Changed++
		}
	}
	if len(moving) == 0 {
		return applied
	}

	for _, priority := range sortedKeys(leaving) {
		kept := s.levels[priority][:0]
		for _, item := range s.levels[priority] {
			if !item.moving {
				kept = append(kept, item)
			}
		}
		s.levels[priority] = kept
	}

	sort.SliceStable(moving, func(i, j int) bool { return before(moving[i], moving[j]) })
	arriving := map[domain.Priority][]*queued{}
	for _, item := range moving {
		item.moving = false
		arriving[item.job.Priority] = append(arriving[item.job.Priority], item)
	}
	for _, priority := range sortedKeys(arriving) {
		s.levels[priority] = merge(s.levels[priority], arriving[priority])
	}
	s.pruneEmptyLevels()
	return applied
}

// merge is two levels, each in deadline order, as one.
func merge(level, arriving []*queued) []*queued {
	out := make([]*queued, 0, len(level)+len(arriving))
	i, j := 0, 0
	for i < len(level) && j < len(arriving) {
		// Ties keep the job already waiting first: it was at the level before
		// the one arriving.
		if before(arriving[j], level[i]) {
			out = append(out, arriving[j])
			j++
			continue
		}
		out = append(out, level[i])
		i++
	}
	out = append(out, level[i:]...)
	return append(out, arriving[j:]...)
}

func sortedKeys[V any](m map[domain.Priority]V) []domain.Priority {
	out := make([]domain.Priority, 0, len(m))
	for priority := range m {
		out = append(out, priority)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

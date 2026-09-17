// Package domain is Simlab's vocabulary: the mines whose workloads are being
// studied, the scenarios that describe a workload, the runs that replay one,
// and what a run produced.
//
// It knows nothing about Postgres, HTTP, or the autoscaler. Everything here is
// a value.
package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Priority is a job's urgency, matching the levels the autoscaler decides on.
// Higher is more urgent.
// JobID identifies one job within a run.
//
// It exists so the mine can say which piece of work its intent applies to.
// Derived from the scenario's seed and the job's position, never from a clock
// or a counter shared across runs: two runs of one scenario must produce the
// same ids, or a comparison between them cannot line up job for job.
type JobID int

type Priority int

// The priority levels this workload uses, named for what they mean in seismic
// processing. They are conventions, not constraints: a scenario may use any
// levels, and the autoscaler carries a deadline for each.
const (
	PriorityRelocate  Priority = 400
	PriorityAssociate Priority = 100
	PriorityLocate    Priority = 50
	PriorityPick      Priority = 25
	PriorityFloor     Priority = 0
)

// Mine is a site being modelled.
type Mine struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	// Sensors is how many seismic sensors the site runs. It sets the scale of
	// everything downstream: more sensors means more picks per event, and more
	// picks means more jobs.
	Sensors int `json:"sensors"`

	// BackgroundRate is events per hour when nothing unusual is happening.
	BackgroundRate float64 `json:"background_rate_per_hour"`

	// Layout is where the sensors are, and the volume they watch.
	//
	// Optional. A mine given only a sensor count still works: a layout is
	// derived from that count and the mine's id. From the id rather than a
	// scenario's seed, because sensors do not move between scenarios — two
	// scenarios on one mine that placed its array differently would be
	// comparing two mines. Set it to state a real array rather than accept a
	// generated one.
	Layout *Layout `json:"layout,omitempty"`

	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Validate rejects a mine that could not produce a workload.
func (m Mine) Validate() error {
	var problems []string
	if strings.TrimSpace(m.ID) == "" {
		problems = append(problems, "id is required")
	}
	if strings.TrimSpace(m.Name) == "" {
		problems = append(problems, "name is required")
	}
	if m.Sensors <= 0 {
		problems = append(problems, fmt.Sprintf("sensors must be > 0, got %d", m.Sensors))
	}
	if m.BackgroundRate < 0 {
		problems = append(problems, fmt.Sprintf("background_rate_per_hour must be >= 0, got %v",
			m.BackgroundRate))
	}
	if m.Layout != nil {
		problems = append(problems, m.Layout.problems(m.Sensors)...)
	}
	if len(problems) > 0 {
		return fmt.Errorf("mine is not usable: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Burst is a seismic event that floods the queue.
//
// This is the whole reason the system exists. Ordinary monitoring is steady
// and easy to provision for; a rock burst produces an hour of work in a minute
// and then keeps producing aftershocks for hours afterwards.
type Burst struct {
	// At is how far into the scenario the main event happens.
	At time.Duration `json:"at"`

	// Magnitude scales the immediate flood. It multiplies the background rate
	// at the instant of the event.
	Magnitude float64 `json:"magnitude"`

	// AftershockDecay is how long the aftershock sequence takes to fall back
	// towards background. Zero means no aftershocks.
	AftershockDecay time.Duration `json:"aftershock_decay"`

	// Epicentre is where in the rock it happened. Optional: unset, one is
	// drawn from the seed.
	//
	// This is ground truth. It is what the simulator knows and the mine does
	// not: the mine sees only picks, and works back to an estimate. Nothing
	// that decides processing order may read this, or the result would be
	// assuming what it set out to show.
	Epicentre *Point `json:"epicentre,omitempty"`

	// MainMagnitude is the Nuttli magnitude of the burst's main shock, the
	// first event it produces. Optional: unset takes a default. Unrelated to
	// Magnitude, which is how much the burst raises the event rate.
	MainMagnitude *float64 `json:"main_magnitude,omitempty"`
}

// Scenario is a workload to replay: a shape, a duration, and a seed.
type Scenario struct {
	ID     string `json:"id"`
	MineID string `json:"mine_id"`
	Name   string `json:"name"`

	// Duration is how much simulated time the scenario covers.
	Duration time.Duration `json:"duration"`

	// Bursts are the seismic events during it.
	Bursts []Burst `json:"bursts,omitempty"`

	// PriorityMix is the share of jobs at each level, as weights. They do not
	// have to sum to anything in particular.
	PriorityMix map[Priority]float64 `json:"priority_mix"`

	// JobSeconds is how long one job takes an executor to run.
	JobSeconds float64 `json:"job_seconds"`

	// PickJitter is the standard deviation of pick error: the difference
	// between when a wave reached a sensor and when the instrument said it
	// did. It is the dial that controls how uncertain the mine's epicentre
	// estimates are.
	//
	// A parameter rather than a constant because the question worth sweeping
	// is how good a location has to be before ordering by it stops helping.
	// Zero gives exact picks, which is a useful upper bound and not a
	// realistic instrument.
	PickJitter time.Duration `json:"pick_jitter"`

	// Workforce is who and what is underground. Optional: nil takes the
	// default workforce, and a stated one of zeroes means nobody.
	Workforce *Workforce `json:"workforce,omitempty"`

	// Seed makes a scenario reproducible. Two runs of the same scenario
	// replay exactly the same jobs, which is what makes comparing two
	// autoscaler settings a controlled experiment rather than an anecdote.
	Seed int64 `json:"seed"`

	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Validate rejects a scenario that could not be replayed.
func (s Scenario) Validate() error {
	var problems []string
	if strings.TrimSpace(s.ID) == "" {
		problems = append(problems, "id is required")
	}
	if strings.TrimSpace(s.MineID) == "" {
		problems = append(problems, "mine_id is required")
	}
	if s.Duration <= 0 {
		problems = append(problems, fmt.Sprintf("duration must be > 0, got %v", s.Duration))
	}
	if s.JobSeconds <= 0 {
		problems = append(problems, fmt.Sprintf("job_seconds must be > 0, got %v: a job that "+
			"takes no time makes every capacity calculation meaningless", s.JobSeconds))
	}
	if len(s.PriorityMix) == 0 {
		problems = append(problems, "priority_mix must name at least one priority level")
	}
	total := 0.0
	for _, weight := range s.PriorityMix {
		if weight < 0 {
			problems = append(problems, "priority_mix weights must be >= 0")
			break
		}
		total += weight
	}
	if s.Workforce != nil {
		problems = append(problems, s.Workforce.problems()...)
	}
	if s.PickJitter < 0 {
		problems = append(problems, fmt.Sprintf("pick_jitter_seconds must be >= 0, got %v",
			s.PickJitter.Seconds()))
	}
	if len(s.PriorityMix) > 0 && total <= 0 {
		problems = append(problems, "priority_mix weights are all zero, so no job could be given a priority")
	}
	for i, burst := range s.Bursts {
		if burst.At < 0 || burst.At > s.Duration {
			problems = append(problems, fmt.Sprintf(
				"burst %d happens at %v, outside the scenario's %v", i, burst.At, s.Duration))
		}
		if burst.Magnitude <= 0 {
			problems = append(problems, fmt.Sprintf("burst %d has magnitude %v, which is not a burst",
				i, burst.Magnitude))
		}
		if burst.AftershockDecay < 0 {
			problems = append(problems, fmt.Sprintf("burst %d has a negative aftershock decay", i))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("scenario is not usable: %s", strings.Join(problems, "; "))
	}
	return nil
}

// SortedPriorities is the scenario's priority levels, most urgent first, so
// anything generated from the mix is reproducible.
func (s Scenario) SortedPriorities() []Priority {
	out := make([]Priority, 0, len(s.PriorityMix))
	for priority := range s.PriorityMix {
		out = append(out, priority)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// RunMode is how a run reaches the infrastructure.
type RunMode string

const (
	// ModeSimulation replays a generated workload against an autoscaler target
	// on the simulation platform. The decisions are real; nothing is
	// provisioned; time is compressed.
	ModeSimulation RunMode = "simulation"

	// ModeLive drives a target on real infrastructure, in real time, and
	// records what actually happens.
	ModeLive RunMode = "live"
)

// RunStatus is where a run has got to.
type RunStatus string

const (
	StatusPending   RunStatus = "pending"
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

// Terminal reports whether a run has finished, one way or another.
func (s RunStatus) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// Run is one replay of a scenario against one autoscaler target.
type Run struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	ScenarioID string  `json:"scenario_id,omitempty"`
	TargetID   string  `json:"target_id"`
	Mode       RunMode `json:"mode"`

	Status RunStatus `json:"status"`

	// SimulatedStart is the instant the replayed clock begins at. A live run
	// uses real time and leaves it unset.
	SimulatedStart time.Time `json:"simulated_start,omitempty"`

	// TimeCompression is how much faster than real time a simulation replays.
	// 1 is real time; 3600 replays an hour a second.
	TimeCompression float64 `json:"time_compression,omitempty"`

	// DecisionInterval is the simulated gap between cycles. It should match
	// the target's own setting, or the run is measuring a different controller
	// from the one deployed.
	DecisionInterval time.Duration `json:"decision_interval"`

	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Error      string    `json:"error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// Validate rejects a run that could not be started.
func (r Run) Validate() error {
	var problems []string
	if strings.TrimSpace(r.ID) == "" {
		problems = append(problems, "id is required")
	}
	if strings.TrimSpace(r.TargetID) == "" {
		problems = append(problems, "target_id is required: a run has to drive some autoscaler target")
	}
	switch r.Mode {
	case ModeSimulation:
		if strings.TrimSpace(r.ScenarioID) == "" {
			problems = append(problems, "scenario_id is required for a simulation run: "+
				"there is nothing to replay without one")
		}
		if r.TimeCompression <= 0 {
			problems = append(problems, fmt.Sprintf("time_compression must be > 0, got %v",
				r.TimeCompression))
		}
	case ModeLive:
		// A live run observes whatever is really happening, so it needs no
		// scenario and cannot compress time.
	default:
		problems = append(problems, fmt.Sprintf("mode %q is not one of %q or %q",
			r.Mode, ModeSimulation, ModeLive))
	}
	if r.DecisionInterval <= 0 {
		problems = append(problems, fmt.Sprintf("decision_interval must be > 0, got %v",
			r.DecisionInterval))
	}
	if len(problems) > 0 {
		return fmt.Errorf("run is not usable: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Cycle is one decision within a run, with everything needed to explain it.
type Cycle struct {
	RunID string `json:"run_id"`

	// Sequence is the cycle's position in the run, from 1.
	Sequence int `json:"sequence"`

	// At is the simulated (or real) instant of the cycle.
	At time.Time `json:"at"`

	// Queues is the workload the decision was taken against, by the priority
	// each job holds now — which is the order the queue will serve it in.
	Queues map[Priority]QueueSnapshot `json:"queues"`

	// SubmittedDepths is the same waiting work counted by the priority each
	// job was submitted at.
	//
	// It differs from Queues only once something has changed a priority after
	// submission, and that difference is what it exists to show. Nil means
	// the cycle was recorded before this was tracked, which is not the same as
	// an empty queue — so it is not omitted from the wire when nil.
	SubmittedDepths map[Priority]int `json:"depth_by_submitted_priority"`

	// Capacity is what was running.
	LocalReady   int `json:"local_ready"`
	CloudReady   int `json:"cloud_ready"`
	LocalPending int `json:"local_pending"`
	CloudPending int `json:"cloud_pending"`

	// The autoscaler's answer, kept whole so a run can be re-read and the
	// reasoning checked rather than only the outcome.
	Action          string `json:"action"`
	PlanLocal       int    `json:"plan_local"`
	PlanCloud       int    `json:"plan_cloud"`
	Reason          string `json:"reason"`
	Constraint      string `json:"constraint,omitempty"`
	SettingsVersion int64  `json:"settings_version"`

	// BreachExpected is what the autoscaler predicted under its chosen plan.
	// Comparing it with what actually happened is the point of a run.
	BreachExpected bool `json:"breach_expected"`

	// Completed and Breached are what this cycle actually produced.
	Completed int `json:"completed"`
	Breached  int `json:"breached"`
}

// QueueSnapshot is one priority level at one instant.
type QueueSnapshot struct {
	Depth               int     `json:"depth"`
	OldestJobAgeSeconds float64 `json:"oldest_job_age_seconds"`
	ArrivalRate         float64 `json:"arrival_rate_per_second"`
}

// Metrics is what a run amounted to.
//
// These are the numbers a run exists to produce, and they are chosen so two
// runs of the same scenario under different settings can be compared directly:
// did it hold the SLA, and what did that cost.
type Metrics struct {
	RunID string `json:"run_id"`

	JobsSubmitted int `json:"jobs_submitted"`
	JobsCompleted int `json:"jobs_completed"`

	// SLABreaches is jobs that waited longer than their level's deadline.
	SLABreaches int `json:"sla_breaches"`

	// BreachRate is breaches as a share of completed jobs.
	BreachRate float64 `json:"breach_rate"`

	MeanWaitSeconds float64 `json:"mean_wait_seconds"`
	P95WaitSeconds  float64 `json:"p95_wait_seconds"`
	MaxWaitSeconds  float64 `json:"max_wait_seconds"`

	PeakQueueDepth int `json:"peak_queue_depth"`

	// Executor-seconds are the cost side. Cloud is separated because it is the
	// tier that is actually billed.
	LocalExecutorSeconds float64 `json:"local_executor_seconds"`
	CloudExecutorSeconds float64 `json:"cloud_executor_seconds"`

	PeakLocalExecutors int `json:"peak_local_executors"`
	PeakCloudExecutors int `json:"peak_cloud_executors"`

	ScalingActions int `json:"scaling_actions"`
	Cycles         int `json:"cycles"`
}

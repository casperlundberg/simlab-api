// Package run executes a run: it replays a workload against the real
// autoscaler and records what happened.
//
// A simulation run and a live run differ in exactly one respect. A simulation
// owns the queue — it generates the jobs, serves them with whatever capacity
// the autoscaler provisions, and moves a clock of its own. A live run owns
// nothing: it watches a target that is really scaling real infrastructure and
// records the decisions as they happen. The decision path is the autoscaler's
// in both cases.
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/casperlundberg/simlab-api/internal/autoscaler"
	"github.com/casperlundberg/simlab-api/internal/buildinfo"
	"github.com/casperlundberg/simlab-api/internal/domain"
	"github.com/casperlundberg/simlab-api/internal/intent"
	"github.com/casperlundberg/simlab-api/internal/observe"
	"github.com/casperlundberg/simlab-api/internal/pipeline"
	"github.com/casperlundberg/simlab-api/internal/queue"
	"github.com/casperlundberg/simlab-api/internal/workload"
)

// maxCycles bounds a run.
//
// A scenario whose queue never drains would otherwise loop until something
// else gave out. Stopping and saying so is far better than a run that is still
// going hours later with nobody able to say why.
const maxCycles = 200_000

// Spec is everything needed to execute one run.
type Spec struct {
	Run      domain.Run
	Mine     domain.Mine
	Scenario domain.Scenario

	// Settings is a patch applied to the ephemeral target a simulation run
	// creates. It is how one scenario is replayed under different policies.
	Settings json.RawMessage

	// Intent is how the mine reorders its queued work, and the changes to it
	// planned for later cycles. Nil reorders nothing, as every run did before
	// intent existed.
	Intent *domain.RunIntent

	// Control, when set, is how intent is changed while the run is in
	// flight. The run makes its own when it is not.
	Control *intent.Control
}

// Recorder is where a run's results go.
type Recorder interface {
	SaveCycle(ctx context.Context, cycle domain.Cycle) error
	SaveMetrics(ctx context.Context, metrics domain.Metrics) error
	SetStatus(ctx context.Context, runID string, status domain.RunStatus, failure string) error

	// SaveLayout records the sensor array a simulation's mine was modelled
	// with.
	SaveLayout(ctx context.Context, runID string, layout domain.Layout) error

	// SaveSeismicEvents records events as the mine knows them, replacing any
	// earlier record of the same events: all of them before the run starts,
	// then each one again as it is located and processed.
	SaveSeismicEvents(ctx context.Context, runID string, events []domain.SeismicEvent) error

	// SaveEntities records the people and vehicles underground, and where
	// they go.
	SaveEntities(ctx context.Context, runID string, entities []domain.Entity) error

	// SaveProvenance records which code produced the run and what it was
	// given, so it can be rebuilt and replayed.
	SaveProvenance(ctx context.Context, runID string, provenance domain.Provenance) error

	// SaveIntentChange records intent as it is from a cycle onwards.
	SaveIntentChange(ctx context.Context, runID string, change domain.IntentChange) error
}

// Publisher is how a run in flight reaches whoever is watching it.
type Publisher interface {
	Publish(event Event)
}

// Event is one thing that happened during a run.
type Event struct {
	RunID   string           `json:"run_id"`
	Type    string           `json:"type"`
	Status  domain.RunStatus `json:"status,omitempty"`
	Cycle   *domain.Cycle    `json:"cycle,omitempty"`
	Metrics *domain.Metrics  `json:"metrics,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// Event types.
const (
	EventStatus  = "status"
	EventCycle   = "cycle"
	EventMetrics = "metrics"
)

// Engine executes runs.
type Engine struct {
	autoscaler *autoscaler.Client
	recorder   Recorder
	publisher  Publisher

	// sleep is how the engine paces a run. Injectable so tests do not wait.
	sleep func(time.Duration)
	now   func() time.Time

	// build is this process's own, recorded with every run.
	build domain.Build
}

// New builds an engine.
func New(client *autoscaler.Client, recorder Recorder, publisher Publisher) *Engine {
	return &Engine{
		autoscaler: client,
		recorder:   recorder,
		publisher:  publisher,
		sleep:      func(d time.Duration) { time.Sleep(d) },
		now:        func() time.Time { return time.Now().UTC() },
		build:      buildinfo.Read(),
	}
}

// WithClock replaces the engine's pacing and clock, for tests.
func (e *Engine) WithClock(sleep func(time.Duration), now func() time.Time) *Engine {
	e.sleep = sleep
	e.now = now
	return e
}

// Execute runs a run to completion, recording as it goes.
func (e *Engine) Execute(ctx context.Context, spec Spec) (domain.Metrics, error) {
	if err := spec.Run.Validate(); err != nil {
		return domain.Metrics{}, err
	}

	e.setStatus(ctx, spec.Run.ID, domain.StatusRunning, "")

	var (
		metrics domain.Metrics
		err     error
	)
	switch spec.Run.Mode {
	case domain.ModeSimulation:
		metrics, err = e.simulate(ctx, spec)
	case domain.ModeLive:
		metrics, err = e.observe(ctx, spec)
	default:
		err = fmt.Errorf("run mode %q cannot be executed", spec.Run.Mode)
	}

	switch {
	case err != nil && ctx.Err() != nil:
		// Cancelled rather than broken. Whatever was recorded before the stop
		// is still worth keeping and reading.
		e.setStatus(ctx, spec.Run.ID, domain.StatusCancelled, "")
		return metrics, err
	case err != nil:
		e.setStatus(ctx, spec.Run.ID, domain.StatusFailed, err.Error())
		return metrics, err
	}

	if saveErr := e.recorder.SaveMetrics(context.WithoutCancel(ctx), metrics); saveErr != nil {
		e.setStatus(ctx, spec.Run.ID, domain.StatusFailed, saveErr.Error())
		return metrics, saveErr
	}
	e.publish(Event{RunID: spec.Run.ID, Type: EventMetrics, Metrics: &metrics})
	e.setStatus(ctx, spec.Run.ID, domain.StatusCompleted, "")
	return metrics, nil
}

// simulate replays a generated workload against an autoscaler target of its
// own.
//
// The target is created for the run and removed afterwards. That is what makes
// each run start from an empty fleet: a shared target would begin holding the
// previous run's executors, which the new run never provisioned and cannot
// account for, and two concurrent runs would fight over one fleet.
func (e *Engine) simulate(ctx context.Context, spec Spec) (domain.Metrics, error) {
	generated, err := workload.Build(spec.Mine, spec.Scenario)
	if err != nil {
		return domain.Metrics{}, err
	}
	catalogue := workload.NewCatalogue(generated)

	// With a workflow of its own, the mine decides when an event is located:
	// having the picks to solve for one only means a locate *can* be run, and
	// the location exists when that job finishes.
	var workflow *pipeline.Pipeline
	if spec.Scenario.Pipeline != nil {
		workflow = pipeline.Of(*spec.Scenario.Pipeline, generated, spec.Scenario.Seed)
		catalogue = catalogue.Deferred()
	}

	if err := e.createTarget(ctx, spec); err != nil {
		return domain.Metrics{}, err
	}
	// Removed even when the run fails; a failed run's leftover target would
	// accumulate silently until somebody noticed a registry full of them.
	defer func() {
		_ = e.autoscaler.DeleteTarget(context.WithoutCancel(ctx), spec.Run.TargetID)
	}()

	settings, err := e.autoscaler.GetSettings(ctx, spec.Run.TargetID)
	if err != nil {
		return domain.Metrics{}, err
	}
	deadlines, err := deadlinesOf(settings)
	if err != nil {
		return domain.Metrics{}, err
	}

	mine, scenario := spec.Mine, spec.Scenario
	runIntent := spec.Intent
	if runIntent == nil {
		runIntent = &domain.RunIntent{Settings: domain.IntentOffSettings()}
	}
	if err := runIntent.Validate(); err != nil {
		return domain.Metrics{}, err
	}
	provenance := e.provenance(ctx, settings, &mine, &scenario)
	provenance.Intent = runIntent
	if err := e.recorder.SaveProvenance(ctx, spec.Run.ID, provenance); err != nil {
		return domain.Metrics{}, err
	}

	// The mine is recorded as the run begins rather than regenerated when
	// someone looks: a scenario can be edited after it has been replayed, and
	// a view of the mine should show what the autoscaler was deciding against.
	if err := e.recorder.SaveLayout(ctx, spec.Run.ID, generated.Layout); err != nil {
		return domain.Metrics{}, err
	}
	if err := e.recorder.SaveSeismicEvents(ctx, spec.Run.ID, catalogue.Events()); err != nil {
		return domain.Metrics{}, err
	}
	if err := e.recorder.SaveEntities(ctx, spec.Run.ID, generated.Entities); err != nil {
		return domain.Metrics{}, err
	}

	simulator := queue.New(generated.Jobs, deadlines)
	control := spec.Control
	if control == nil {
		control = intent.NewControl(runIntent.Settings)
	}
	// What the planner may know of where people and vehicles are: what the
	// mine's own systems read, and the simulator's truth for the oracle arm.
	views := observe.ViewsOf(generated.Entities, generated.Layout.Tunnels, workload.WalkingSpeed)
	intents := newIntentLoop(runIntent, control, intent.New(generated, catalogue, runIntent.Settings, views))
	interval := spec.Run.DecisionInterval
	start := spec.Run.SimulatedStart
	if start.IsZero() {
		start = e.now()
	}

	// Pacing. A run is watched while it happens, so compression turns
	// simulated time into real time: 3600 replays an hour a second. Very large
	// values simply mean "as fast as it will go".
	pace := time.Duration(0)
	if spec.Run.TimeCompression > 0 {
		pace = time.Duration(interval.Seconds() / spec.Run.TimeCompression * float64(time.Second))
	}

	metrics := domain.Metrics{RunID: spec.Run.ID}
	capacity := autoscaler.Capacity{}

	for sequence := 1; ; sequence++ {
		if err := ctx.Err(); err != nil {
			return metrics, err
		}
		if sequence > maxCycles {
			return metrics, fmt.Errorf("run %q exceeded %d cycles without draining: the "+
				"scenario produces more work than the target's caps can ever serve",
				spec.Run.ID, maxCycles)
		}

		elapsed := time.Duration(sequence) * interval

		// Only ready executors do work. Pending capacity has been asked for
		// and is still starting, and counting it would make the simulation
		// kinder to the autoscaler than reality is.
		progress := simulator.Advance(elapsed, capacity.LocalReady+capacity.CloudReady)

		// The workflow reads the completions first: it is what knows which of
		// them were picks, and a sweep that finished in this interval submits
		// its locates before the autoscaler is shown the queue they join.
		finished := progress.Finished
		var located []domain.SeismicEvent
		if workflow != nil {
			step, err := workflow.Advance(elapsed, finished, intents.planner.Valued)
			if err != nil {
				return metrics, fmt.Errorf("cycle %d: %w", sequence, err)
			}
			if err := simulator.Submit(step.Submit...); err != nil {
				return metrics, fmt.Errorf("cycle %d: %w", sequence, err)
			}
			finished = step.Picks
			if located, err = catalogue.Locate(elapsed, step.Located); err != nil {
				return metrics, fmt.Errorf("cycle %d: %w", sequence, err)
			}
			metrics.Sweeps += stageCount(step.Submit, workload.StageAssociate)
			metrics.Locates += stageCount(step.Submit, workload.StageLocate)
		}

		// Before the loop can end: the interval that drains the queue is the
		// one that processes the last picks, and a location it produced but
		// never recorded would leave an event unlocated for good.
		changed, err := catalogue.Observe(elapsed, finished)
		if err != nil {
			return metrics, fmt.Errorf("cycle %d: %w", sequence, err)
		}
		located = append(located, changed...)
		if len(located) > 0 {
			if err := e.recorder.SaveSeismicEvents(ctx, spec.Run.ID, located); err != nil {
				return metrics, err
			}
		}

		if elapsed > spec.Scenario.Duration && simulator.Done() {
			break
		}

		// Intent acts on what the mine knows once this interval's work is in,
		// and before the autoscaler is shown the queue, so the queue it
		// decides against is the one intent has just left.
		summary, err := e.applyIntent(ctx, spec.Run.ID, sequence, elapsed, intents, simulator, catalogue)
		if err != nil {
			return metrics, fmt.Errorf("cycle %d: %w", sequence, err)
		}

		snapshot := simulator.Snapshot(elapsed)
		submitted := simulator.DepthBySubmittedPriority()
		counted, exempt := simulator.SnapshotByBurst(elapsed)
		result, err := e.autoscaler.Cycle(ctx, spec.Run.TargetID, autoscaler.CycleRequest{
			At:       start.Add(elapsed),
			Workload: toWorkload(counted, exempt, spec.Scenario.JobSeconds),
		})
		if err != nil {
			return metrics, fmt.Errorf("cycle %d: %w", sequence, err)
		}
		capacity = result.Observation.Capacity

		cycle := toCycle(spec.Run.ID, sequence, start.Add(elapsed), snapshot, result, progress)
		cycle.SubmittedDepths = submitted
		cycle.Intent = summary
		if err := e.recorder.SaveCycle(ctx, cycle); err != nil {
			return metrics, err
		}
		e.publish(Event{RunID: spec.Run.ID, Type: EventCycle, Cycle: &cycle})

		accumulate(&metrics, cycle, interval)

		if pace > 0 {
			e.sleep(pace)
		}
	}

	finalise(&metrics, simulator.Stats())
	return metrics, nil
}

// observe watches a target that is scaling real infrastructure and records
// what it does.
//
// It drives nothing. A live target is either running its own loop or being
// driven by whoever owns it, and a second controller issuing cycles would be
// two schedulers fighting over one fleet.
func (e *Engine) observe(ctx context.Context, spec Spec) (domain.Metrics, error) {
	metrics := domain.Metrics{RunID: spec.Run.ID}

	// A live run replays nothing, but which code decided and under what
	// settings is as much a part of its record as a simulation's.
	settings, err := e.autoscaler.GetSettings(ctx, spec.Run.TargetID)
	if err != nil {
		return metrics, err
	}
	if err := e.recorder.SaveProvenance(ctx, spec.Run.ID, e.provenance(ctx, settings, nil, nil)); err != nil {
		return metrics, err
	}

	interval := spec.Run.DecisionInterval

	var lastSeen time.Time
	sequence := 0

	for {
		if err := ctx.Err(); err != nil {
			// A live run has no natural end: it is stopped. Everything
			// recorded so far stands.
			finalise(&metrics, queue.Stats{})
			return metrics, err
		}

		status, err := e.autoscaler.TargetStatus(ctx, spec.Run.TargetID)
		if err != nil {
			return metrics, err
		}

		if decision := status.LastDecision; decision != nil && decision.At.After(lastSeen) {
			lastSeen = decision.At
			sequence++

			cycle := domain.Cycle{
				RunID: spec.Run.ID, Sequence: sequence, At: decision.At,
				Action: decision.Action, PlanLocal: decision.Plan.LocalExecutors,
				PlanCloud: decision.Plan.CloudExecutors, Reason: decision.Reason,
				Constraint: decision.Constraint, SettingsVersion: decision.SettingsVersion,
				BreachExpected: decision.Projection.BreachExpected,
			}
			if err := e.recorder.SaveCycle(ctx, cycle); err != nil {
				return metrics, err
			}
			e.publish(Event{RunID: spec.Run.ID, Type: EventCycle, Cycle: &cycle})

			metrics.Cycles++
			if decision.Action != "maintain" {
				metrics.ScalingActions++
			}
			metrics.PeakLocalExecutors = maxInt(metrics.PeakLocalExecutors, decision.Plan.LocalExecutors)
			metrics.PeakCloudExecutors = maxInt(metrics.PeakCloudExecutors, decision.Plan.CloudExecutors)
		}

		e.sleep(interval)
	}
}

func (e *Engine) createTarget(ctx context.Context, spec Spec) error {
	// The coldstarts the simulated fleet honours come from the settings the
	// run is being executed under, so a run that tunes coldstart is actually
	// testing that coldstart.
	config := map[string]string{}
	if seconds := settingNumber(spec.Settings, "local_coldstart_seconds"); seconds != nil {
		config["local_coldstart_seconds"] = strconv.Itoa(int(*seconds))
	}
	if seconds := settingNumber(spec.Settings, "cloud_coldstart_seconds"); seconds != nil {
		config["cloud_coldstart_seconds"] = strconv.Itoa(int(*seconds))
	}

	_, err := e.autoscaler.CreateTarget(ctx, autoscaler.Target{
		ID:     spec.Run.TargetID,
		Name:   "Simlab run " + spec.Run.ID,
		Kind:   "simulation",
		Mode:   "driven",
		Config: config,
	}, spec.Settings)
	if err != nil {
		return fmt.Errorf("preparing the autoscaler target for run %q: %w", spec.Run.ID, err)
	}
	return nil
}

// provenance is what this run is produced by and from, as it begins.
//
// An autoscaler that cannot report its build is recorded as unknown rather
// than failing the run: it can still decide, and the provenance says plainly
// that this run cannot be traced to its code.
func (e *Engine) provenance(ctx context.Context, settings autoscaler.SettingsSnapshot,
	mine *domain.Mine, scenario *domain.Scenario) domain.Provenance {
	p := domain.Provenance{
		RecordedAt:      e.now(),
		SimlabAPI:       e.build,
		Mine:            mine,
		Scenario:        scenario,
		Settings:        settings.Settings,
		SettingsVersion: settings.Version,
	}
	if build, err := e.autoscaler.Version(ctx); err == nil {
		p.Autoscaler = &domain.Build{
			Version: build.Version, Commit: build.Commit, Modified: build.Modified,
			GoVersion: build.GoVersion, Platform: build.Platform,
		}
	}
	return p
}

// deadlinesOf reads the SLA the target is actually configured with.
//
// Reading them rather than taking them from the run is what keeps a run
// honest: the queue simulation counts a breach against exactly the deadline
// the autoscaler was deciding against, so predicted and actual are comparable.
// A run that used its own numbers would be marking a different exam from the
// one the controller sat.
func deadlinesOf(snapshot autoscaler.SettingsSnapshot) (queue.Deadlines, error) {
	var settings struct {
		Deadlines map[string]float64 `json:"deadline_seconds_by_priority"`
		Default   float64            `json:"default_deadline_seconds"`
	}
	if err := json.Unmarshal(snapshot.Settings, &settings); err != nil {
		return queue.Deadlines{}, fmt.Errorf("reading the target's deadlines: %w", err)
	}

	levels := map[domain.Priority]time.Duration{}
	for key, seconds := range settings.Deadlines {
		priority, err := strconv.Atoi(key)
		if err != nil {
			return queue.Deadlines{}, fmt.Errorf("the target has a deadline for %q, which is "+
				"not a priority level", key)
		}
		levels[domain.Priority(priority)] = time.Duration(seconds * float64(time.Second))
	}

	fallback := time.Duration(settings.Default * float64(time.Second))
	if fallback <= 0 {
		fallback = 24 * time.Hour
	}
	return queue.Deadlines{Levels: levels, Default: fallback}, nil
}

func (e *Engine) setStatus(ctx context.Context, runID string, status domain.RunStatus, failure string) {
	// Status must be recorded even when the run was cancelled, or a stopped
	// run would sit at "running" forever.
	_ = e.recorder.SetStatus(context.WithoutCancel(ctx), runID, status, failure)
	e.publish(Event{RunID: runID, Type: EventStatus, Status: status, Error: failure})
}

func (e *Engine) publish(event Event) {
	if e.publisher != nil {
		e.publisher.Publish(event)
	}
}

// toWorkload turns a queue snapshot into what the autoscaler decides against:
// the work that may be the reason cloud capacity is bought, and the work exempt
// from that. The exempt half is left out entirely when empty.
func toWorkload(counted, exempt map[domain.Priority]domain.QueueSnapshot, jobSeconds float64) *autoscaler.Workload {
	wire := func(snapshot map[domain.Priority]domain.QueueSnapshot) map[string]autoscaler.QueueInfo {
		out := make(map[string]autoscaler.QueueInfo, len(snapshot))
		for priority, level := range snapshot {
			out[strconv.Itoa(int(priority))] = autoscaler.QueueInfo{
				Depth:               level.Depth,
				OldestJobAgeSeconds: level.OldestJobAgeSeconds,
				ArrivalRate:         level.ArrivalRate,
			}
		}
		return out
	}

	throughput := 1.0 / jobSeconds
	if jobSeconds <= 0 || math.IsInf(throughput, 0) {
		throughput = 1
	}
	workload := &autoscaler.Workload{Queues: wire(counted), ExecutorThroughput: throughput}
	if len(exempt) > 0 {
		workload.BurstExempt = wire(exempt)
	}
	return workload
}

// intentLoop is a run's intent over its cycles: the settings in force, the
// planned changes still to come, and the planner acting on them.
type intentLoop struct {
	control  *intent.Control
	planner  *intent.Planner
	schedule []domain.IntentStep
	next     int
	version  int
}

func newIntentLoop(runIntent *domain.RunIntent, control *intent.Control, planner *intent.Planner) *intentLoop {
	return &intentLoop{control: control, planner: planner, schedule: runIntent.Schedule}
}

// applyIntent brings intent's settings up to date for a cycle, recording any
// change as in force from it, then plans and reorders the queue.
func (e *Engine) applyIntent(ctx context.Context, runID string, sequence int, elapsed time.Duration,
	loop *intentLoop, simulator *queue.Simulator, catalogue *workload.Catalogue) (*domain.CycleIntent, error) {
	for loop.next < len(loop.schedule) && loop.schedule[loop.next].Cycle <= sequence {
		step := loop.schedule[loop.next]
		loop.next++
		source := step.Source
		if source == "" {
			source = domain.IntentSourceSchedule
		}
		current, _, _ := loop.control.Current()
		settings, err := current.Patched(step.Settings)
		if err != nil {
			return nil, fmt.Errorf("applying the intent planned for cycle %d: %w", step.Cycle, err)
		}
		if err := loop.control.Replay(settings, step.Version, source); err != nil {
			return nil, fmt.Errorf("applying the intent planned for cycle %d: %w", step.Cycle, err)
		}
	}

	settings, version, source := loop.control.Current()
	if version != loop.version {
		loop.planner.Configure(settings)
		loop.version = version
		change := domain.IntentChange{
			Version: version, Cycle: sequence, Source: source, Settings: settings, RecordedAt: e.now(),
		}
		if err := e.recorder.SaveIntentChange(ctx, runID, change); err != nil {
			return nil, err
		}
	}

	plan := loop.planner.Plan(elapsed)
	applied := simulator.Reprioritise(elapsed, plan.Updates)

	if len(plan.Transitions) > 0 {
		changed := make([]domain.SeismicEvent, 0, len(plan.Transitions))
		for _, transition := range plan.Transitions {
			changed = append(changed, catalogue.RecordIntent(transition.Event, transition.IntentTransition))
		}
		if err := e.recorder.SaveSeismicEvents(ctx, runID, changed); err != nil {
			return nil, err
		}
	}

	decayed, promoted, exempt := simulator.Waiting()
	summary := &domain.CycleIntent{
		Version: version, Decayed: decayed, Promoted: promoted,
		Changed: applied.Changed, TooLate: applied.TooLate,
	}
	for _, count := range exempt {
		summary.Exempt += count
	}
	if summary.Exempt > 0 {
		summary.ExemptByPriority = exempt
	}
	return summary, nil
}

// stageCount is how many of a workflow's submissions were of one stage.
func stageCount(jobs []workload.Job, stage workload.Stage) int {
	n := 0
	for _, job := range jobs {
		if job.Stage == stage {
			n++
		}
	}
	return n
}

func toCycle(runID string, sequence int, at time.Time,
	snapshot map[domain.Priority]domain.QueueSnapshot,
	result autoscaler.CycleResult, progress queue.Progress) domain.Cycle {
	return domain.Cycle{
		RunID: runID, Sequence: sequence, At: at,
		Queues:             snapshot,
		LocalReady:         result.Observation.Capacity.LocalReady,
		CloudReady:         result.Observation.Capacity.CloudReady,
		LocalPending:       result.Observation.Capacity.LocalPending,
		CloudPending:       result.Observation.Capacity.CloudPending,
		Action:             result.Decision.Action,
		PlanLocal:          result.Decision.Plan.LocalExecutors,
		PlanCloud:          result.Decision.Plan.CloudExecutors,
		Reason:             result.Decision.Reason,
		Constraint:         result.Decision.Constraint,
		SettingsVersion:    result.Decision.SettingsVersion,
		BreachExpected:     result.Decision.Projection.BreachExpected,
		BreachesExemptOnly: result.Decision.Projection.BreachesExemptOnly,
		Completed:          progress.Completed,
		Breached:           progress.Breached,
	}
}

// accumulate adds one cycle to the running totals.
//
// Executor-seconds are charged on what was ready during the interval, not on
// what was planned: capacity that spent the interval starting up did no work,
// and billing it would overstate the cost of every burst.
func accumulate(metrics *domain.Metrics, cycle domain.Cycle, interval time.Duration) {
	metrics.Cycles++
	if cycle.Action != "" && cycle.Action != "maintain" {
		metrics.ScalingActions++
	}

	seconds := interval.Seconds()
	metrics.LocalExecutorSeconds += float64(cycle.LocalReady) * seconds
	metrics.CloudExecutorSeconds += float64(cycle.CloudReady) * seconds

	metrics.PeakLocalExecutors = maxInt(metrics.PeakLocalExecutors, cycle.LocalReady+cycle.LocalPending)
	metrics.PeakCloudExecutors = maxInt(metrics.PeakCloudExecutors, cycle.CloudReady+cycle.CloudPending)
}

func finalise(metrics *domain.Metrics, stats queue.Stats) {
	metrics.JobsSubmitted = stats.Submitted
	metrics.JobsCompleted = stats.Completed
	metrics.SLABreaches = stats.Breached
	metrics.SLABreachesAsSubmitted = stats.BreachedAsSubmitted
	metrics.JobsReprioritised = stats.Reprioritised
	metrics.MeanWaitSeconds = stats.MeanWaitSeconds
	metrics.P95WaitSeconds = stats.P95WaitSeconds
	metrics.MaxWaitSeconds = stats.MaxWaitSeconds
	metrics.PeakQueueDepth = stats.PeakDepth

	if stats.Completed > 0 {
		metrics.BreachRate = float64(stats.Breached) / float64(stats.Completed)
	}
}

// settingNumber pulls one numeric field out of a settings patch, if it names
// one.
func settingNumber(settings json.RawMessage, field string) *float64 {
	if len(settings) == 0 {
		return nil
	}
	var document map[string]any
	if err := json.Unmarshal(settings, &document); err != nil {
		return nil
	}
	value, ok := document[field].(float64)
	if !ok {
		return nil
	}
	return &value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

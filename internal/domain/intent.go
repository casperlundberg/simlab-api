package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// IntentMode is what the mine's intent is allowed to do to work already
// queued.
type IntentMode string

const (
	// IntentOff leaves every job at the priority it was submitted with.
	IntentOff IntentMode = "off"

	// IntentDecay lowers the work of events that threaten nothing being
	// protected, now or along where it is going. It is a relaxation: nothing
	// becomes more urgent, so nothing new can demand capacity.
	IntentDecay IntentMode = "decay"

	// IntentPromote raises the work of events that put something being
	// protected at a high level of ground motion.
	IntentPromote IntentMode = "promote"

	// IntentBoth does both.
	IntentBoth IntentMode = "both"
)

// Decays reports whether the mode lowers work.
func (m IntentMode) Decays() bool { return m == IntentDecay || m == IntentBoth }

// Promotes reports whether the mode raises work.
func (m IntentMode) Promotes() bool { return m == IntentPromote || m == IntentBoth }

// IntentKnowledge is what intent is decided from.
type IntentKnowledge string

const (
	// KnowledgeEstimate is what a real mine has: locations and magnitudes
	// solved from processed picks, and before that only which sensors
	// triggered.
	KnowledgeEstimate IntentKnowledge = "estimate"

	// KnowledgeTruth is the simulator's ground truth, from the moment an
	// event happens. No mine has it. It is the oracle arm: the most any
	// ordering by location could achieve.
	KnowledgeTruth IntentKnowledge = "truth"
)

// DeadlineOrigin is what a reprioritised job's deadline is measured from.
type DeadlineOrigin string

const (
	// DeadlineFromArrival keeps the job's submission as its deadline origin,
	// so a change of priority can never hide how long it has waited. A job
	// promoted after waiting longer than its new level allows is late at
	// once — and, unless exempt from cloud burst, a reason to scale hard.
	DeadlineFromArrival DeadlineOrigin = "arrival"

	// DeadlineFromChange restarts the clock when the priority changes: the
	// job is judged as if it had arrived at its new level then.
	DeadlineFromChange DeadlineOrigin = "change"
)

// IntentClass is what intent has done to one job.
type IntentClass string

const (
	ClassDecayed  IntentClass = "decayed"
	ClassPromoted IntentClass = "promoted"

	// ClassRestored is a job back at the priority it was submitted with after
	// intent had moved it. With deadlines measured from arrival it is often
	// already late when it returns — which, to the autoscaler, is a breach no
	// capacity avoids.
	ClassRestored IntentClass = "restored"
)

// The hazard levels intent can be set to act at, weakest first. Named as the
// hazard package names them; domain imports nothing of ours, so they are
// listed here and checked against it by a test there.
var IntentLevels = []string{"moderate", "high", "very-high"}

// IntentSettings is how the mine reorders work it has already queued, from
// what it knows about where events are and who is near them.
//
// Every field can change while a run is in flight. A change is recorded with
// the cycle it took effect in, so a run whose intent was changed by hand can
// still be replayed exactly.
type IntentSettings struct {
	Mode      IntentMode
	Knowledge IntentKnowledge

	// PreLocation lets intent act on an event before it can be located, from
	// the sensor that triggered first. Its position stands in for the
	// hypocentre, PreLocationMagnitude for the magnitude, and the distance to
	// the fourth sensor to trigger — how far the event can be from the first
	// while those are still the nearest — for how far out that may be.
	PreLocation          bool
	PreLocationMagnitude float64

	// Protect is the kinds of entity whose safety decides what is important.
	Protect []string

	// Lookahead is how far along its planned route an entity is protected.
	// Zero protects only where things are now; anything more also protects
	// the rock they are travelling towards.
	Lookahead time.Duration

	// ProtectLevel is the ground motion at which an event threatens something:
	// an event reaching a protected path at this level keeps its priority, and
	// one that reaches none decays. PromoteLevel is the ground motion at which
	// an event's work is promoted.
	ProtectLevel string
	PromoteLevel string

	// Margin widens every reach, in metres, on top of the hazard zone.
	Margin float64

	// LocationUncertainty is how far, in metres, a location may be out. Zones
	// are widened by it, as the mine's exposure judgement is.
	LocationUncertainty float64

	// DecayTo and PromoteTo are the levels work is moved to. Decay never
	// raises a job and promotion never lowers one.
	DecayTo   Priority
	PromoteTo Priority

	DeadlineFrom DeadlineOrigin

	// Restore is whether decayed work returns to its submitted priority when
	// something protected comes within reach of its event. Without it decay is
	// final: a pure relaxation, at the cost of not protecting anyone who
	// arrives later than the lookahead could see.
	Restore bool

	// BurstExempt is which jobs, by what intent did to them, may not be the
	// reason cloud capacity is bought. They are still served, and still
	// counted as breaches when late: exemption is a decision to let them be
	// late rather than pay, not a way of hiding that they were.
	BurstExempt []IntentClass
}

// DefaultIntent is decay only, from estimates, protecting everyone and every
// vehicle where they were when an event happened and along the next five
// minutes of their route, with restored work exempt from cloud burst.
//
// Decay is the default because it relaxes rather than raises. That holds of
// decay itself, but not of restoring what was decayed: restored work comes
// back already late, and without the exemption each restore sent the
// autoscaler to its ceiling. Across the intent-modes sweep, decay with
// restored work exempt cut breaches by a quarter and cloud time by a
// twenty-fifth against no intent, while decay without it cost two-fifths more
// cloud time (platform-experiments reports/intent-modes).
func DefaultIntent() IntentSettings {
	return IntentSettings{
		Mode:                 IntentDecay,
		Knowledge:            KnowledgeEstimate,
		PreLocation:          false,
		PreLocationMagnitude: 1.5,
		Protect:              []string{EntityPerson, EntityCrewedVehicle, EntityAutonomousVehicle},
		Lookahead:            5 * time.Minute,
		ProtectLevel:         "moderate",
		PromoteLevel:         "high",
		Margin:               0,
		LocationUncertainty:  50,
		DecayTo:              PriorityFloor,
		PromoteTo:            PriorityRelocate,
		DeadlineFrom:         DeadlineFromArrival,
		Restore:              true,
		BurstExempt:          []IntentClass{ClassRestored},
	}
}

// IntentOffSettings is the defaults with intent switched off: every job keeps
// the priority it was submitted with.
func IntentOffSettings() IntentSettings {
	s := DefaultIntent()
	s.Mode = IntentOff
	return s
}

// Validate rejects settings intent cannot act on, naming every problem.
func (s IntentSettings) Validate() error {
	var problems []string
	oneOf := func(field, value string, allowed ...string) {
		for _, a := range allowed {
			if value == a {
				return
			}
		}
		problems = append(problems, fmt.Sprintf("%s %q is not one of %s", field, value,
			strings.Join(quoted(allowed), ", ")))
	}

	oneOf("mode", string(s.Mode), string(IntentOff), string(IntentDecay), string(IntentPromote), string(IntentBoth))
	oneOf("knowledge", string(s.Knowledge), string(KnowledgeEstimate), string(KnowledgeTruth))
	oneOf("protect_level", s.ProtectLevel, IntentLevels...)
	oneOf("promote_level", s.PromoteLevel, IntentLevels...)
	oneOf("deadline_from", string(s.DeadlineFrom), string(DeadlineFromArrival), string(DeadlineFromChange))
	for _, kind := range s.Protect {
		oneOf("protect", kind, EntityPerson, EntityCrewedVehicle, EntityAutonomousVehicle)
	}
	for _, class := range s.BurstExempt {
		oneOf("burst_exempt", string(class), string(ClassDecayed), string(ClassPromoted), string(ClassRestored))
	}
	if s.Lookahead < 0 {
		problems = append(problems, fmt.Sprintf("lookahead_seconds must be >= 0, got %v", s.Lookahead.Seconds()))
	}
	if s.Margin < 0 {
		problems = append(problems, fmt.Sprintf("margin_m must be >= 0, got %v", s.Margin))
	}
	if s.LocationUncertainty < 0 {
		problems = append(problems, fmt.Sprintf("location_uncertainty_m must be >= 0, got %v", s.LocationUncertainty))
	}
	if s.DecayTo >= s.PromoteTo {
		problems = append(problems, fmt.Sprintf("decay_to (%d) must be below promote_to (%d): "+
			"otherwise decaying a job could raise it and promoting one lower it", s.DecayTo, s.PromoteTo))
	}
	if len(problems) > 0 {
		return fmt.Errorf("intent settings are not usable: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Exempts reports whether jobs intent has done this to are exempt from cloud
// burst.
func (s IntentSettings) Exempts(class IntentClass) bool {
	for _, c := range s.BurstExempt {
		if c == class {
			return true
		}
	}
	return false
}

// Protects reports whether an entity of this kind is protected.
func (s IntentSettings) Protects(kind string) bool {
	for _, k := range s.Protect {
		if k == kind {
			return true
		}
	}
	return false
}

func quoted(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = fmt.Sprintf("%q", v)
	}
	return out
}

type intentWire struct {
	Mode                 IntentMode      `json:"mode"`
	Knowledge            IntentKnowledge `json:"knowledge"`
	PreLocation          bool            `json:"pre_location"`
	PreLocationMagnitude float64         `json:"pre_location_magnitude"`
	Protect              []string        `json:"protect"`
	LookaheadSeconds     float64         `json:"lookahead_seconds"`
	ProtectLevel         string          `json:"protect_level"`
	PromoteLevel         string          `json:"promote_level"`
	Margin               float64         `json:"margin_m"`
	LocationUncertainty  float64         `json:"location_uncertainty_m"`
	DecayTo              Priority        `json:"decay_to"`
	PromoteTo            Priority        `json:"promote_to"`
	DeadlineFrom         DeadlineOrigin  `json:"deadline_from"`
	Restore              bool            `json:"restore"`
	BurstExempt          []IntentClass   `json:"burst_exempt"`
}

func (s IntentSettings) toWire() intentWire {
	protect := s.Protect
	if protect == nil {
		protect = []string{}
	}
	exempt := s.BurstExempt
	if exempt == nil {
		exempt = []IntentClass{}
	}
	return intentWire{
		Mode: s.Mode, Knowledge: s.Knowledge,
		PreLocation: s.PreLocation, PreLocationMagnitude: s.PreLocationMagnitude,
		Protect: protect, LookaheadSeconds: s.Lookahead.Seconds(),
		ProtectLevel: s.ProtectLevel, PromoteLevel: s.PromoteLevel,
		Margin: s.Margin, LocationUncertainty: s.LocationUncertainty,
		DecayTo: s.DecayTo, PromoteTo: s.PromoteTo,
		DeadlineFrom: s.DeadlineFrom, Restore: s.Restore, BurstExempt: exempt,
	}
}

// MarshalJSON renders the settings with the lookahead in seconds.
func (s IntentSettings) MarshalJSON() ([]byte, error) { return json.Marshal(s.toWire()) }

// UnmarshalJSON applies a document onto the receiver, so a partial document is
// a patch and fields it does not mention keep their value; start from
// DefaultIntent to read a whole one. Unknown fields are refused: a misspelled
// key that is silently dropped tells an operator a change took effect when it
// did not.
func (s *IntentSettings) UnmarshalJSON(data []byte) error {
	wire := s.toWire()
	// A list present in the document replaces the list; decoding onto the
	// existing one would keep elements it did not mention.
	wire.Protect, wire.BurstExempt = nil, nil

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("reading intent settings: %w", err)
	}
	if wire.Protect == nil {
		wire.Protect = s.Protect
	}
	if wire.BurstExempt == nil {
		wire.BurstExempt = s.BurstExempt
	}
	*s = IntentSettings{
		Mode: wire.Mode, Knowledge: wire.Knowledge,
		PreLocation: wire.PreLocation, PreLocationMagnitude: wire.PreLocationMagnitude,
		Protect: wire.Protect, Lookahead: seconds(wire.LookaheadSeconds),
		ProtectLevel: wire.ProtectLevel, PromoteLevel: wire.PromoteLevel,
		Margin: wire.Margin, LocationUncertainty: wire.LocationUncertainty,
		DecayTo: wire.DecayTo, PromoteTo: wire.PromoteTo,
		DeadlineFrom: wire.DeadlineFrom, Restore: wire.Restore, BurstExempt: wire.BurstExempt,
	}
	return nil
}

// Patched is the settings with a patch applied and validated. The receiver is
// not changed, so a refused patch leaves nothing half-applied.
func (s IntentSettings) Patched(patch []byte) (IntentSettings, error) {
	out := s.clone()
	if len(bytes.TrimSpace(patch)) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(patch, &out); err != nil {
		return s, err
	}
	if err := out.Validate(); err != nil {
		return s, err
	}
	return out, nil
}

func (s IntentSettings) clone() IntentSettings {
	out := s
	out.Protect = append([]string{}, s.Protect...)
	out.BurstExempt = append([]IntentClass{}, s.BurstExempt...)
	return out
}

// IntentStep is a change of intent planned before a run starts: a patch that
// takes effect at the start of a cycle. It is how an experiment switches
// intent mid-run deterministically, and how a run whose intent an operator
// changed by hand is replayed.
type IntentStep struct {
	Cycle    int             `json:"cycle"`
	Settings json.RawMessage `json:"settings"`

	// Version and Source, when set, are the version and source the change
	// had when it was recorded, so a replay records it identically.
	Version int    `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
}

// RunIntent is the intent a run is created with: its settings, and the changes
// to them planned for later cycles.
type RunIntent struct {
	Settings IntentSettings `json:"settings"`
	Schedule []IntentStep   `json:"schedule"`
}

// Validate checks the settings, and that every step of the schedule applies
// in turn to what the steps before it left.
func (r RunIntent) Validate() error {
	if err := r.Settings.Validate(); err != nil {
		return err
	}
	current := r.Settings
	for i, step := range r.Schedule {
		if step.Cycle < 1 {
			return fmt.Errorf("intent schedule step %d is at cycle %d; cycles start at 1", i, step.Cycle)
		}
		if i > 0 && step.Cycle < r.Schedule[i-1].Cycle {
			return fmt.Errorf("intent schedule step %d is at cycle %d, before step %d at cycle %d: "+
				"steps apply in order", i, step.Cycle, i-1, r.Schedule[i-1].Cycle)
		}
		next, err := current.Patched(step.Settings)
		if err != nil {
			return fmt.Errorf("intent schedule step %d (cycle %d): %w", i, step.Cycle, err)
		}
		current = next
	}
	return nil
}

// UnmarshalJSON reads a run's intent, starting its settings from the defaults
// so a document naming only some of them is complete.
func (r *RunIntent) UnmarshalJSON(data []byte) error {
	var wire struct {
		Settings json.RawMessage `json:"settings"`
		Schedule []IntentStep    `json:"schedule"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	settings := DefaultIntent()
	if len(wire.Settings) > 0 {
		if err := json.Unmarshal(wire.Settings, &settings); err != nil {
			return err
		}
	}
	*r = RunIntent{Settings: settings, Schedule: wire.Schedule}
	return nil
}

// IntentChange is intent as it was from one cycle of a run onwards.
type IntentChange struct {
	Version int `json:"version"`

	// Cycle is the first cycle the settings were in force for.
	Cycle int `json:"cycle"`

	// Source is what changed it: "initial", "schedule", or "operator".
	Source   string         `json:"source"`
	Settings IntentSettings `json:"settings"`

	RecordedAt time.Time `json:"recorded_at"`
}

// Sources of an intent change.
const (
	IntentSourceInitial  = "initial"
	IntentSourceSchedule = "schedule"
	IntentSourceOperator = "operator"
)

// The states intent can put an event's work in.
const (
	// EventUnknown: nothing to act on yet — no location, and nothing that may
	// stand in for one.
	EventUnknown = "unknown"
	// EventKept: the work keeps the priority it was submitted with.
	EventKept     = "kept"
	EventDecayed  = "decayed"
	EventPromoted = "promoted"
)

// IntentTransition is intent changing its mind about one event.
type IntentTransition struct {
	At    time.Duration
	State string

	// Basis is what it was decided from: "location", "sensors" before a
	// location exists, or "truth".
	Basis string

	// Entity is the protected entity whose path came nearest, Distance how
	// near in metres, and Reach how far the deciding level of ground motion
	// extends from the event. Empty and zero when nothing is protected.
	Entity   string
	Distance float64
	Reach    float64
}

type intentTransitionWire struct {
	AtSeconds float64 `json:"at_seconds"`
	State     string  `json:"state"`
	Basis     string  `json:"basis,omitempty"`
	Entity    string  `json:"entity,omitempty"`
	Distance  float64 `json:"distance_m"`
	Reach     float64 `json:"reach_m"`
}

// MarshalJSON renders a transition with its time in seconds.
func (t IntentTransition) MarshalJSON() ([]byte, error) {
	return json.Marshal(intentTransitionWire{
		AtSeconds: t.At.Seconds(), State: t.State, Basis: t.Basis,
		Entity: t.Entity, Distance: t.Distance, Reach: t.Reach,
	})
}

// UnmarshalJSON reads a transition whose time is in seconds.
func (t *IntentTransition) UnmarshalJSON(data []byte) error {
	var wire intentTransitionWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*t = IntentTransition{
		At: seconds(wire.AtSeconds), State: wire.State, Basis: wire.Basis,
		Entity: wire.Entity, Distance: wire.Distance, Reach: wire.Reach,
	}
	return nil
}

// CycleIntent is what intent had done to the queue at one cycle.
type CycleIntent struct {
	// Version is the intent settings version in force.
	Version int `json:"version"`

	// Decayed, Promoted and Exempt count waiting jobs: those at a priority
	// below or above the one they were submitted with, and those exempt from
	// cloud burst.
	Decayed  int `json:"decayed"`
	Promoted int `json:"promoted"`
	Exempt   int `json:"exempt"`

	// ExemptByPriority is the exempt jobs by the priority they hold now.
	ExemptByPriority map[Priority]int `json:"exempt_by_priority,omitempty"`

	// Changed is how many jobs this cycle moved, and TooLate how many it
	// would have moved had an executor not already started them.
	Changed int `json:"changed"`
	TooLate int `json:"too_late"`
}

# Changelog

Every release, newest first. `make release` will not tag a version without a
section here, so the tag message and this file always agree.

## 4.0.0 — unreleased

MAJOR: a run whose intent protects people decides differently — a person is
now protected by every stretch of tunnel they could walk to, not by the route
the simulator will walk them.

- **What the mine can read, separated from what the simulator knows**
  (`internal/observe`). The planner reads where units are through a
  `Whereabouts` it is given, never from their tracks: the mine's own reading —
  every unit's position exactly, each vehicle's planned route, and for a person
  the tunnels they could reach over the lookahead at walking pace — or, for the
  oracle arm only, the simulator's truth. Both views keep one contract, held by
  one test: exact positions, a reach that covers everywhere the unit really
  goes, and a reach that moves no faster than the planner's skipping assumes.
  A test fails if the planner's own code reads a track.
- `GET /api/runs/{id}/ground` serves the ground the planner protects at a
  moment, under either knowledge, from the same views the run engine builds
  (`observe.ViewsOf`), so a page draws what intent decided from instead of
  recomputing it.
- **Use cases** (`internal/usecase`, `POST /api/runs/{id}/use-cases`): whether
  the information each tactical decision needed arrived before the decision
  stopped being useful. `turn-back`, `way-out` and `reroute`, over an event's
  true hazard zone; scored from what a run stored, so any recorded run can be
  asked. A decision waits on the event's first location, or — with `need` set
  to `warning` — on the first location whose own zone reaches the unit, which is
  where imperfect hypocentres cost a decision. See `docs/use-cases.md`.
- **Scripted encounters** (`encounters` on a scenario, `docs/use-cases.md`):
  events placed where a unit's own track is about to take it, timed so the unit
  reaches the edge of the zone exactly a stated lead after the event — so the
  decisions a case is about exist at a known notice instead of only where the
  day happened to give them, which at high ground motion is a handful a day.
  Drawn from a stream of its own after the day it is scripted into, so the same
  seed gives the same day with the encounters added to it.
- An event records **what produced it** (`activity`: work, blast, background or
  encounter) and, for an encounter, **the unit it was scripted for**, stored
  and served. A use case can be asked about one activity alone
  (`"activity": "encounter"`), and an encounter is then a decision for the unit
  it was scripted for and no other — which is what makes it an experiment at a
  known notice rather than more of the day.
- **The closure map** (use case 2: `POST /api/runs/{id}/closure`): the ground
  the mine's located hypocentres would have kept people out of, against the
  ground its events really made dangerous, in metre-seconds of tunnel — what
  was missed, what was closed needlessly, and how long after each event the map
  covered all of it. A union, so a neighbour's zone can cover an event nobody
  has located yet. Drawn from what a run stored, like the cases.
- **How a mine is worked** (`activity` on a scenario, `internal/activity`,
  `docs/activity.md`): the same daily rate from work around the faces being
  worked, the Omori sequence after each blast in a daily blasting window, and a
  background elsewhere — instead of evenly along every tunnel around the clock.
  Faces rotate; blasts fire at them in turn; each event records what produced
  it and the workload the blasts fired. Crews work the faces being worked and
  go to their level's shaft station while the production areas are cleared
  for blasting, until re-entry (`clear_seconds`, `reentry_seconds`). Optional,
  on streams of its own: a scenario without it replays exactly as before, its
  tracks included.
- **The application and its interface depend on ports they declare.** The run
  engine drives the autoscaler through `run.Autoscaler`, the run manager reads
  runs through `runner.Store`, and the HTTP handlers take a store, a manager and
  an autoscaler as interfaces split by what they are for (`api/ports.go`). Only
  the composition root knows the store is Postgres — now a test — and "not
  found" is the domain's own error. The planner takes the work as the mine
  sees it (`domain.Work`: priorities, and each event's triggers), its catalogue
  through an interface, and the oracle's truth as an explicit input, so it no
  longer imports the package that generates the world — also a test now. No
  behaviour changes.
- **The layering is enforced** (`internal/arch`), as autoscaler's is: every
  package's imports against a table, and the invariants that matter named —
  the simulated mine never imports the network or the database, what decides
  and what scores it never see each other, the queue and the autoscaler client
  never meet.
- `mineplan.Reach`: the ground within a distance of a point along the tunnels,
  held exactly to the graph's own route search by a property test.
- Nothing else moves: with intent off, or protecting no people, a scenario
  without a pipeline replays exactly as under 3.0.0.

- **The mine's workflow in a scenario** (`pipeline`): pick, associate, locate,
  with a trigger deciding when a sweep runs (`fixed`, `when-drained`,
  `just-in-time`, `adaptive`, and a pressure override that only ever sweeps
  earlier), each stage stating its own priority and cost. A finished sweep
  submits a locate per group before the autoscaler sees the queue they join,
  and an event is located when its locate finishes, not when its fourth pick
  does. The rules come from the operational extract
  (platform-experiments `docs/workflow-inference.md`).
- The queue takes work submitted during a run, with its deadline running from
  that moment; a run does not end with submitted work outstanding.
- Metrics count the `sweeps` and `locates` the workflow submitted.
- The same seed produces the same seismic events with and without a pipeline,
  so a comparison isolates the workflow.
- A development build is named after the release this file says it leads to —
  this section makes them 4.0.0-dev — rather than the next patch.

## 3.0.0 — 2026-09-18

MAJOR: a run created without stating `decay_to` decays to a different level, so
a recorded result for an unchanged scenario can change.

- **Decayed work goes below everything submitted** (`decay_to` defaults to −1,
  was 0). Sharing a level with submitted work put relaxed work in front of the
  low-priority picks of the events intent had kept. On a job mix calibrated to
  an operational catalogue — a quarter of it submitted at priority 0 — events
  that truly exposed someone finished five to seven times sooner once decay had
  a level of its own: 760 s against 2,151 s on a normal day, 1,767 s against
  9,047 s with a rock burst, and 7,154 s against 46,657 s with a medium
  earthquake (platform-experiments `reports/scenario-shapes`).
- `domain.PriorityBelowFloor` names that level.

## 2.0.1 — 2026-09-17

- Intent skips judging an event whose judgement cannot have changed: nothing
  protected moves faster than the quickest vehicle, so until the time since a
  judgement covers the distance it had to spare, no boundary can have been
  crossed. It also stops asking for work already where it asked for it. A whole
  simulated day of a busy mine now plans in seconds rather than minutes, and
  `TestSkippingSettledEventsDecidesExactlyWhatJudgingEverythingDoes` holds it to
  deciding exactly what judging everything every cycle decides.

## 2.0.0 — 2026-09-17

MAJOR, for two reasons a recorded result for an unchanged scenario can change:
simulation runs now decay work by default, and breaches are counted correctly.

- **Intent.** The mine reorders its queued work from what it knows about where
  events are and who is near them: decay (the default), promote, both, or off;
  from its estimates, or from the truth as an oracle arm; optionally before a
  location exists, from the sensors that triggered. What is protected is every
  protected person and vehicle and where its planned route takes it over a
  lookahead, in three dimensions — and where each was when an event happened,
  since an event that shook someone matters after they walk away. See
  `docs/intent.md`.
- **Exemption from cloud burst.** Decayed, promoted or restored work can be sent to the
  autoscaler as exempt: served, and counted when late, but never the reason
  cloud capacity is bought. Needs autoscaler 1.1.0 to take effect.
- **Deadline origin.** A moved job's deadline is measured from its arrival, or
  optionally from the change.
- **Restore.** Decayed work returns to its submitted priority when something
  protected comes within reach, unless `restore` is off; restored work is its
  own class for exemption, because it often returns already late, and is
  exempt by default. Without that, restores sent the autoscaler to its ceiling
  and decay cost two-fifths more cloud time than no intent at all.
- **Changing intent while a run is in flight**: `GET` and `PATCH
  /api/runs/{id}/intent`, with compare-and-swap. Every version that takes effect
  is recorded with its cycle, and replaying them as `intent_schedule` on a new
  run reproduces it; a schedule also switches intent mid-run deterministically.
- Recorded per cycle: what intent had done to the queue, and whether the
  autoscaler predicted only breaches of exempt work. Per event: every change of
  intent about its work, and why. Per run: `sla_breaches_as_submitted` and
  `jobs_reprioritised`. Provenance carries the intent a run began with.
- **Breaches are counted correctly.** A job that started past its deadline and
  finished inside the same interval was never counted, and a job started in
  time was counted as a breach once it had been running longer than its
  deadline. A breach is now exactly a wait longer than the deadline — so
  without intent, `sla_breaches` equals `sla_breaches_as_submitted`, and counts
  for the same scenario differ from 1.x.
- A run created without `intent` decays by default. Pass `"intent": {"mode":
  "off"}` to leave every job at its submitted priority, as 1.x did.
- `make db-up` waits for Postgres over TCP, not its socket.

## 1.0.0 — 2026-09-17

The first versioned release. 1.0.0 rather than 0.x because runs recorded by this
service are already research results, and a version has to be able to promise
whether a run replays the same way.

- Simulation runs that replay a seeded workload against the real autoscaler,
  recording every decision with its reasoning, and live runs that observe a
  real target.
- The virtual mine: a planned tunnel network with sensors installed in it,
  events around the workings with Gutenberg–Richter magnitudes, locations and
  magnitudes solved from processed picks only, and a moving workforce of people,
  crewed and autonomous vehicles.
- Exposure of people and vehicles judged twice with the Canadian Rockburst
  Support Handbook's ground-motion scaling law: by the simulator from the truth,
  and by the mine from its estimates.
- The queue counted by submitted and by current priority every cycle.
- Provenance on every run: both services' builds and copies of the mine,
  scenario and effective settings, with whether the run is reproducible and why
  not. `GET /api/version`.

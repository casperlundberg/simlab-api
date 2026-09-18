# Changelog

Every release, newest first. `make release` will not tag a version without a
section here, so the tag message and this file always agree.

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

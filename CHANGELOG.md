# Changelog

Every release, newest first. `make release` will not tag a version without a
section here, so the tag message and this file always agree.

## 2.0.0 — 2026-09-17

MAJOR, for two reasons a recorded result for an unchanged scenario can change:
simulation runs now decay work by default, and breaches are counted correctly.

- **Intent.** The mine reorders its queued work from what it knows about where
  events are and who is near them: decay (the default), promote, both, or off;
  from its estimates, or from the truth as an oracle arm; optionally before a
  location exists, from the sensors that triggered. What is protected is every
  protected person and vehicle and where its planned route takes it over a
  lookahead, in three dimensions. See `docs/intent.md`.
- **Exemption from cloud burst.** Decayed or promoted work can be sent to the
  autoscaler as exempt: served, and counted when late, but never the reason
  cloud capacity is bought. Needs autoscaler 1.1.0 to take effect.
- **Deadline origin.** A moved job's deadline is measured from its arrival, or
  optionally from the change.
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
  "off"}` for the behaviour of 1.x.
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

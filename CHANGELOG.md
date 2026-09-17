# Changelog

Every release, newest first. `make release` will not tag a version without a
section here, so the tag message and this file always agree.

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

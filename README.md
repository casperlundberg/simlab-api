# simlab-api

The backend of Simlab: a mining-workload simulation and emulation application.

Simlab drives scaling **runs**. A run replays a seismic-processing workload —
generated or recorded — and asks the real
[`autoscaler`](../autoscaler) service what to do about it, cycle by cycle. It
stores every observation, every decision and every resulting metric in
Postgres, and streams them to the frontend while the run is in flight.

Runs come in two modes, and the difference is entirely in which autoscaler
target the run is bound to:

- **simulation** — the target uses the autoscaler's `simulation` platform
  adapter. Decisions are computed by the real decision engine and reported
  back; nothing is provisioned. Time is compressed, so a two-day workload
  finishes in minutes.
- **emulation** — the target uses a real platform adapter (`kubernetes`,
  `colonyos-k8s`, `colonyos-container`). The same run definition drives actual
  executors on actual infrastructure, in real time.

The point of having both behind one interface is that the decision path is
identical. A simulation is not a model of the autoscaler; it *is* the
autoscaler, with a different adapter underneath.

## What a run produces

Every decision is stored with the reasoning behind it — what the queue looked
like, what was running, what the autoscaler decided, why, and what actually
happened in that interval. So a run can be read back and the controller's
judgement checked, not merely its outcome.

The metrics are chosen so two runs are directly comparable on the only two
questions that matter: did it hold the SLA, and what did that cost.

A simulation run also records its **virtual mine**: where the sensors are,
where each event happened, which sensors picked it up, and when the mine had
processed enough of those picks to locate it. That last time is set by the
queue, and so by the autoscaler — it is the operator-facing consequence of a
scaling decision. The location is solved from processed picks, never read from
the simulator's ground truth, which is recorded beside it for comparison.

The mine has tunnels, with its sensors installed in them and its events
clustered around them, and a workforce — people on foot, crewed vehicles and
autonomous haulers — moving through them. Each event carries a magnitude, and
who it exposes is judged twice with the rockburst handbook's ground-motion
scaling law: by the simulator, from where the event really was, and by the
mine, from its estimate and a 50 m allowance for location error.

The mine acts on that judgement through **intent**: work for events whose
hazard zone reaches no protected person or vehicle, or where they are heading,
decays; optionally, work for events that put someone at high risk is promoted,
and either kind can be exempted from buying cloud capacity. Intent can be
changed while a run is in flight, and every change is recorded with the cycle
it took effect from so the run can still be reproduced.
[`docs/intent.md`](docs/intent.md) has the model and every setting.

Because a scenario carries a seed, two runs replay exactly the same jobs. The
difference between their results is attributable to the settings that changed
and to nothing else — which is what makes it an experiment rather than an
anecdote.

## Documentation

- [`docs/intent.md`](docs/intent.md) — how the mine reorders its work: protected volumes, decay, promotion, exemption from cloud burst
- [`docs/interactions.md`](docs/interactions.md) — sequence diagrams for a simulation run, a live run, and comparing two policies
- [`docs/development.md`](docs/development.md) — working on it, driving it by hand against a real autoscaler
- [`api/openapi.yaml`](api/openapi.yaml) — the HTTP contract, checked against the routes by a test

## Versions

Releases are [semantic versions](https://semver.org), tagged `vMAJOR.MINOR.PATCH`
and cut with

```bash
make release VERSION=1.3.0
```

which refuses a dirty tree, a branch other than `main`, a `main` behind its
remote, a version not above the last release, a `CHANGELOG.md` with no section
for it, and failing checks — then tags, with the changelog section as the tag
message, and pushes the commit and the tag together so CI stamps the image with
the release. Between releases a build is a pre-release of the release the
changelog's unreleased section names — `2.0.0-dev.N+<commit>` under a section
headed `## 2.0.0 — unreleased`, the next patch without one — and `.dirty`
when built with uncommitted changes (`make version` prints it). Every binary
carries its version and commit; `GET /api/version` reports them.

For a service whose output is research results, a version answers one question
besides "will my client break": **will a run replay the same way?**

- **MAJOR** — the HTTP API changes incompatibly, or an existing scenario under
  the same settings produces a different result: different jobs, queue
  behaviour, recorded mine, locations or exposure. Results from two majors are
  not comparable without saying so.
- **MINOR** — something is added (an endpoint, a field, newly recorded data)
  and every existing result is as it was.
- **PATCH** — a fix that changes no result and no contract.

`TestGeometryChangesNotOneJobOfAnExistingScenario` pins the job stream of the
scenarios with runs recorded against them; a change that moves those digests is
a MAJOR release by definition.

### Provenance

Every run records, as it begins, both services' builds and copies of the mine,
scenario and effective settings it was given — `provenance` on
`GET /api/runs/{id}`, with `reproducible` and, when it is not, the reasons. The
database is tied to the code by commit, and no data is committed.
`make -C ../platform-experiments reproduce RUN=<id>` rebuilds both commits and
replays the run from its provenance, and exits zero only on an identical
replay.

## Running

```bash
make db-up         # a scratch Postgres on :15432
make test          # unit tests (no database needed)
make test-db       # everything, including the database and API suites
make run           # serve on :8081
```

Deployment is `deploy/chart`, which can bring its own Postgres for a
self-contained install. The whole platform in one namespace — this service,
the autoscaler, the frontend and a database — is composed in the
**platform-deploy** repository, which also holds `verify.sh`: it runs
everything for real and checks it does what it claims. `SIMLAB_DATABASE_URL` and `SIMLAB_AUTOSCALER_URL` have
no defaults: without either, this service would start happily and fail every
run, which is much harder to diagnose than a service that will not start.

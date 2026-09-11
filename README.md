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

Because a scenario carries a seed, two runs replay exactly the same jobs. The
difference between their results is attributable to the settings that changed
and to nothing else — which is what makes it an experiment rather than an
anecdote.

## Documentation

- [`docs/interactions.md`](docs/interactions.md) — sequence diagrams for a simulation run, a live run, and comparing two policies
- [`docs/development.md`](docs/development.md) — working on it, driving it by hand against a real autoscaler
- [`api/openapi.yaml`](api/openapi.yaml) — the HTTP contract, checked against the routes by a test

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

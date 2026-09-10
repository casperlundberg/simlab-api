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

## Running

```bash
make test          # unit tests (no database needed)
make test-db       # integration tests against a scratch Postgres
make migrate       # apply migrations to $SIMLAB_DATABASE_URL
make run           # serve on :8081
```

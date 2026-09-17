# Working on Simlab's backend

## Getting started

```bash
make db-up      # a scratch Postgres on :15432
make test       # unit tests; no database needed
make test-db    # everything, including the database and API suites
make run        # serve on :8081
make db-down
```

`make test-db` runs one package at a time. Go tests packages in parallel, and
both the store and API suites clear the shared database on entry, so in
parallel each deletes the other's rows mid-test.

## Running it against a real autoscaler

```bash
# In the autoscaler repo
AUTOSCALER_API_TOKEN=dev make run &

# Here
export SIMLAB_DATABASE_URL='postgres://simlab:simlab@127.0.0.1:15432/simlab_test?sslmode=disable'
export SIMLAB_AUTOSCALER_URL=http://localhost:8080
export SIMLAB_AUTOSCALER_TOKEN=dev
make run &

curl -s localhost:8081/api/mines -H 'Content-Type: application/json' \
  -d '{"id":"storhall","name":"Storhall","sensors":40,"background_rate_per_hour":60}' | jq

curl -s localhost:8081/api/scenarios -H 'Content-Type: application/json' -d '{
  "id":"burst","mine_id":"storhall","name":"Rock burst",
  "duration_seconds":3600,"job_seconds":20,"seed":42,
  "priority_mix":{"100":1,"25":3},
  "bursts":[{"at_seconds":600,"magnitude":30,"aftershock_decay_seconds":900}]}' | jq

RUN=$(curl -s localhost:8081/api/runs -H 'Content-Type: application/json' -d '{
  "name":"Baseline","mode":"simulation","scenario_id":"burst",
  "decision_interval_seconds":15,"time_compression":100000,
  "settings":{"local_executor_cap":20,"cloud_executor_cap":40}}' | jq -r .id)

curl -sN localhost:8081/api/runs/$RUN/events    # watch it happen
curl -s localhost:8081/api/runs/$RUN/metrics | jq
```

Change one setting, run it again, and compare. Because the seed fixes the
workload, the difference is the settings.

## Where things live

| Package | What it owns |
|---|---|
| `internal/domain` | Mines, scenarios, runs, cycles, metrics. Values only. |
| `internal/workload` | Turning a scenario into jobs and the mine they came from — sensor layout, event epicentres, picks — deterministically from its seed. Also the `Catalogue`: the mine locating events as its picks are processed. |
| `internal/seismic` | The physics: travel times, picks, solving a location from picks. |
| `internal/mineplan` | The mine's development: shaft, ramp, levels, drives and crosscuts; sensors installed in them; the tunnel network as a graph to travel. |
| `internal/hazard` | How much an event threatens a place: the Canadian Rockburst Support Handbook's scaling law, ppv = C*·√(10^(mN+1))/R, its near-field limit, and ground-motion levels. |
| `internal/intent` | The mine's intent: protected paths over the lookahead, judging each event from what the mine knows, the updates that move its work, and the runtime control an operator changes it through. |
| `internal/orchestrator` | The seam in front of the queue, with the capabilities a real orchestrator declares. |
| `internal/queue` | The job-level simulation: arrivals, service, deadlines missed. |
| `internal/autoscaler` | The client for the autoscaler service, plus a fake to test against. |
| `internal/run` | The engine: replay a workload, or watch real infrastructure. |
| `internal/store` | Postgres, and the migrations. |
| `internal/events` | Fanning a run out to whoever is watching. |
| `internal/runner` | Runs in flight: starting, stopping, knowing what is going. |
| `internal/api` | HTTP, SSE, and standing in front of the autoscaler. |
| `internal/app` | Wiring and process lifecycle. |

The division that matters most is between `queue` and the autoscaler.
`queue` owns the queue mechanics; the autoscaler owns the scaling decisions.
Neither knows how the other works, which is what makes a run a real test of
the autoscaler rather than two halves of one model agreeing with each other.
If you find yourself wanting the queue simulator to know what the autoscaler
decided, or the run engine to make a scaling decision of its own, stop: that
is the seam that makes the results mean something.

## Adding a workload feature

Anything that changes what jobs get generated goes in `internal/workload` and
must stay deterministic from the seed. Two rules:

- Draw from the injected PRNG, never from `math/rand`'s package-level
  functions, which share global state — one run's output would then depend on
  what else the process was doing.
- Never iterate a map to make a decision. Go randomises map order, and the
  same scenario would replay different jobs. `SortedPriorities` exists for
  exactly this.
- A new dial draws from a stream of its own. Jobs, event geometry and pick
  noise each have a separate PCG stream off the seed, so turning pick jitter
  moves no event and adding geometry moved no job. Sharing one stream would
  make every dial move everything drawn after it.
  `TestGeometryChangesNotOneJobOfAnExistingScenario` pins the job stream of
  the scenarios with runs already recorded against them; if a change has to
  move it, that is a decision to say out loud, not a digest to update quietly.

The mine never reads an event's `Truth`. A location is solved from the picks
the queue has reported processed, as a real installation would have to; the
truth is recorded beside it only so the two can be compared.

Statistical assertions need enough events to be stable. At twenty events an
hour the sampling noise is wider than most effects worth testing, so the
existing tests raise the background rate rather than widen their tolerances
until they prove nothing.

## Conventions

- **Tests first**, named as sentences about behaviour.
- **Durations are seconds on the wire**, `time.Duration` in Go, milliseconds
  in Postgres (which has no duration type, hence the explicit `_ms` suffix).
- **No sleeping in tests.** The run engine's pacing and clock are injected.
- **The OpenAPI document is checked against the routes** by a test. If you add
  an endpoint, the build tells you to document it.

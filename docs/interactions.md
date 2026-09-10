# How Simlab works

## A simulation run

The substitution happens at exactly one point — the autoscaler's simulation
adapter — and everything above it is the code that runs in production.

```mermaid
sequenceDiagram
    participant Web as simlab-web
    participant API as simlab-api
    participant Q as queue simulator
    participant AS as autoscaler
    participant DB as Postgres

    Web->>API: POST /api/runs {mode: simulation, scenario, settings}
    API->>DB: save the run as pending
    API->>AS: POST /v1/targets — a simulation target for this run alone
    API->>AS: GET settings — read back the deadlines it will decide against
    Note over API: the queue counts breaches against exactly<br/>those deadlines, so predicted and actual compare
    API->>API: generate the jobs from the scenario's seed
    API-->>Web: 202 Accepted, run id

    loop each cycle, on the run's own compressed clock
        API->>Q: advance(elapsed, ready executors)
        Q-->>API: jobs completed, deadlines missed
        Q-->>API: queue depth and oldest age, per priority
        API->>AS: POST /cycle {at: simulated, workload}
        AS->>AS: the real engine, real settings, real hysteresis
        AS-->>API: decision + reason + projection + capacity
        API->>DB: save the cycle
        API-->>Web: stream it
    end

    API->>DB: save the metrics
    API->>AS: DELETE the target
    API-->>Web: stream completed
```

Two things in that diagram do most of the work. The deadlines are read back
from the target rather than taken from the run, so a breach is counted against
exactly the rule the controller was deciding under. And the target is created
and destroyed with the run, so no run ever starts holding executors a previous
one provisioned.

## A live run

Simlab watches. It does not drive: a second controller issuing decisions for
the same target would be two schedulers fighting over one fleet.

```mermaid
sequenceDiagram
    participant API as simlab-api
    participant AS as autoscaler
    participant K8s as real infrastructure
    participant DB as Postgres

    Note over AS,K8s: the autoscaler's own loop is already running
    AS->>K8s: observe, decide, provision

    loop at the run's interval
        API->>AS: GET /v1/targets/{id}/status
        AS-->>API: the last decision, with its reasoning
        alt this decision is new
            API->>DB: record it
            API-->>API: stream it
        else already seen
            Note over API: recorded once, however long it stands
        end
    end
```

## Comparing two policies

The comparison the whole application exists to make. Same scenario, same seed,
same jobs; different settings.

```mermaid
flowchart LR
    S[Scenario<br/>seed 42] --> A[Run A<br/>local cap 10, no cloud]
    S --> B[Run B<br/>local cap 10, cloud 40]
    A --> AM[breaches: 812<br/>cloud seconds: 0]
    B --> BM[breaches: 4<br/>cloud seconds: 21,600]
    AM --> C{What is a<br/>missed deadline<br/>worth?}
    BM --> C
```

Because the seed fixes the workload, the difference between those two columns
is attributable to the settings and nothing else. That is what makes it an
experiment rather than an anecdote.

## Who talks to whom

```mermaid
flowchart TD
    Browser[simlab-web<br/>in a browser] -->|/api| API[simlab-api]
    API --> DB[(Postgres)]
    API -->|bearer token| AS[autoscaler]
    AS --> K8s[Kubernetes]
    AS --> Colony[ColonyOS]
    AS --> Docker[Docker hosts]
    AS -.->|simulation adapter<br/>provisions nothing| Sim[( )]

    style Browser fill:#e8f0fe
    style Sim fill:#f5f5f5
```

The browser never reaches the autoscaler. That token unlocks every platform
credential the autoscaler holds, and a token that reaches a browser has been
disclosed.

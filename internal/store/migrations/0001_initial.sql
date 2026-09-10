-- Mines are the sites whose workloads are studied.
CREATE TABLE mines (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    sensors         INTEGER NOT NULL CHECK (sensors > 0),
    background_rate DOUBLE PRECISION NOT NULL CHECK (background_rate >= 0),
    description     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Scenarios describe a workload to replay. The seed is what makes two runs of
-- one scenario a controlled comparison rather than two anecdotes, so it is
-- stored rather than regenerated.
CREATE TABLE scenarios (
    id            TEXT PRIMARY KEY,
    mine_id       TEXT NOT NULL REFERENCES mines(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    duration_ms   BIGINT NOT NULL CHECK (duration_ms > 0),
    job_seconds   DOUBLE PRECISION NOT NULL CHECK (job_seconds > 0),
    seed          BIGINT NOT NULL,
    priority_mix  JSONB NOT NULL,
    bursts        JSONB NOT NULL DEFAULT '[]'::jsonb,
    description   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX scenarios_mine_idx ON scenarios (mine_id);

-- Runs are replays. A scenario is optional because a live run has nothing to
-- replay: it watches real infrastructure.
--
-- ON DELETE SET NULL rather than CASCADE: a completed run's results stay
-- meaningful after its scenario is tidied away, and losing them would destroy
-- the only record of what the autoscaler actually did.
CREATE TABLE runs (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL DEFAULT '',
    scenario_id           TEXT REFERENCES scenarios(id) ON DELETE SET NULL,
    target_id             TEXT NOT NULL,
    mode                  TEXT NOT NULL CHECK (mode IN ('simulation', 'live')),
    status                TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed','cancelled')),
    simulated_start       TIMESTAMPTZ,
    time_compression      DOUBLE PRECISION,
    decision_interval_ms  BIGINT NOT NULL CHECK (decision_interval_ms > 0),
    settings              JSONB,
    started_at            TIMESTAMPTZ,
    finished_at           TIMESTAMPTZ,
    error                 TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX runs_status_idx ON runs (status);
CREATE INDEX runs_created_idx ON runs (created_at DESC);

-- One row per decision. This is the largest table by far — a six-hour run at
-- fifteen seconds is 1440 rows, and a sweep is hundreds of runs — so it is
-- kept narrow, with the per-priority queue shape in one JSONB column rather
-- than a row per level.
CREATE TABLE run_cycles (
    run_id            TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    sequence          INTEGER NOT NULL,
    at                TIMESTAMPTZ NOT NULL,
    queues            JSONB NOT NULL DEFAULT '{}'::jsonb,
    local_ready       INTEGER NOT NULL DEFAULT 0,
    cloud_ready       INTEGER NOT NULL DEFAULT 0,
    local_pending     INTEGER NOT NULL DEFAULT 0,
    cloud_pending     INTEGER NOT NULL DEFAULT 0,
    action            TEXT NOT NULL DEFAULT '',
    plan_local        INTEGER NOT NULL DEFAULT 0,
    plan_cloud        INTEGER NOT NULL DEFAULT 0,
    reason            TEXT NOT NULL DEFAULT '',
    constraint_name   TEXT NOT NULL DEFAULT '',
    settings_version  BIGINT NOT NULL DEFAULT 0,
    breach_expected   BOOLEAN NOT NULL DEFAULT false,
    completed         INTEGER NOT NULL DEFAULT 0,
    breached          INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, sequence)
);

-- One row per run: what it amounted to.
CREATE TABLE run_metrics (
    run_id                 TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    jobs_submitted         INTEGER NOT NULL DEFAULT 0,
    jobs_completed         INTEGER NOT NULL DEFAULT 0,
    sla_breaches           INTEGER NOT NULL DEFAULT 0,
    breach_rate            DOUBLE PRECISION NOT NULL DEFAULT 0,
    mean_wait_seconds      DOUBLE PRECISION NOT NULL DEFAULT 0,
    p95_wait_seconds       DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_wait_seconds       DOUBLE PRECISION NOT NULL DEFAULT 0,
    peak_queue_depth       INTEGER NOT NULL DEFAULT 0,
    local_executor_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    cloud_executor_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    peak_local_executors   INTEGER NOT NULL DEFAULT 0,
    peak_cloud_executors   INTEGER NOT NULL DEFAULT 0,
    scaling_actions        INTEGER NOT NULL DEFAULT 0,
    cycles                 INTEGER NOT NULL DEFAULT 0
);

-- The virtual mine: where sensors are, where events happen, and when the mine
-- had a location for each one.
--
-- Every column added to an existing table is nullable or defaulted, so rows
-- recorded before this migration stay valid and say honestly that they predate
-- it rather than pretending to a value.

-- A mine may state its own sensor array. NULL means it did not, and one is
-- derived from its sensor count and id whenever a workload is built.
ALTER TABLE mines ADD COLUMN layout JSONB;

-- Pick jitter is a standard deviation of a few milliseconds, often less than
-- one, so it keeps its fraction rather than rounding to the nearest ms.
ALTER TABLE scenarios
    ADD COLUMN pick_jitter_ms DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (pick_jitter_ms >= 0);

-- The waiting work counted by the priority each job was submitted at. NULL for
-- a cycle recorded before this was tracked, which is a different statement from
-- '{}', an empty queue.
ALTER TABLE run_cycles ADD COLUMN submitted_depths JSONB;

-- The sensor array a simulation run was replayed against. Recorded with the
-- run rather than read back from the mine, which can be edited afterwards.
ALTER TABLE runs ADD COLUMN layout JSONB;

-- One row per detected event. Written once, unlocated, as a run starts, and
-- rewritten as the mine locates and finishes processing each one.
--
-- Times are milliseconds from the start of the scenario, which is finer than
-- anything a view of a run can show. truth is ground truth, known to the
-- simulator only; located and final are what the mine solved from picks.
CREATE TABLE run_seismic_events (
    run_id           TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    sequence         INTEGER NOT NULL CHECK (sequence > 0),
    origin_ms        BIGINT NOT NULL CHECK (origin_ms >= 0),
    burst            INTEGER CHECK (burst >= 0),
    truth            JSONB NOT NULL,
    sensors          TEXT[] NOT NULL,
    located_at_ms    BIGINT,
    located          JSONB,
    processed_at_ms  BIGINT,
    final            JSONB,
    PRIMARY KEY (run_id, sequence)
);

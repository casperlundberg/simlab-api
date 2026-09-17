-- How the mine reorders its queued work. runs.intent is what a run was created
-- with: its settings and any changes planned for later cycles. NULL for a run
-- created before intent existed, which reordered nothing.
ALTER TABLE runs ADD COLUMN intent JSONB;

-- Intent as it was from one cycle of a run onwards: the initial settings, and
-- every change after, planned or made by an operator while the run was in
-- flight. Replaying these is part of reproducing the run.
CREATE TABLE run_intent_changes (
    run_id      TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    version     INTEGER NOT NULL,
    cycle       INTEGER NOT NULL,
    source      TEXT NOT NULL,
    settings    JSONB NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (run_id, version)
);

-- Per cycle: what intent had done to the queue, and whether the autoscaler
-- predicted only breaches it was accepting. Per event: every change of intent
-- about its work. NULL when recorded before intent was.
ALTER TABLE run_cycles
    ADD COLUMN intent JSONB,
    ADD COLUMN breaches_exempt_only BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE run_seismic_events ADD COLUMN intent JSONB;

-- Breaches against the SLA each job was submitted under, and how many jobs
-- intent moved. NULL for a run measured before these were.
ALTER TABLE run_metrics
    ADD COLUMN sla_breaches_as_submitted INTEGER,
    ADD COLUMN jobs_reprioritised INTEGER;

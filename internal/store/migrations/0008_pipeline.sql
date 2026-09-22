-- The mine's own three-stage workflow, when a scenario runs one: when it
-- sweeps, and what each stage is worth and costs. NULL for a scenario without
-- one, which generates picks alone, as every scenario before it did.
ALTER TABLE scenarios ADD COLUMN pipeline JSONB;

-- The work that workflow submitted: associate sweeps and the locates they
-- emitted. NULL for a run measured before these were, and read as none, which
-- is what a run without a pipeline submits.
ALTER TABLE run_metrics
    ADD COLUMN sweeps INTEGER,
    ADD COLUMN locates INTEGER;

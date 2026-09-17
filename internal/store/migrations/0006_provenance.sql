-- What a run was produced by and from: both services' builds, and the mine,
-- scenario and effective settings as they were when it began. NULL for a run
-- recorded before this was, which cannot be traced to its code.
ALTER TABLE runs ADD COLUMN provenance JSONB;

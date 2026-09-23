-- What produced an event: work around a face, the sequence after a blast,
-- background, or an encounter scripted where a unit was about to be. Empty for
-- an event of a scenario that says nothing of how the mine is worked, which is
-- every event recorded before it.
ALTER TABLE run_seismic_events ADD COLUMN activity TEXT NOT NULL DEFAULT '';

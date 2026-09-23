-- The unit an encounter was scripted for: the one the event was placed ahead
-- of, at the notice the scenario asked for. Empty for every event a scenario
-- did not script, which is every event recorded before them.
ALTER TABLE run_seismic_events ADD COLUMN scripted_for TEXT NOT NULL DEFAULT '';

-- When each of an event's picks was processed, in the order of its sensors.
-- NULL for an event recorded before this was tracked, and a NULL element for a
-- pick still waiting. It is what says which sensors have work outstanding at a
-- moment; the event-level times cannot, since an event's picks finish one by
-- one.
ALTER TABLE run_seismic_events ADD COLUMN picks_processed_at_ms BIGINT[];

-- How large each event really was, and who it really exposed. Ground truth,
-- stored beside the mine's estimates (inside located and final) so the two can
-- be compared. NULL for events recorded before magnitudes were.
ALTER TABLE run_seismic_events
    ADD COLUMN magnitude DOUBLE PRECISION,
    ADD COLUMN exposed JSONB;

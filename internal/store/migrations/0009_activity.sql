-- How a scenario's mine is worked: faces, blasting and where its seismicity
-- comes from. NULL for a scenario that does not say, whose background is
-- spread along the tunnels around the clock, as every scenario before it.
ALTER TABLE scenarios ADD COLUMN activity JSONB;

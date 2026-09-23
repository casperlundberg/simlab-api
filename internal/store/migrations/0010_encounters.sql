-- Encounters scripted into a scenario: events placed where a unit is about to
-- be, at a stated notice. NULL for a scenario that scripts none, which is
-- every scenario before them.
ALTER TABLE scenarios ADD COLUMN encounters JSONB;

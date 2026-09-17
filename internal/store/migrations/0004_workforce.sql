-- Who is underground. A scenario's workforce is NULL when it did not state
-- one, and takes the default; a stated one of zeroes means nobody.
ALTER TABLE scenarios ADD COLUMN workforce JSONB;

-- The people and vehicles a run's mine was replayed with, and where they
-- went. Recorded with the run, like its seismic events, rather than
-- regenerated from a scenario that may have been edited since.
CREATE TABLE run_entities (
    run_id  TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    id      TEXT NOT NULL,
    kind    TEXT NOT NULL,
    track   JSONB NOT NULL,
    PRIMARY KEY (run_id, id)
);

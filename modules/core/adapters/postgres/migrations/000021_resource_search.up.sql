BEGIN;

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- One candidate index avoids six separate bitmap scans for the three fields.
-- The adapter rechecks individual fields so a phrase spanning field boundaries
-- does not become a match just because the indexed text is concatenated.
CREATE INDEX idx_resources_search_text ON core.resources USING gin
    (lower(title || E'\n' || annotation || E'\n' || content) gin_trgm_ops)
    WHERE deleted_at IS NULL AND is_public AND is_searchable;

-- The partitioned parent propagates the index to every leaf partition.
CREATE INDEX idx_library_items_search_text ON core.library_items USING gin
    (lower(title || E'\n' || annotation || E'\n' || content) gin_trgm_ops)
    WHERE deleted_at IS NULL AND is_public AND is_searchable;

COMMIT;

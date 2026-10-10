CREATE TABLE core.media_field_occurrences (
    owner_kind TEXT NOT NULL,
    owner_id BIGINT NOT NULL,
    site_id BIGINT NOT NULL REFERENCES core.sites(id) ON DELETE CASCADE,
    container TEXT NOT NULL,
    value_path TEXT[] NOT NULL,
    media_id BIGINT NOT NULL REFERENCES core.media(id) ON DELETE RESTRICT,
    reference_target TEXT NOT NULL CHECK (reference_target IN ('file','media')),
    PRIMARY KEY(owner_kind,owner_id,container,value_path)
);
CREATE INDEX idx_media_field_occurrences_media ON core.media_field_occurrences(media_id);

CREATE TABLE core.media_file_deletions (
    operation_id TEXT PRIMARY KEY,
    site_id BIGINT NOT NULL,
    media_id BIGINT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','completed')),
    result JSONB NOT NULL,
    files JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    completed_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT
);
CREATE INDEX idx_media_file_deletions_pending ON core.media_file_deletions(attempts,created_at) WHERE status='pending';

ALTER TABLE core.resource_media_references ADD COLUMN reference_target TEXT NOT NULL DEFAULT 'media' CHECK(reference_target IN ('file','media'));

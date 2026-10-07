ALTER TABLE core.resources
    ADD COLUMN source_library_id BIGINT GENERATED ALWAYS AS
        (CASE WHEN type='library_mirror' THEN (type_settings->>'source_library_id')::bigint END) STORED,
    ADD CONSTRAINT ck_library_mirror_source CHECK
        (type<>'library_mirror' OR source_library_id IS NOT NULL AND source_library_id>0 AND source_library_id<>id),
    ADD CONSTRAINT fk_library_mirror_source FOREIGN KEY (source_library_id)
        REFERENCES core.resources(id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX uq_library_mirror_site_source ON core.resources(site_id, source_library_id)
    WHERE source_library_id IS NOT NULL;
CREATE INDEX idx_library_mirror_source ON core.resources(source_library_id)
    WHERE source_library_id IS NOT NULL;

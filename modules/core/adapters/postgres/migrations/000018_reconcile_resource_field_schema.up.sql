BEGIN;

-- Migration 000015 was changed while development databases already recorded it
-- as applied. Reconcile those databases with the current schema definition while
-- remaining a no-op for databases created from the current migration set.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE core.resource_field_values
    ADD COLUMN IF NOT EXISTS library_id BIGINT NULL;

ALTER TABLE core.resource_field_values
    DROP CONSTRAINT IF EXISTS fk_resource_field_values_library_site,
    ADD CONSTRAINT fk_resource_field_values_library_site
        FOREIGN KEY (library_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_resource_field_values_library_string
    ON core.resource_field_values (site_id, library_id, field_key, value_string, resource_id)
    WHERE library_id IS NOT NULL AND value_kind = 'string' AND position = 0 AND NOT is_multi;
CREATE INDEX IF NOT EXISTS idx_resource_field_values_library_integer
    ON core.resource_field_values (site_id, library_id, field_key, value_integer, resource_id)
    WHERE library_id IS NOT NULL AND value_kind = 'integer' AND position = 0 AND NOT is_multi;
CREATE INDEX IF NOT EXISTS idx_resource_field_values_library_float
    ON core.resource_field_values (site_id, library_id, field_key, value_float, resource_id)
    WHERE library_id IS NOT NULL AND value_kind = 'float' AND position = 0 AND NOT is_multi;
CREATE INDEX IF NOT EXISTS idx_resource_field_values_library_timestamp
    ON core.resource_field_values (site_id, library_id, field_key, value_timestamp, resource_id)
    WHERE library_id IS NOT NULL AND value_kind = 'timestamp' AND position = 0 AND NOT is_multi;

CREATE TABLE IF NOT EXISTS core.library_item_template_usage
(
    site_id    BIGINT NOT NULL,
    library_id BIGINT NOT NULL,
    template   TEXT   NOT NULL CHECK (template = btrim(template) AND template <> ''),
    PRIMARY KEY (site_id, library_id, template),
    CONSTRAINT fk_library_item_template_usage_library_site
        FOREIGN KEY (library_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON DELETE CASCADE
);

COMMIT;

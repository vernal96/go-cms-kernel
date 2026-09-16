CREATE SCHEMA IF NOT EXISTS seo;

CREATE TABLE seo.resource_metadata
(
    resource_id            BIGINT      PRIMARY KEY,
    site_id                BIGINT      NOT NULL,
    title_template         TEXT        NOT NULL,
    description_template   TEXT        NOT NULL,
    keywords_template      TEXT        NOT NULL DEFAULT '',
    canonical_template     TEXT        NOT NULL DEFAULT '',
    robots_index           BOOLEAN     NOT NULL DEFAULT TRUE,
    robots_follow          BOOLEAN     NOT NULL DEFAULT TRUE,
    og_title_template      TEXT        NOT NULL DEFAULT '',
    og_description_template TEXT       NOT NULL DEFAULT '',
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by             BIGINT      NULL,
    updated_by             BIGINT      NULL,

    CONSTRAINT fk_seo_resource_metadata_resource_site
        FOREIGN KEY (resource_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON DELETE CASCADE,
    CONSTRAINT fk_seo_resource_metadata_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    CONSTRAINT fk_seo_resource_metadata_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL
);

CREATE INDEX idx_seo_resource_metadata_site
    ON seo.resource_metadata (site_id, resource_id);

CREATE TABLE core.resources
(
    id                 BIGSERIAL PRIMARY KEY,
    site_id            BIGINT      NOT NULL,
    parent_id          BIGINT      NULL,
    type               TEXT        NOT NULL DEFAULT 'page'
        CHECK (btrim(type) <> '' AND type = btrim(type)),
    template           TEXT        NULL
        CHECK (template IS NULL OR (
            btrim(template) <> ''
                AND template = btrim(template)
        )),
    content_type       TEXT        NULL DEFAULT 'html'
        CHECK (content_type IS NULL OR (
            content_type ~ '^[a-z0-9][a-z0-9.+-]*$'
        )),
    title              TEXT        NOT NULL
        CHECK (btrim(title) <> ''),
    menu_title         TEXT        NOT NULL DEFAULT '',
    slug               TEXT        NOT NULL DEFAULT ''
        CHECK (
            (slug = '' AND parent_id IS NULL)
                OR slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
        ),
    path               TEXT        NULL
        CHECK (
            path IS NULL
                OR path = '/'
                OR (
                    left(path, 1) = '/'
                        AND right(path, 1) <> '/'
                        AND position('//' IN path) = 0
                )
        ),
    content            TEXT        NOT NULL DEFAULT '',
    target_resource_id BIGINT      NULL,
    external_url       TEXT        NULL
        CHECK (external_url IS NULL OR btrim(external_url) <> ''),
    is_public          BOOLEAN     NOT NULL DEFAULT TRUE,
    is_searchable      BOOLEAN     NOT NULL DEFAULT TRUE,
    in_menu            BOOLEAN     NOT NULL DEFAULT TRUE,
    in_sitemap         BOOLEAN     NOT NULL DEFAULT TRUE,
    sort               INTEGER     NOT NULL DEFAULT 0,
    published_at       TIMESTAMPTZ NULL,
    unpublished_at     TIMESTAMPTZ NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_resources_id_site UNIQUE (id, site_id),
    CONSTRAINT fk_resources_site
        FOREIGN KEY (site_id)
            REFERENCES core.sites (id)
            ON DELETE CASCADE,
    CONSTRAINT fk_resources_parent_site
        FOREIGN KEY (parent_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON DELETE CASCADE,
    CONSTRAINT fk_resources_target_site
        FOREIGN KEY (target_resource_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON DELETE NO ACTION,
    CONSTRAINT ck_resources_publication_window
        CHECK (
            published_at IS NULL
                OR unpublished_at IS NULL
                OR unpublished_at > published_at
        )
);

CREATE UNIQUE INDEX uq_resources_root_slug
    ON core.resources (site_id, slug)
    WHERE parent_id IS NULL;

CREATE UNIQUE INDEX uq_resources_child_slug
    ON core.resources (site_id, parent_id, slug)
    WHERE parent_id IS NOT NULL;

CREATE UNIQUE INDEX uq_resources_site_path
    ON core.resources (site_id, path)
    WHERE path IS NOT NULL;

CREATE INDEX idx_resources_tree
    ON core.resources (site_id, parent_id, sort, id);

CREATE INDEX idx_resources_target
    ON core.resources (site_id, target_resource_id)
    WHERE target_resource_id IS NOT NULL;

CREATE INDEX idx_resources_type_template
    ON core.resources (site_id, type, template);

CREATE INDEX idx_resources_publication
    ON core.resources (site_id, is_public, published_at, unpublished_at);

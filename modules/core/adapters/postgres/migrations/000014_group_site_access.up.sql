CREATE TABLE core.group_site_access
(
    group_id    BIGINT      NOT NULL,
    site_id     BIGINT      NOT NULL,
    can_view    BOOLEAN     NOT NULL DEFAULT FALSE,
    can_edit    BOOLEAN     NOT NULL DEFAULT FALSE,
    can_delete  BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by  BIGINT      NULL,
    updated_by  BIGINT      NULL,

    CONSTRAINT pk_group_site_access
        PRIMARY KEY (group_id, site_id),
    CONSTRAINT ck_group_site_access_view
        CHECK (NOT can_edit OR can_view),
    CONSTRAINT ck_group_site_access_edit
        CHECK (NOT can_delete OR can_edit),
    CONSTRAINT fk_group_site_access_group
        FOREIGN KEY (group_id)
            REFERENCES core.groups (id)
            ON DELETE CASCADE,
    CONSTRAINT fk_group_site_access_site
        FOREIGN KEY (site_id)
            REFERENCES core.sites (id)
            ON DELETE CASCADE,
    CONSTRAINT fk_group_site_access_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    CONSTRAINT fk_group_site_access_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL
);

CREATE INDEX idx_group_site_access_site_capabilities
    ON core.group_site_access (site_id, group_id)
    WHERE can_view OR can_edit OR can_delete;

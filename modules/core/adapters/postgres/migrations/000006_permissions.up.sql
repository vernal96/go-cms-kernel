CREATE TABLE core.group_permissions
(
    group_id       BIGINT      NOT NULL,
    permission_code TEXT       NOT NULL
        CHECK (
            permission_code ~
                '^[a-z][a-z0-9_-]*\.[a-z][a-z0-9_-]*\.(read|create|update|delete)$'
        ),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by     BIGINT      NULL,
    updated_by     BIGINT      NULL,

    CONSTRAINT pk_group_permissions
        PRIMARY KEY (group_id, permission_code),
    CONSTRAINT fk_group_permissions_group
        FOREIGN KEY (group_id)
            REFERENCES core.groups (id)
            ON DELETE CASCADE,
    CONSTRAINT fk_group_permissions_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    CONSTRAINT fk_group_permissions_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL
);

CREATE INDEX idx_group_permissions_code
    ON core.group_permissions (permission_code, group_id);

CREATE TABLE core.guest_permissions
(
    permission_code TEXT        PRIMARY KEY
        CHECK (
            permission_code ~
                '^[a-z][a-z0-9_-]*\.[a-z][a-z0-9_-]*\.(read|create|update|delete)$'
        ),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      BIGINT      NULL,
    updated_by      BIGINT      NULL,

    CONSTRAINT fk_guest_permissions_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    CONSTRAINT fk_guest_permissions_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL
);

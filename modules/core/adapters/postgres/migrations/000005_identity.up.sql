CREATE TABLE core.users
(
    id              BIGSERIAL PRIMARY KEY,
    login           TEXT        NOT NULL
        CHECK (
            login = lower(btrim(login))
                AND login ~ '^[a-z][a-z0-9._-]{2,63}$'
        ),
    email           TEXT        NOT NULL
        CHECK (
            email = lower(btrim(email))
                AND email <> ''
                AND position('@' IN email) > 1
        ),
    password_hash   TEXT        NOT NULL
        CHECK (password_hash = btrim(password_hash) AND password_hash <> ''),
    name            TEXT        NOT NULL CHECK (btrim(name) <> ''),
    last_name       TEXT        NULL
        CHECK (last_name IS NULL OR btrim(last_name) <> ''),
    middle_name     TEXT        NULL
        CHECK (middle_name IS NULL OR btrim(middle_name) <> ''),
    phone           TEXT        NULL
        CHECK (phone IS NULL OR btrim(phone) <> ''),
    avatar_media_id BIGINT      NULL,
    last_login_at   TIMESTAMPTZ NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ NULL,
    created_by      BIGINT      NULL,
    updated_by      BIGINT      NULL,
    deleted_by      BIGINT      NULL,

    CONSTRAINT uq_users_login UNIQUE (login),
    CONSTRAINT uq_users_email UNIQUE (email),
    CONSTRAINT fk_users_avatar_media
        FOREIGN KEY (avatar_media_id)
            REFERENCES core.media (id)
            ON DELETE SET NULL,
    CONSTRAINT fk_users_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    CONSTRAINT fk_users_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    CONSTRAINT fk_users_deleted_by
        FOREIGN KEY (deleted_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL
);

CREATE INDEX idx_users_avatar_media
    ON core.users (avatar_media_id)
    WHERE avatar_media_id IS NOT NULL;

CREATE INDEX idx_users_deleted
    ON core.users (deleted_at, id);

CREATE TABLE core.groups
(
    id         BIGSERIAL PRIMARY KEY,
    code       TEXT        NOT NULL
        CHECK (
            code = lower(btrim(code))
                AND code ~ '^[a-z][a-z0-9_-]{1,63}$'
        ),
    name       TEXT        NOT NULL CHECK (btrim(name) <> ''),
    is_super   BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by BIGINT      NULL,
    updated_by BIGINT      NULL,

    CONSTRAINT uq_groups_code UNIQUE (code),
    CONSTRAINT fk_groups_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    CONSTRAINT fk_groups_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL
);

CREATE TABLE core.user_groups
(
    user_id    BIGINT      NOT NULL,
    group_id   BIGINT      NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by BIGINT      NULL,
    updated_by BIGINT      NULL,

    CONSTRAINT pk_user_groups PRIMARY KEY (user_id, group_id),
    CONSTRAINT fk_user_groups_user
        FOREIGN KEY (user_id)
            REFERENCES core.users (id)
            ON DELETE CASCADE,
    CONSTRAINT fk_user_groups_group
        FOREIGN KEY (group_id)
            REFERENCES core.groups (id)
            ON DELETE CASCADE,
    CONSTRAINT fk_user_groups_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    CONSTRAINT fk_user_groups_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL
);

CREATE INDEX idx_user_groups_group
    ON core.user_groups (group_id, user_id);

ALTER TABLE core.sites
    ADD COLUMN is_public BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN created_by BIGINT NULL,
    ADD COLUMN updated_by BIGINT NULL,
    ADD CONSTRAINT fk_sites_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    ADD CONSTRAINT fk_sites_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL;

UPDATE core.sites
SET is_public = TRUE;

ALTER TABLE core.resources
    ADD COLUMN created_by BIGINT NULL,
    ADD COLUMN updated_by BIGINT NULL,
    ADD CONSTRAINT fk_resources_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    ADD CONSTRAINT fk_resources_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL;

ALTER TABLE core.file_folders
    ADD COLUMN created_by BIGINT NULL,
    ADD COLUMN updated_by BIGINT NULL,
    ADD CONSTRAINT fk_file_folders_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    ADD CONSTRAINT fk_file_folders_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL;

ALTER TABLE core.files
    ADD COLUMN created_by BIGINT NULL,
    ADD COLUMN updated_by BIGINT NULL,
    ADD CONSTRAINT fk_files_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    ADD CONSTRAINT fk_files_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL;

ALTER TABLE core.media
    ADD COLUMN created_by BIGINT NULL,
    ADD COLUMN updated_by BIGINT NULL,
    ADD CONSTRAINT fk_media_created_by
        FOREIGN KEY (created_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL,
    ADD CONSTRAINT fk_media_updated_by
        FOREIGN KEY (updated_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL;

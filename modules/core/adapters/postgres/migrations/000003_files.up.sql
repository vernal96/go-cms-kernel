CREATE TABLE core.file_folders
(
    id         BIGSERIAL PRIMARY KEY,
    parent_id  BIGINT      NULL,
    storage    TEXT        NOT NULL
        CHECK (storage = btrim(storage) AND storage <> ''),
    name       TEXT        NOT NULL
        CHECK (
            name = btrim(name)
                AND name <> ''
                AND name NOT IN ('.', '..')
                AND position('/' IN name) = 0
        ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_file_folders_id_storage UNIQUE (id, storage),
    CONSTRAINT uq_file_folders_namespace
        UNIQUE NULLS NOT DISTINCT (storage, parent_id, name),
    CONSTRAINT fk_file_folders_parent_storage
        FOREIGN KEY (parent_id, storage)
            REFERENCES core.file_folders (id, storage)
            ON DELETE CASCADE
);

CREATE INDEX idx_file_folders_tree
    ON core.file_folders (storage, parent_id, name, id);

CREATE TABLE core.files
(
    id              BIGSERIAL PRIMARY KEY,
    folder_id       BIGINT      NULL,
    storage         TEXT        NOT NULL
        CHECK (storage = btrim(storage) AND storage <> ''),
    name            TEXT        NOT NULL
        CHECK (
            name = btrim(name)
                AND name <> ''
                AND name NOT IN ('.', '..')
                AND position('/' IN name) = 0
        ),
    mime_type       TEXT        NOT NULL
        CHECK (mime_type = btrim(mime_type) AND mime_type <> ''),
    size            BIGINT      NOT NULL CHECK (size >= 0),
    checksum_sha256 BYTEA       NOT NULL
        CHECK (octet_length(checksum_sha256) = 32),
    path            TEXT        NOT NULL
        CHECK (path = btrim(path) AND path <> ''),
    parent_id       BIGINT      NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_files_namespace
        UNIQUE NULLS NOT DISTINCT (storage, folder_id, name),
    CONSTRAINT uq_files_storage_path UNIQUE (storage, path),
    CONSTRAINT fk_files_folder_storage
        FOREIGN KEY (folder_id, storage)
            REFERENCES core.file_folders (id, storage)
            ON DELETE CASCADE,
    CONSTRAINT fk_files_parent
        FOREIGN KEY (parent_id)
            REFERENCES core.files (id)
            ON DELETE CASCADE,
    CONSTRAINT ck_files_not_self_parent
        CHECK (parent_id IS NULL OR parent_id <> id)
);

CREATE INDEX idx_files_folder
    ON core.files (storage, folder_id, name, id);

CREATE INDEX idx_files_parent
    ON core.files (parent_id)
    WHERE parent_id IS NOT NULL;

CREATE INDEX idx_files_checksum
    ON core.files (checksum_sha256);

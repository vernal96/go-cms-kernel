CREATE TABLE core.file_field_references
(
    owner_kind TEXT   NOT NULL
        CHECK (owner_kind IN ('site', 'resource')),
    owner_id   BIGINT NOT NULL,
    field_key  TEXT   NOT NULL
        CHECK (field_key = btrim(field_key) AND field_key <> ''),
    file_id    BIGINT NOT NULL,

    CONSTRAINT pk_file_field_references
        PRIMARY KEY (owner_kind, owner_id, field_key),
    CONSTRAINT fk_file_field_references_file
        FOREIGN KEY (file_id)
            REFERENCES core.files (id)
            ON DELETE RESTRICT
);

CREATE INDEX idx_file_field_references_file
    ON core.file_field_references (file_id);

CREATE TABLE core.media
(
    id         BIGSERIAL PRIMARY KEY,
    file_id    BIGINT      NOT NULL,
    title      TEXT        NULL
        CHECK (title IS NULL OR btrim(title) <> ''),
    params     JSONB       NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(params) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT fk_media_file
        FOREIGN KEY (file_id)
            REFERENCES core.files (id)
            ON DELETE CASCADE
);

CREATE INDEX idx_media_file
    ON core.media (file_id);

ALTER TABLE core.resources
    ADD COLUMN image_media_id BIGINT NULL,
    ADD CONSTRAINT fk_resources_image_media
        FOREIGN KEY (image_media_id)
            REFERENCES core.media (id)
            ON DELETE SET NULL;

CREATE INDEX idx_resources_image_media
    ON core.resources (image_media_id)
    WHERE image_media_id IS NOT NULL;

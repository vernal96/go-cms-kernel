DROP INDEX IF EXISTS core.idx_resources_image_media;

ALTER TABLE core.resources
    DROP CONSTRAINT IF EXISTS fk_resources_image_media,
    DROP COLUMN IF EXISTS image_media_id;

DROP TABLE IF EXISTS core.media;

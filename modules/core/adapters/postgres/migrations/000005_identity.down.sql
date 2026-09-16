ALTER TABLE core.media
    DROP COLUMN updated_by,
    DROP COLUMN created_by;

ALTER TABLE core.files
    DROP COLUMN updated_by,
    DROP COLUMN created_by;

ALTER TABLE core.file_folders
    DROP COLUMN updated_by,
    DROP COLUMN created_by;

ALTER TABLE core.resources
    DROP COLUMN updated_by,
    DROP COLUMN created_by;

ALTER TABLE core.sites
    DROP COLUMN updated_by,
    DROP COLUMN created_by,
    DROP COLUMN is_public;

DROP TABLE core.user_groups;
DROP TABLE core.groups;
DROP TABLE core.users;

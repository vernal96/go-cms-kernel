DROP INDEX IF EXISTS core.idx_resources_active_tree;

ALTER TABLE core.resources
    DROP CONSTRAINT IF EXISTS fk_resources_deleted_by,
    DROP COLUMN IF EXISTS deleted_by,
    DROP COLUMN IF EXISTS deleted_at,
    DROP COLUMN IF EXISTS annotation;

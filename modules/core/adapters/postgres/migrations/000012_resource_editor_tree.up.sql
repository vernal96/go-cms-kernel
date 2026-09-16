ALTER TABLE core.resources
    ADD COLUMN annotation TEXT NOT NULL DEFAULT '',
    ADD COLUMN deleted_at TIMESTAMPTZ NULL,
    ADD COLUMN deleted_by BIGINT NULL;

ALTER TABLE core.resources
    ADD CONSTRAINT fk_resources_deleted_by
        FOREIGN KEY (deleted_by)
            REFERENCES core.users (id)
            ON DELETE SET NULL;

WITH ordered AS (
    SELECT
        id,
        row_number() OVER (
            PARTITION BY site_id, parent_id
            ORDER BY sort, id
        ) - 1 AS new_sort
    FROM core.resources
)
UPDATE core.resources AS target
SET sort = ordered.new_sort
FROM ordered
WHERE target.id = ordered.id
  AND target.sort <> ordered.new_sort;

CREATE INDEX idx_resources_active_tree
    ON core.resources (site_id, deleted_at, parent_id, sort, id);

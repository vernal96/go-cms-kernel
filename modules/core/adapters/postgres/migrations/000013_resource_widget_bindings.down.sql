ALTER TABLE core.resource_widgets
    DROP CONSTRAINT uq_resource_widgets_area_position;

WITH ordered AS
(
    SELECT id,
           row_number() OVER (
               PARTITION BY resource_id
               ORDER BY area, position, id
           )::integer - 1 AS new_position
    FROM core.resource_widgets
)
UPDATE core.resource_widgets AS widgets
SET position = ordered.new_position
FROM ordered
WHERE widgets.id = ordered.id;

ALTER TABLE core.resource_widgets
    DROP CONSTRAINT resource_widgets_pkey,
    DROP COLUMN enabled,
    DROP COLUMN margin_bottom,
    DROP COLUMN margin_top,
    DROP COLUMN columns,
    DROP COLUMN view,
    DROP COLUMN area,
    DROP COLUMN id,
    ADD CONSTRAINT resource_widgets_pkey PRIMARY KEY (resource_id, position);

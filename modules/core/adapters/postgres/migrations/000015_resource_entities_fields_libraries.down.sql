BEGIN;

DELETE FROM core.resource_entities WHERE storage_kind = 'library_item';

DROP TABLE core.library_item_template_usage;
DROP TABLE core.library_items;
DROP TABLE core.library_item_routes;
DROP TABLE core.resource_field_values;

ALTER TABLE core.resource_widgets
    DROP CONSTRAINT fk_resource_widgets_resource,
    ADD CONSTRAINT fk_resource_widgets_resource
        FOREIGN KEY (resource_id) REFERENCES core.resources (id) ON DELETE CASCADE;

ALTER TABLE core.resources
    DROP CONSTRAINT fk_resources_target_site,
    ADD CONSTRAINT fk_resources_target_site
        FOREIGN KEY (target_resource_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON DELETE NO ACTION,
    DROP CONSTRAINT fk_resources_entity_site,
    DROP COLUMN type_settings,
    ALTER COLUMN id SET DEFAULT nextval('core.resources_id_seq');

SELECT setval(
    'core.resources_id_seq',
    greatest(coalesce((SELECT max(id) FROM core.resources), 1), 1),
    EXISTS (SELECT 1 FROM core.resources)
);

DROP TABLE core.resource_entities;

COMMIT;

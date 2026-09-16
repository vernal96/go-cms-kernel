ALTER TABLE core.resources
    DROP CONSTRAINT fk_resources_entity_site,
    ADD CONSTRAINT fk_resources_entity_site
        FOREIGN KEY (id, site_id)
            REFERENCES core.resource_entities (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE;

ALTER TABLE core.resources
    DROP CONSTRAINT fk_resources_parent_site,
    ADD CONSTRAINT fk_resources_parent_site
        FOREIGN KEY (parent_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE,
    DROP CONSTRAINT fk_resources_target_site,
    ADD CONSTRAINT fk_resources_target_site
        FOREIGN KEY (target_resource_id, site_id)
            REFERENCES core.resource_entities (id, site_id)
            ON UPDATE CASCADE
            ON DELETE NO ACTION;

ALTER TABLE core.resource_field_values
    DROP CONSTRAINT fk_resource_field_values_entity_site,
    ADD CONSTRAINT fk_resource_field_values_entity_site
        FOREIGN KEY (resource_id, site_id)
            REFERENCES core.resource_entities (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE;

ALTER TABLE core.resource_field_values
    DROP CONSTRAINT fk_resource_field_values_library_site,
    ADD CONSTRAINT fk_resource_field_values_library_site
        FOREIGN KEY (library_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE;

ALTER TABLE core.library_item_routes
    DROP CONSTRAINT fk_library_item_routes_entity_site,
    ADD CONSTRAINT fk_library_item_routes_entity_site
        FOREIGN KEY (resource_id, site_id)
            REFERENCES core.resource_entities (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE;

ALTER TABLE core.library_item_routes
    DROP CONSTRAINT fk_library_item_routes_library_site,
    ADD CONSTRAINT fk_library_item_routes_library_site
        FOREIGN KEY (library_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE;

ALTER TABLE core.library_items
    DROP CONSTRAINT fk_library_items_entity_site,
    ADD CONSTRAINT fk_library_items_entity_site
        FOREIGN KEY (id, site_id)
            REFERENCES core.resource_entities (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE;

ALTER TABLE core.library_items
    DROP CONSTRAINT fk_library_items_library_site,
    ADD CONSTRAINT fk_library_items_library_site
        FOREIGN KEY (library_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE;

ALTER TABLE core.library_item_template_usage
    DROP CONSTRAINT fk_library_item_template_usage_library_site,
    ADD CONSTRAINT fk_library_item_template_usage_library_site
        FOREIGN KEY (library_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE;

ALTER TABLE core.resource_revisions
    DROP CONSTRAINT fk_resource_revisions_entity_site,
    ADD CONSTRAINT fk_resource_revisions_entity_site
        FOREIGN KEY (resource_id, site_id)
            REFERENCES core.resource_entities (id, site_id)
            ON UPDATE CASCADE
            ON DELETE CASCADE;

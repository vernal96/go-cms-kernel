ALTER TABLE seo.resource_metadata
    DROP CONSTRAINT fk_seo_resource_metadata_resource_site,
    ADD CONSTRAINT fk_seo_resource_metadata_resource_site
        FOREIGN KEY (resource_id, site_id)
            REFERENCES core.resources (id, site_id)
            ON DELETE CASCADE;


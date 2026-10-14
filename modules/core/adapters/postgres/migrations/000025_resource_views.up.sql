CREATE SCHEMA resource_views;
CREATE TABLE resource_views.counts (
    site_id BIGINT NOT NULL,
    resource_id BIGINT NOT NULL,
    total BIGINT NOT NULL DEFAULT 0 CHECK (total >= 0),
    PRIMARY KEY (site_id, resource_id),
    FOREIGN KEY (resource_id,site_id) REFERENCES core.resource_entities(id,site_id) ON DELETE CASCADE ON UPDATE CASCADE,
    FOREIGN KEY (site_id) REFERENCES core.sites(id) ON DELETE CASCADE
);
-- Keep committed batch identities: retries may arrive after a lease expires,
-- including a worker resumed after an arbitrarily long process pause.
CREATE TABLE resource_views.batches (
    batch_id TEXT PRIMARY KEY,
    site_id BIGINT NOT NULL
);

CREATE TABLE core.sites
(
    id           BIGSERIAL PRIMARY KEY,
    profile_code TEXT        NOT NULL CHECK (btrim(profile_code) <> ''),
    domain       TEXT        NOT NULL CHECK (
        btrim(domain) <> ''
            AND domain = btrim(domain)
        ),
    locale       TEXT        NOT NULL DEFAULT 'ru-RU' CHECK (btrim(locale) <> ''),
    settings     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_sites_normalized_domain
    ON core.sites ((lower(rtrim(domain, '.'))));

CREATE INDEX idx_sites_profile_code
    ON core.sites (profile_code);

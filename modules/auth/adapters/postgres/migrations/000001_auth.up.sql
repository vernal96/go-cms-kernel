CREATE TABLE auth.flows (
    state_hash TEXT PRIMARY KEY CHECK (length(state_hash) = 64),
    browser_hash TEXT NOT NULL CHECK (length(browser_hash) = 64),
    site_id BIGINT NOT NULL,
    provider TEXT NOT NULL,
    namespace TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    nonce TEXT NOT NULL,
    code_verifier TEXT NOT NULL,
    link_user_id BIGINT NULL,
    link_session_version BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX flows_site_expiry ON auth.flows(site_id, expires_at);

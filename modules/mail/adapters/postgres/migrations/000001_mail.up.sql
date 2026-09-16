CREATE SCHEMA IF NOT EXISTS mail;

CREATE TABLE mail.templates
(
    id              BIGSERIAL PRIMARY KEY,
    site_id         BIGINT      NOT NULL REFERENCES core.sites (id) ON DELETE CASCADE,
    code            TEXT        NOT NULL CHECK (code = btrim(code) AND code <> ''),
    name            TEXT        NOT NULL CHECK (btrim(name) <> ''),
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    from_address    JSONB       NOT NULL,
    to_addresses    JSONB       NOT NULL DEFAULT '[]'::jsonb,
    cc_addresses    JSONB       NOT NULL DEFAULT '[]'::jsonb,
    bcc_addresses   JSONB       NOT NULL DEFAULT '[]'::jsonb,
    reply_to        JSONB       NULL,
    subject         TEXT        NOT NULL DEFAULT '',
    content_type    TEXT        NOT NULL CHECK (content_type IN ('text', 'html')),
    text_body       TEXT        NOT NULL DEFAULT '',
    html_body       TEXT        NOT NULL DEFAULT '',
    attachments     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    variables       JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      BIGINT      NULL REFERENCES core.users (id) ON DELETE SET NULL,
    updated_by      BIGINT      NULL REFERENCES core.users (id) ON DELETE SET NULL,
    UNIQUE (site_id, code)
);

CREATE INDEX idx_mail_templates_site_name ON mail.templates (site_id, name, id);

CREATE TABLE mail.messages
(
    id                BIGSERIAL PRIMARY KEY,
    site_id           BIGINT      NOT NULL REFERENCES core.sites (id) ON DELETE CASCADE,
    template_id       BIGINT      NULL REFERENCES mail.templates (id) ON DELETE SET NULL,
    template_code     TEXT        NOT NULL DEFAULT '',
    template_name     TEXT        NOT NULL DEFAULT '',
    rfc_message_id    TEXT        NOT NULL UNIQUE,
    from_address      JSONB       NOT NULL,
    to_addresses      JSONB       NOT NULL DEFAULT '[]'::jsonb,
    cc_addresses      JSONB       NOT NULL DEFAULT '[]'::jsonb,
    bcc_addresses     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    reply_to          JSONB       NULL,
    subject           TEXT        NOT NULL DEFAULT '',
    content_type      TEXT        NOT NULL CHECK (content_type IN ('text', 'html')),
    text_body         TEXT        NOT NULL DEFAULT '',
    html_body         TEXT        NOT NULL DEFAULT '',
    attachments       JSONB       NOT NULL DEFAULT '[]'::jsonb,
    status            TEXT        NOT NULL CHECK (status IN ('queued', 'sending', 'accepted', 'failed')),
    origin            TEXT        NOT NULL CHECK (origin IN ('manual', 'automatic')),
    origin_source     TEXT        NOT NULL DEFAULT '',
    requested_at      TIMESTAMPTZ NOT NULL,
    requested_by      BIGINT      NULL REFERENCES core.users (id) ON DELETE SET NULL,
    requested_by_name TEXT        NOT NULL DEFAULT '',
    accepted_at       TIMESTAMPTZ NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_mail_messages_site_created ON mail.messages (site_id, created_at DESC, id DESC);
CREATE INDEX idx_mail_messages_site_status_created ON mail.messages (site_id, status, created_at, id);
CREATE INDEX idx_mail_messages_retention ON mail.messages (updated_at, id) WHERE status IN ('accepted', 'failed');

CREATE TABLE mail.delivery_attempts
(
    id                BIGSERIAL PRIMARY KEY,
    message_id        BIGINT      NOT NULL REFERENCES mail.messages (id) ON DELETE CASCADE,
    attempt_number    INTEGER     NOT NULL CHECK (attempt_number > 0),
    driver            TEXT        NOT NULL DEFAULT '',
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at       TIMESTAMPTZ NULL,
    status            TEXT        NOT NULL CHECK (status IN ('sending', 'accepted', 'failed')),
    remote_message_id TEXT        NOT NULL DEFAULT '',
    response_code     TEXT        NOT NULL DEFAULT '',
    safe_error        TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (message_id, attempt_number)
);

CREATE INDEX idx_mail_attempts_message_order ON mail.delivery_attempts (message_id, attempt_number DESC);

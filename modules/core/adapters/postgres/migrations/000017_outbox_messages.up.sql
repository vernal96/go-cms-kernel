CREATE TABLE core.outbox_messages
(
    message_id text PRIMARY KEY,
    topic text NOT NULL CHECK (btrim(topic) <> ''),
    message_key bytea NOT NULL DEFAULT ''::bytea,
    body bytea NOT NULL,
    headers jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(headers) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    attempt_count bigint NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_error text,
    lease_owner text,
    lease_until timestamptz,
    published_at timestamptz,
    CHECK ((lease_owner IS NULL) = (lease_until IS NULL))
);

CREATE INDEX outbox_messages_available_idx
    ON core.outbox_messages (available_at, created_at, message_id)
    WHERE published_at IS NULL;

CREATE INDEX outbox_messages_published_idx
    ON core.outbox_messages (published_at, message_id)
    WHERE published_at IS NOT NULL;

ALTER TABLE mail.messages DROP CONSTRAINT messages_status_check;
ALTER TABLE mail.messages ADD CONSTRAINT messages_status_check
    CHECK (status IN ('queued', 'sending', 'retryable', 'accepted', 'failed'));

ALTER TABLE mail.messages
    ADD COLUMN origin_event TEXT NOT NULL DEFAULT '',
    ADD COLUMN origin_reference TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_mail_messages_site_retryable
    ON mail.messages (site_id, updated_at, id)
    WHERE status IN ('queued', 'sending', 'retryable');

CREATE INDEX idx_mail_messages_site_requested
    ON mail.messages (site_id, requested_at DESC, id DESC);

CREATE INDEX idx_mail_messages_site_status_requested
    ON mail.messages (site_id, status, requested_at DESC, id DESC);

CREATE INDEX idx_mail_messages_site_template_requested
    ON mail.messages (site_id, template_code, requested_at DESC, id DESC);

DROP INDEX IF EXISTS mail.idx_mail_messages_site_retryable;
DROP INDEX IF EXISTS mail.idx_mail_messages_site_requested;
DROP INDEX IF EXISTS mail.idx_mail_messages_site_status_requested;
DROP INDEX IF EXISTS mail.idx_mail_messages_site_template_requested;

ALTER TABLE mail.messages DROP COLUMN origin_event, DROP COLUMN origin_reference;

UPDATE mail.messages SET status = 'failed' WHERE status = 'retryable';
ALTER TABLE mail.messages DROP CONSTRAINT messages_status_check;
ALTER TABLE mail.messages ADD CONSTRAINT messages_status_check
    CHECK (status IN ('queued', 'sending', 'accepted', 'failed'));

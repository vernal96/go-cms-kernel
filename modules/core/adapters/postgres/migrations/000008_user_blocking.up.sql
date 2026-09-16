ALTER TABLE core.users
    RENAME COLUMN deleted_at TO blocked_at;

ALTER TABLE core.users
    RENAME COLUMN deleted_by TO blocked_by;

ALTER TABLE core.users
    RENAME CONSTRAINT fk_users_deleted_by TO fk_users_blocked_by;

ALTER INDEX core.idx_users_deleted RENAME TO idx_users_blocked;

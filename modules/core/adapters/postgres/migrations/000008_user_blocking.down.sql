ALTER INDEX core.idx_users_blocked RENAME TO idx_users_deleted;

ALTER TABLE core.users
    RENAME CONSTRAINT fk_users_blocked_by TO fk_users_deleted_by;

ALTER TABLE core.users
    RENAME COLUMN blocked_by TO deleted_by;

ALTER TABLE core.users
    RENAME COLUMN blocked_at TO deleted_at;

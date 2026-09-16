BEGIN;

-- The reconciled objects are part of the canonical schema created by migration
-- 000015. Removing them here would break a database freshly created from the
-- current migration set, so rolling back the reconciliation marker is a no-op.

COMMIT;

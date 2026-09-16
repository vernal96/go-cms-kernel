INSERT INTO core.groups (code, name, is_super)
VALUES
    ('admin', 'Администратор', true),
    ('manager', 'Менеджер', false)
ON CONFLICT (code) DO UPDATE
SET name = EXCLUDED.name,
    is_super = EXCLUDED.is_super,
    updated_at = CURRENT_TIMESTAMP,
    updated_by = NULL;

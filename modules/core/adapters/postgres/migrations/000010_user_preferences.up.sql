ALTER TABLE core.users
    ADD COLUMN color_scheme TEXT NOT NULL DEFAULT 'system'
        CHECK (color_scheme IN ('light', 'dark', 'system'));

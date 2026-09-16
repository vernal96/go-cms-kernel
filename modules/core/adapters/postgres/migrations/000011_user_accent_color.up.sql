ALTER TABLE core.users
    ADD COLUMN accent_color TEXT NOT NULL DEFAULT 'blue'
        CHECK (accent_color IN ('blue', 'violet', 'indigo', 'emerald', 'amber', 'rose'));

CREATE TABLE core.resource_widgets
(
    resource_id BIGINT  NOT NULL,
    widget_code TEXT    NOT NULL
        CHECK (
            btrim(widget_code) <> ''
                AND widget_code = btrim(widget_code)
        ),
    position    INTEGER NOT NULL
        CHECK (position >= 0),
    params      JSONB   NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(params) = 'object'),
    param_bindings JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(param_bindings) = 'object'),

    PRIMARY KEY (resource_id, position),
    CONSTRAINT fk_resource_widgets_resource
        FOREIGN KEY (resource_id)
            REFERENCES core.resources (id)
            ON DELETE CASCADE
);

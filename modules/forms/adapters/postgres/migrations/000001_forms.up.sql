CREATE SCHEMA IF NOT EXISTS forms;

CREATE TABLE forms.forms
(
    id          BIGSERIAL PRIMARY KEY,
    site_id     BIGINT      NOT NULL REFERENCES core.sites (id) ON DELETE CASCADE,
    code        TEXT        NOT NULL CHECK (code = btrim(code) AND code <> ''),
    name        TEXT        NOT NULL CHECK (name = btrim(name) AND name <> ''),
    description TEXT        NOT NULL DEFAULT '',
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by  BIGINT      NULL REFERENCES core.users (id) ON DELETE SET NULL,
    updated_by  BIGINT      NULL REFERENCES core.users (id) ON DELETE SET NULL,
    UNIQUE (site_id, code),
    UNIQUE (id, site_id)
);

CREATE INDEX idx_forms_site_enabled ON forms.forms (site_id, enabled, id);
CREATE INDEX idx_forms_site_name ON forms.forms (site_id, name, id);

CREATE TABLE forms.fields
(
    id              BIGSERIAL PRIMARY KEY,
    form_id         BIGINT      NOT NULL REFERENCES forms.forms (id) ON DELETE CASCADE,
    code            TEXT        NOT NULL CHECK (code = btrim(code) AND code <> ''),
    type            TEXT        NOT NULL CHECK (type = btrim(type) AND type <> ''),
    label           TEXT        NOT NULL CHECK (label = btrim(label) AND label <> ''),
    required        BOOLEAN     NOT NULL DEFAULT FALSE,
    rules           JSONB       NOT NULL DEFAULT '[]'::jsonb,
    options         JSONB       NULL,
    editor          TEXT        NOT NULL DEFAULT '',
    visible_when    JSONB       NULL,
    result_label    TEXT        NOT NULL DEFAULT '',
    show_on_site    BOOLEAN     NOT NULL DEFAULT FALSE CHECK (NOT show_on_site OR type NOT IN ('forms.captcha', 'forms.upload')),
    show_in_results BOOLEAN     NOT NULL DEFAULT FALSE,
    result_position INTEGER     NOT NULL DEFAULT 0 CHECK (result_position >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (form_id, code),
    UNIQUE (id, form_id)
);

CREATE INDEX idx_forms_fields_result_order ON forms.fields (form_id, result_position, id);

CREATE TABLE forms.elements
(
    id         BIGSERIAL PRIMARY KEY,
    form_id    BIGINT      NOT NULL REFERENCES forms.forms (id) ON DELETE CASCADE,
    code       TEXT        NOT NULL CHECK (code = btrim(code) AND code <> ''),
    type       TEXT        NOT NULL CHECK (type = btrim(type) AND type <> ''),
    config     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (form_id, code),
    UNIQUE (id, form_id)
);

CREATE TABLE forms.layout_nodes
(
    id             BIGSERIAL PRIMARY KEY,
    form_id        BIGINT  NOT NULL REFERENCES forms.forms (id) ON DELETE CASCADE,
    parent_id      BIGINT  NULL,
    kind           TEXT    NOT NULL CHECK (kind IN ('field', 'element', 'container')),
    field_id       BIGINT  NULL,
    element_id     BIGINT  NULL,
    container_type TEXT    NOT NULL DEFAULT '',
    position       INTEGER NOT NULL CHECK (position >= 0),
    config         JSONB   NOT NULL DEFAULT '{}'::jsonb,
    CHECK (
        (kind = 'field' AND field_id IS NOT NULL AND element_id IS NULL AND container_type = '') OR
        (kind = 'element' AND field_id IS NULL AND element_id IS NOT NULL AND container_type = '') OR
        (kind = 'container' AND field_id IS NULL AND element_id IS NULL AND container_type IN ('group', 'slide'))
    ),
	CHECK (parent_id IS NULL OR parent_id <> id),
    UNIQUE (field_id),
	UNIQUE (element_id),
	UNIQUE (id, form_id),
	FOREIGN KEY (parent_id, form_id) REFERENCES forms.layout_nodes (id, form_id) ON DELETE CASCADE,
	FOREIGN KEY (field_id, form_id) REFERENCES forms.fields (id, form_id) ON DELETE CASCADE,
	FOREIGN KEY (element_id, form_id) REFERENCES forms.elements (id, form_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX ux_forms_layout_sibling_position
    ON forms.layout_nodes (form_id, COALESCE(parent_id, 0), position);
CREATE INDEX idx_forms_layout_form_parent_position ON forms.layout_nodes (form_id, parent_id, position, id);

CREATE TABLE forms.statuses
(
    id         BIGSERIAL PRIMARY KEY,
    form_id    BIGINT      NOT NULL REFERENCES forms.forms (id) ON DELETE CASCADE,
    code       TEXT        NOT NULL CHECK (code = btrim(code) AND code <> ''),
    name       TEXT        NOT NULL CHECK (name = btrim(name) AND name <> ''),
    color      TEXT        NOT NULL DEFAULT '',
    position   INTEGER     NOT NULL CHECK (position >= 0),
    is_default BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (form_id, code),
    UNIQUE (id, form_id)
);

CREATE UNIQUE INDEX ux_forms_statuses_one_default ON forms.statuses (form_id) WHERE is_default;
CREATE INDEX idx_forms_statuses_order ON forms.statuses (form_id, position, id);

CREATE TABLE forms.results
(
    id             BIGSERIAL PRIMARY KEY,
    site_id        BIGINT      NOT NULL REFERENCES core.sites (id) ON DELETE CASCADE,
    form_id        BIGINT      NOT NULL,
    form_code      TEXT        NOT NULL,
    form_name      TEXT        NOT NULL,
    status_id      BIGINT      NOT NULL,
    user_id        BIGINT      NULL REFERENCES core.users (id) ON DELETE SET NULL,
    user_agent     TEXT        NOT NULL DEFAULT '',
    client_address TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (id, site_id),
	FOREIGN KEY (form_id, site_id) REFERENCES forms.forms (id, site_id) ON DELETE CASCADE,
	FOREIGN KEY (status_id, form_id) REFERENCES forms.statuses (id, form_id) ON DELETE RESTRICT
);

CREATE INDEX idx_forms_results_form_created ON forms.results (site_id, form_id, created_at DESC, id DESC);
CREATE INDEX idx_forms_results_status_created ON forms.results (site_id, status_id, created_at DESC, id DESC);

CREATE TABLE forms.result_values
(
    id              BIGSERIAL PRIMARY KEY,
    result_id       BIGINT  NOT NULL REFERENCES forms.results (id) ON DELETE CASCADE,
    field_id        BIGINT  NULL REFERENCES forms.fields (id) ON DELETE SET NULL,
    field_code      TEXT    NOT NULL,
    field_label     TEXT    NOT NULL,
    result_label    TEXT    NOT NULL,
    field_type      TEXT    NOT NULL,
    is_multi        BOOLEAN NOT NULL DEFAULT FALSE,
    storage_kind    TEXT    NOT NULL CHECK (storage_kind IN ('string', 'integer', 'float', 'boolean', 'timestamp', 'reference', 'json')),
    position        INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    string_value    TEXT NULL,
    integer_value   BIGINT NULL,
    float_value     DOUBLE PRECISION NULL,
    boolean_value   BOOLEAN NULL,
    timestamp_value TIMESTAMPTZ NULL,
    reference_value BIGINT NULL,
    json_value      JSONB NULL,
    CHECK (
        (storage_kind = 'string' AND string_value IS NOT NULL AND integer_value IS NULL AND float_value IS NULL AND boolean_value IS NULL AND timestamp_value IS NULL AND reference_value IS NULL AND json_value IS NULL) OR
        (storage_kind = 'integer' AND string_value IS NULL AND integer_value IS NOT NULL AND float_value IS NULL AND boolean_value IS NULL AND timestamp_value IS NULL AND reference_value IS NULL AND json_value IS NULL) OR
        (storage_kind = 'float' AND string_value IS NULL AND integer_value IS NULL AND float_value IS NOT NULL AND boolean_value IS NULL AND timestamp_value IS NULL AND reference_value IS NULL AND json_value IS NULL) OR
        (storage_kind = 'boolean' AND string_value IS NULL AND integer_value IS NULL AND float_value IS NULL AND boolean_value IS NOT NULL AND timestamp_value IS NULL AND reference_value IS NULL AND json_value IS NULL) OR
        (storage_kind = 'timestamp' AND string_value IS NULL AND integer_value IS NULL AND float_value IS NULL AND boolean_value IS NULL AND timestamp_value IS NOT NULL AND reference_value IS NULL AND json_value IS NULL) OR
        (storage_kind = 'reference' AND string_value IS NULL AND integer_value IS NULL AND float_value IS NULL AND boolean_value IS NULL AND timestamp_value IS NULL AND reference_value IS NOT NULL AND json_value IS NULL) OR
        (storage_kind = 'json' AND string_value IS NULL AND integer_value IS NULL AND float_value IS NULL AND boolean_value IS NULL AND timestamp_value IS NULL AND reference_value IS NULL AND json_value IS NOT NULL)
    ),
    UNIQUE (result_id, field_code, position)
);

CREATE INDEX idx_forms_result_values_result ON forms.result_values (result_id, id);
CREATE INDEX idx_forms_result_values_result_field ON forms.result_values (result_id, field_code, position);

CREATE TABLE forms.result_uploads
(
    id                BIGSERIAL PRIMARY KEY,
    result_id         BIGINT      NOT NULL REFERENCES forms.results (id) ON DELETE CASCADE,
    field_id          BIGINT      NULL REFERENCES forms.fields (id) ON DELETE SET NULL,
    field_code        TEXT        NOT NULL,
    position          INTEGER     NOT NULL DEFAULT 0 CHECK (position >= 0),
    filename          TEXT        NOT NULL,
    mime_type         TEXT        NOT NULL,
    size              BIGINT      NOT NULL CHECK (size >= 0),
    checksum          TEXT        NOT NULL,
    spool_reference   TEXT        NULL,
    spool_deleted_at  TIMESTAMPTZ NULL,
    UNIQUE (result_id, field_code, position)
);

CREATE INDEX idx_forms_result_uploads_result ON forms.result_uploads (result_id, id);
CREATE INDEX idx_forms_result_uploads_spool ON forms.result_uploads (spool_reference) WHERE spool_reference IS NOT NULL AND spool_deleted_at IS NULL;

CREATE TABLE forms.actions
(
    id          BIGSERIAL PRIMARY KEY,
    form_id     BIGINT      NOT NULL REFERENCES forms.forms (id) ON DELETE CASCADE,
    code        TEXT        NOT NULL CHECK (code = btrim(code) AND code <> ''),
    name        TEXT        NOT NULL CHECK (name = btrim(name) AND name <> ''),
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    trigger     JSONB       NOT NULL,
    action_type TEXT        NOT NULL CHECK (action_type = btrim(action_type) AND action_type <> ''),
    config      JSONB       NOT NULL,
    position    INTEGER     NOT NULL CHECK (position >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (form_id, code)
);

CREATE INDEX idx_forms_actions_trigger ON forms.actions (form_id, ((trigger ->> 'type')), enabled, position, id);

CREATE TABLE forms.action_executions
(
    id                 BIGSERIAL PRIMARY KEY,
    site_id            BIGINT      NOT NULL REFERENCES core.sites (id) ON DELETE CASCADE,
    result_id          BIGINT      NOT NULL,
    action_id          BIGINT      NULL REFERENCES forms.actions (id) ON DELETE SET NULL,
    action_code        TEXT        NOT NULL,
    action_name        TEXT        NOT NULL,
    action_type        TEXT        NOT NULL,
    trigger            JSONB       NOT NULL,
    config             JSONB       NOT NULL,
    status             TEXT        NOT NULL CHECK (status IN ('pending', 'running', 'retryable', 'succeeded', 'failed')),
    attempt_count      INTEGER     NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    safe_error         TEXT        NOT NULL DEFAULT '',
    external_reference TEXT        NOT NULL DEFAULT '',
    started_at         TIMESTAMPTZ NULL,
    finished_at        TIMESTAMPTZ NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
	FOREIGN KEY (result_id, site_id) REFERENCES forms.results (id, site_id) ON DELETE CASCADE
);

CREATE INDEX idx_forms_executions_site_status ON forms.action_executions (site_id, status, updated_at, id);
CREATE INDEX idx_forms_executions_result ON forms.action_executions (result_id, created_at, id);
CREATE INDEX idx_forms_executions_claim ON forms.action_executions (site_id, id, attempt_count)
    WHERE status IN ('pending', 'retryable');

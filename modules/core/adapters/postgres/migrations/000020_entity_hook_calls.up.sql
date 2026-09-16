CREATE TABLE core.entity_hook_calls (
    event_id text NOT NULL,
    event_name text NOT NULL,
    event_body jsonb NOT NULL,
    module text NOT NULL,
    handler text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('application', 'site')),
    scope_id text NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    lease_owner text,
    lease_until timestamptz,
    last_error text,
    completed_at timestamptz,
    PRIMARY KEY (event_id, module, handler, scope, scope_id),
    CHECK ((scope='application' AND scope_id='') OR (scope='site' AND scope_id<>''))
);
CREATE INDEX entity_hook_calls_pending ON core.entity_hook_calls (scope,scope_id,module,handler) WHERE completed_at IS NULL;
CREATE INDEX entity_hook_calls_cleanup ON core.entity_hook_calls (completed_at) WHERE completed_at IS NOT NULL;

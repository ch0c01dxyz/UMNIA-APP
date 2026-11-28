CREATE TABLE IF NOT EXISTS inventory_items (
    workspace_id uuid NOT NULL,
    catalog_item_id uuid NOT NULL,
    unit text NOT NULL,
    PRIMARY KEY (workspace_id, catalog_item_id),
    UNIQUE (workspace_id, catalog_item_id, unit),
    FOREIGN KEY (workspace_id, catalog_item_id) REFERENCES catalog_items (workspace_id, id)
);

CREATE TABLE IF NOT EXISTS inventory_movements (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    catalog_item_id uuid NOT NULL,
    unit text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('receipt', 'issue', 'reversal')),
    delta bigint NOT NULL CHECK (delta <> 0 AND delta >= -9223372036854775807),
    source_reference text NOT NULL CHECK (char_length(btrim(source_reference)) BETWEEN 1 AND 200),
    reason text,
    reverses_id uuid,
    client_request_id uuid NOT NULL,
    recorded_by uuid NOT NULL REFERENCES identity_subjects (id),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((kind = 'receipt' AND delta > 0 AND reverses_id IS NULL AND reason IS NULL)
        OR (kind = 'issue' AND delta < 0 AND reverses_id IS NULL AND reason IS NULL)
        OR (kind = 'reversal' AND reverses_id IS NOT NULL AND reason IS NOT NULL
                              AND char_length(btrim(reason)) BETWEEN 1 AND 500)),
    UNIQUE (workspace_id, client_request_id),
    UNIQUE (workspace_id, catalog_item_id, id),
    UNIQUE (workspace_id, reverses_id),
    FOREIGN KEY (workspace_id, catalog_item_id, unit) REFERENCES inventory_items (workspace_id, catalog_item_id, unit),
    FOREIGN KEY (workspace_id, catalog_item_id, reverses_id) REFERENCES inventory_movements (workspace_id, catalog_item_id, id)
);
CREATE INDEX IF NOT EXISTS inventory_movements_history_idx
    ON inventory_movements (workspace_id, recorded_at DESC, id DESC);

ALTER TABLE inventory_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_items FORCE ROW LEVEL SECURITY;
ALTER TABLE inventory_movements ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_movements FORCE ROW LEVEL SECURITY;

DO $policies$
BEGIN
    DROP POLICY IF EXISTS inventory_items_read ON inventory_items;
    CREATE POLICY inventory_items_read ON inventory_items FOR SELECT USING (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
    DROP POLICY IF EXISTS inventory_items_post ON inventory_items;
    CREATE POLICY inventory_items_post ON inventory_items FOR INSERT WITH CHECK (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
    );
    DROP POLICY IF EXISTS inventory_movements_read ON inventory_movements;
    CREATE POLICY inventory_movements_read ON inventory_movements FOR SELECT USING (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
    DROP POLICY IF EXISTS inventory_movements_post ON inventory_movements;
    CREATE POLICY inventory_movements_post ON inventory_movements FOR INSERT WITH CHECK (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND recorded_by = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
        AND app.has_permission(workspace_id, recorded_by, 'view_workspace')
        AND CASE WHEN kind = 'reversal' THEN EXISTS (
            SELECT 1 FROM workspaces w WHERE w.id = inventory_movements.workspace_id AND w.owner_subject_id = inventory_movements.recorded_by
        ) ELSE app.has_permission(workspace_id, recorded_by, 'manage_business_data') END
    );
END;
$policies$;

GRANT SELECT, INSERT ON inventory_items, inventory_movements TO umnia_business;

-- The same workspace lock guards classification even for a direct INSERT.
CREATE OR REPLACE FUNCTION app.guard_stock_tracking()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $guard$
BEGIN
    PERFORM 1 FROM public.workspaces WHERE id = NEW.workspace_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'workspace cannot be locked' USING ERRCODE = '42501';
    END IF;
    IF (TG_TABLE_NAME = 'inventory_items' AND EXISTS (
          SELECT 1 FROM public.physical_assets WHERE workspace_id = NEW.workspace_id AND catalog_item_id = NEW.catalog_item_id
        )) OR (TG_TABLE_NAME = 'physical_assets' AND EXISTS (
          SELECT 1 FROM public.inventory_items WHERE workspace_id = NEW.workspace_id AND catalog_item_id = NEW.catalog_item_id
        )) THEN
        RAISE EXCEPTION 'item cannot use both stock tracking modes'
            USING ERRCODE = '23514', CONSTRAINT = 'inventory_tracking_exclusive';
    END IF;
    RETURN NEW;
END;
$guard$;
CREATE OR REPLACE TRIGGER inventory_items_tracking BEFORE INSERT ON inventory_items
    FOR EACH ROW EXECUTE FUNCTION app.guard_stock_tracking();
CREATE OR REPLACE TRIGGER physical_assets_tracking BEFORE INSERT ON physical_assets
    FOR EACH ROW EXECUTE FUNCTION app.guard_stock_tracking();

INSERT INTO module_registry (module_id, version, manifest)
VALUES ('inventory.basic', '0.1.0', $manifest$
{
  "schema_version": "0.4",
  "id": "inventory.basic",
  "version": "0.1.0",
  "family": "inventory",
  "edition": "basic",
  "exclusive_group": "inventory-edition",
  "display": {
    "name_id": "Stok Sederhana",
    "description_id": "Catat barang masuk, barang keluar, dan saldo stok"
  },
  "provides": [
    "inventory.stock.read"
  ],
  "requires": [
    {
      "capability": "catalog.item.read"
    }
  ],
  "conflicts": [],
  "data_ownership": {
    "schemas": [
      "inventory"
    ],
    "downgrade_strategy": "retain_read_only"
  },
  "simulation_dataset": {
    "required": true,
    "schema_id": "inventory-basic-simulation-v1",
    "validation_profile": "inventory-basic-rules",
    "refresh_policy": "on_module_change_and_scheduled"
  },
  "security": {
    "risk_class": "medium",
    "needs_approval_for_mutations": false
  },
  "ui": {
    "primary_nav": "Stok"
  },
  "events": [
    "inventory.movement.posted"
  ]
}
$manifest$::jsonb)
ON CONFLICT (module_id, version) DO NOTHING;

DO $grant$
BEGIN
    EXECUTE 'GRANT CREATE ON SCHEMA app TO umnia_grant_exec';
    EXECUTE $function$
CREATE OR REPLACE FUNCTION app.grant_inventory_entitlement(p_workspace_id uuid, p_module_id text, p_version text, p_reason text)
RETURNS TABLE(result text, workspace_id uuid, module_id text, version text, granted_by text, granted_at timestamptz, audit_present boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $body$
DECLARE
    existing public.module_entitlements%ROWTYPE;
    matched_audit boolean;
    now_at timestamptz;
    reason_text text := btrim(p_reason);
    actor text := session_user;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE schema_migrations.version = '015_inventory_basic')
       OR NOT pg_has_role(session_user, 'umnia_operator', 'member') THEN
        RAISE EXCEPTION 'not_ready: inventory operator schema or capability unavailable' USING ERRCODE = 'U14NR';
    END IF;
    IF p_workspace_id IS NULL OR p_module_id IS DISTINCT FROM 'inventory.basic'
       OR p_version IS DISTINCT FROM '0.1.0' OR reason_text IS NULL
       OR char_length(reason_text) NOT BETWEEN 1 AND 500 THEN
        RAISE EXCEPTION 'invalid: inventory grant input' USING ERRCODE = 'U14IV';
    END IF;
    PERFORM 1 FROM public.workspaces WHERE id = p_workspace_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'not_found: workspace' USING ERRCODE = 'U14NF';
    END IF;
    SELECT e.* INTO existing FROM public.module_entitlements e
        WHERE e.workspace_id = p_workspace_id AND e.module_id = p_module_id;
    IF FOUND THEN
        IF existing.version <> p_version THEN
            RAISE EXCEPTION 'conflict: different version already granted' USING ERRCODE = 'U14CF';
        END IF;
        SELECT EXISTS (
            SELECT 1 FROM public.operator_audit_events a
            WHERE a.workspace_id = p_workspace_id AND a.module_id = p_module_id AND a.version = p_version
              AND a.action = 'entitlement_granted' AND 'operator:' || a.operator_id = existing.granted_by
              AND a.source = existing.source AND a.reason = existing.reason AND a.created_at = existing.granted_at
        ) INTO matched_audit;
        RETURN QUERY SELECT (CASE WHEN matched_audit THEN 'already_granted' ELSE 'legacy_existing' END),
            p_workspace_id, p_module_id, p_version, existing.granted_by, existing.granted_at, matched_audit;
        RETURN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.module_registry r WHERE r.module_id = p_module_id AND r.version = p_version) THEN
        RAISE EXCEPTION 'not_found: catalog tuple missing' USING ERRCODE = 'U14NF';
    END IF;
    now_at := clock_timestamp();
    INSERT INTO public.module_entitlements (workspace_id, module_id, version, granted_at, granted_by, source, reason)
        VALUES (p_workspace_id, p_module_id, p_version, now_at, 'operator:' || actor, 'operator-cli', reason_text);
    INSERT INTO public.operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result, created_at)
        VALUES (p_workspace_id, p_module_id, p_version, actor, 'entitlement_granted', 'operator-cli', reason_text, 'granted', now_at);
    RETURN QUERY SELECT 'granted'::text, p_workspace_id, p_module_id, p_version, 'operator:' || actor, now_at, true;
END;
$body$;
    $function$;
    EXECUTE 'ALTER FUNCTION app.grant_inventory_entitlement(uuid, text, text, text) OWNER TO umnia_grant_exec';
    EXECUTE 'REVOKE ALL ON FUNCTION app.grant_inventory_entitlement(uuid, text, text, text) FROM PUBLIC';
    EXECUTE 'GRANT EXECUTE ON FUNCTION app.grant_inventory_entitlement(uuid, text, text, text) TO umnia_operator';
    EXECUTE 'REVOKE CREATE ON SCHEMA app FROM umnia_grant_exec';
END;
$grant$;

INSERT INTO schema_migrations (version) VALUES ('015_inventory_basic') ON CONFLICT (version) DO NOTHING;

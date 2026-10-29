-- UMNIA v0.4 — SQL reference, NOT production-ready migration.
-- PostgreSQL; privileges, composite FK, indexes, tenant context, migration versions require hardening.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE IF NOT EXISTS identity_subjects (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  contact_email text UNIQUE,
  contact_phone text UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (contact_email IS NOT NULL OR contact_phone IS NOT NULL)
);
CREATE TABLE IF NOT EXISTS workspaces (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  owner_subject_id uuid NOT NULL REFERENCES identity_subjects(id),
  timezone text NOT NULL DEFAULT 'Asia/Jakarta',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS workspace_memberships (
  workspace_id uuid NOT NULL REFERENCES workspaces(id),
  subject_id uuid NOT NULL REFERENCES identity_subjects(id),
  status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','active','suspended')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, subject_id)
);
CREATE TABLE IF NOT EXISTS catalog_items (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id),
  sku text NOT NULL,
  name text NOT NULL,
  revision bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(workspace_id, sku),
  UNIQUE(workspace_id,id)
);
CREATE TABLE IF NOT EXISTS price_versions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL,
  item_id uuid NOT NULL,
  price_list_id text NOT NULL DEFAULT 'standard',
  valid_from timestamptz NOT NULL,
  valid_to timestamptz,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  amount_minor bigint NOT NULL CHECK(amount_minor >= 0),
  currency char(3) NOT NULL DEFAULT 'IDR',
  CHECK(valid_to IS NULL OR valid_to > valid_from),
  FOREIGN KEY(workspace_id,item_id) REFERENCES catalog_items(workspace_id,id),
  EXCLUDE USING gist (workspace_id WITH =, item_id WITH =, price_list_id WITH =,
    tstzrange(valid_from, valid_to, '[)') WITH &&)
);
CREATE TABLE IF NOT EXISTS workspace_module_installations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id),
  module_id text NOT NULL,
  module_version text NOT NULL,
  exclusive_group text,
  lifecycle text NOT NULL CHECK(lifecycle IN ('planning','awaiting_approval','installing','active','suspended','read_only','migrating','failed_recoverable')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(workspace_id,module_id)
);
-- Extension: prevent two ACTIVE editions per exclusive_group with partial unique index.
CREATE UNIQUE INDEX IF NOT EXISTS one_active_module_per_group
  ON workspace_module_installations(workspace_id,exclusive_group)
  WHERE lifecycle = 'active' AND exclusive_group IS NOT NULL;

CREATE TABLE IF NOT EXISTS audit_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id),
  actor_subject_id uuid REFERENCES identity_subjects(id),
  event_type text NOT NULL,
  entity_type text NOT NULL,
  entity_id uuid,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  correlation_id uuid,
  occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS outbox_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id),
  aggregate_type text NOT NULL,
  aggregate_id uuid NOT NULL,
  event_type text NOT NULL,
  event_version integer NOT NULL DEFAULT 1,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  delivered_at timestamptz
);

-- Illustrative RLS: application role must NOT be table owner/superuser/BYPASSRLS.
-- Mandatory runtime: BEGIN; SET LOCAL app.workspace_id = '<authorized uuid>'; ... COMMIT;
-- NOT secure by itself if role can choose arbitrary workspace_id. Backend must verify membership!
ALTER TABLE catalog_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_items_select ON catalog_items FOR SELECT
  USING (workspace_id = NULLIF(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_items_insert ON catalog_items FOR INSERT
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id',true),'')::uuid);
CREATE POLICY tenant_items_update ON catalog_items FOR UPDATE
  USING (workspace_id = NULLIF(current_setting('app.workspace_id',true),'')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id',true),'')::uuid);
-- Other tenant tables require policies before business deployment. No permissive fall-through.

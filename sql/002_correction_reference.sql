-- UMNIA v0.4 — SQL partial reference; not production-ready migration.
CREATE TABLE IF NOT EXISTS incident_cases (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id),
  reported_by_subject_id uuid NOT NULL REFERENCES identity_subjects(id),
  incident_type text NOT NULL,
  description text NOT NULL,
  case_status text NOT NULL DEFAULT 'reported' CHECK (case_status IN
   ('reported','collecting_evidence','investigating','impact_ready','awaiting_approval',
    'approved','rejected','disputed','applying','failed_retryable','reconciled')),
  discrepancy_minor bigint,
  actual_loss_minor bigint,
  currency char(3) NOT NULL DEFAULT 'IDR',
  revision bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(workspace_id,id)
);
CREATE TABLE IF NOT EXISTS incident_evidence (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL,
  incident_id uuid NOT NULL,
  object_id uuid NOT NULL,
  content_sha256 text NOT NULL,
  classification text NOT NULL DEFAULT 'restricted',
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(workspace_id,incident_id) REFERENCES incident_cases(workspace_id,id)
);
CREATE TABLE IF NOT EXISTS correction_proposals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL,
  incident_id uuid NOT NULL,
  proposed_by_subject_id uuid NOT NULL REFERENCES identity_subjects(id),
  plan_digest text NOT NULL,
  plan jsonb NOT NULL,
  status text NOT NULL DEFAULT 'draft' CHECK(status IN
    ('draft','awaiting_approval','approved','rejected','applying','reconciled','failed_retryable')),
  version bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(workspace_id,incident_id) REFERENCES incident_cases(workspace_id,id),
  UNIQUE(workspace_id,id)
);
CREATE TABLE IF NOT EXISTS correction_approvals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL,
  proposal_id uuid NOT NULL,
  approver_subject_id uuid NOT NULL REFERENCES identity_subjects(id),
  approved_plan_digest text NOT NULL,
  decision text NOT NULL CHECK(decision IN ('approved','rejected')),
  reason text NOT NULL,
  decided_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(workspace_id,proposal_id) REFERENCES correction_proposals(workspace_id,id)
);
-- Enforce proposer != approver via application transaction and/or DB trigger.
-- Implement immutable posted journal/payments in dedicated domain migration, not in this sample.
-- RLS must be enabled + complete policies on every tenant table BEFORE any production use.

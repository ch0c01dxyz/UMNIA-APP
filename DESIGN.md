# UMNIA Design Contract v0.5 (Proposed)

**North star:** 'Tampilan sesederhana buku catatan usaha; kemampuannya setara sistem bisnis profesional.'

Priorities: novice Indonesian UX, readable type, predictable navigation, minimal cognitive load, minimal UI noise, clear statuses, reversible preparation, transparent consequences, accessible mobile POS/tablet and desktop.

Do not render Git/accounting/backend jargon for novices. See `docs/12-design-system-ux.md` for exact mapping and interaction acceptance. Admin portal must look distinct from business app. Design tokens, primary brand palette and typography are **proposals** until reviewed and frozen by owner.

## Components to standardize

`AppShell, WorkspaceSwitcher, ModuleNav, StatusLabel, ActionButton, ConfirmImpactDialog, PreviewDiff, SafePointTimeline, PermissionScopePicker, PriceEffectiveTimeline, ResourceBookingGrid, TherapistStatusBoard, EvidenceModal, DataConfidenceChip, AIApprovalPanel, ErrorRecoveryPanel, EmptyState, ResponsiveDataTable`.

## Action philosophy

Primary CTA answers 'what happens next?'; no hidden writes on mere preview. Risk-sensitive actions show scope and impact; never silently post refund or payroll deduction. Always display operation mode (production vs simulation). Screen design requires loading/empty/error/no-permission states, dark/light contrasts and keyboard focus.

## Design tool policy

Use verified Impeccable or OpenDesign MCP if available; do not claim installed or executed without tool evidence. Capture screenshots on actual running implementation. Human reviews high-risk UX: finance, payroll, module downgrade, backup restore and permission sharing.

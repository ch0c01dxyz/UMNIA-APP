# 06 — Beginner UX, Authentication, API & Mobile

## UX principles

- `Tindakan sederhana dulu`: dashboard per role. Hide advanced concepts behind `Lihat detail` and contextual education.
- Every consequential change shows `Sebelum` -> `Sesudah` -> `Dampak` -> `Setujui/Batalkan`; provide clear loading, retry, failure and support states.
- Beginner vocabulary: `Titik Aman` (checkpoint), `Ruang Percobaan` (branch), `Pulihkan Data`, `Lihat Perubahan`, `Periksa Kesalahan`, `Minta Persetujuan`.
- Users can access guided simulation via `Coba tanpa mengubah data asli`. Animation through UI scene descriptors (Vue/SVG/CSS), not arbitrary AI-produced JS.
- Accessible keyboard/focus/modal interactions; localization Bahasa Indonesia as first-class; `prefers-reduced-motion` supported.

## Proposed navigation

Pemilik: **Beranda**, **Usaha Saya**, **Transaksi**, **Barang & Layanan**, **Pelanggan**, **Laporan**, **Karyawan**, **Bantuan AI**, **Keamanan Data**, **Pengaturan**. Only show active/allowed modules; custom modules attach contextual nav sections.

Staf karaoke/spa: fullscreen `Monitor Operasional` with access classification. `Nama Lengkap / Kode Terapis` per display policy; default staff_code for public screens; treat patient/customer names as sensitive.

## Login & onboarding

- Register: email/phone -> select configured password or OTP email/SMS/WhatsApp -> verify possession of supplied contact -> workspace name -> choose modules -> dashboard.
- Login: email/phone -> available registered method -> optional 2FA if configured; mandatory step-up for some dangerous actions regardless of login 2FA setting.
- Strong recommendation to offer phishing-resistant passkeys, account recovery policy and replay-resistant OTP with rate limit and expiry.
- Employee invite: one-time expiry token issued by owner/manager, private communication, consent/identity confirmation; token revoked on use; metadata retained for audit, not raw token.

## RBAC + ABAC + entitlements

All decisions evaluate subject membership, role, workspace, active module capability, edition, purchased entitlement, resource restriction (branch/department), special delegated authority and operation risk.

Key flows: employee promotion effective dated with prior permission revocation/session recheck; transfer workspace ownership requires special multi-party/step-up approval; temporary delegation explicit expiry.

## API conventions

`/api/v1/workspaces/{workspace_id}/...` REST resources. OpenAPI documents per workspace capability and edition. Route present in server does not imply authorized exposure.

Examples:
```http
GET /api/v1/workspaces/{workspace_id}/modules
GET /api/v1/workspaces/{workspace_id}/inventory/items
POST /api/v1/workspaces/{workspace_id}/rental/contracts
POST /api/v1/workspaces/{workspace_id}/corrections
GET /api/v1/workspaces/{workspace_id}/corrections/{case_id}/impact
POST /api/v1/workspaces/{workspace_id}/corrections/{case_id}/approval
```

Mutation contract requires `Idempotency-Key` (where applicable), a client-request ID and revision check (`If-Match` for edits). Errors follow consistent typed envelope with Indonesian friendly message, stable machine code and request id.

Illustrative error:
```json
{
  "error": {
    "code": "MODULE_NOT_ACTIVE",
    "message": "Fitur Keuangan belum diaktifkan untuk usaha ini.",
    "request_id": "req_example",
    "details": {"required_capability": "finance.summary.read"}
  }
}
```

API key scopes linked to workspace, grant & expiry, hashed tokens at rest, rotation and revocation. Webhook signed; retry, delivery attempt/idempotent consumers, no auto firing from simulation. API docs should include request/response, auth, pagination, rate limits, error semantics and example curl.

## Mobile rollout

- Initial: responsive Nuxt PWA that honors role and installed features; single installable UMNIA app.
- Android: optional Trusted Web Activity for official branded app after testing and signing.
- iOS: Home Screen web app initially; App Store native wrapper only where justified and platform policies allow.
- Offline: support simple draft/queue flows later with idempotent sync and conflict resolution; no offline financial posting or payment state claims without reconciled online confirmation.

## Key UI acceptance

A nontechnical user should be able to invite an employee, enter rental, locate a returned item, inspect past pricing, report an input error, and understand correction preview without knowledge of database/Git/accounting terms. Validate through moderated usability tests; record errors and comprehension not just task completion.

## Design process additions (v0.5)

`DESIGN.md` and `12-design-system-ux.md` are binding for all UI tasks. Use verified OpenDesign MCP **or** Impeccable skill when available for design critique/prototypes, then perform actual rendered screenshot QA + keyboard/contrast checks; do not claim these tools installed unless verified.

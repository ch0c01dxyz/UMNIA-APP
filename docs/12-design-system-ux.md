# 12 — UMNIA Design System, OpenDesign MCP / Impeccable Workflow

## Design objective

Professional, clean, accessible, **beginner-first Bahasa Indonesia** UX, usable on low-end Android, tablets, POS displays, and desktop. The visual language should be quiet, consistent, dense only where warranted (inventory tables), with obvious statuses and safe actions. Simple presentation must not hide critical consequences.

## Design tool policy

- **Impeccable** (`pbakaus/impeccable`) is the recommended default for critique, polish, layout quality and design-system consistency; inspect its current SKILL.md and local availability. Treat it as an *execution aid*, not a substitute for visual verification.
- **OpenDesign** (`nexu-io/open-design`) MCP or skills optional for generating/iterating design proposals and interactive prototypes; install only when supported in the selected agent harness. Official documented examples include `od mcp install codex`, `od mcp install claude`, `od mcp install hermes`, with `--print` preview before modifying configuration. Commands and support can change; verify runtime.
- **Either must feed the same frozen tokens/components**. Running both concurrently on the same design files is discouraged; designate one design owner. Do not require both tools installed on every Flash coding worker. If neither available, use repository DESIGN.md and manual screenshot QA; no fake claim tool ran.
- Treat third-party skills and MCP as code with privileges: pin vetted release, inspect permissions, isolate secrets and sandbox generated code; no unreviewed shell execution.

Official sources:
- https://github.com/nexu-io/open-design
- https://github.com/pbakaus/impeccable

## Required design artifacts prior to UI implementation

```text
DESIGN.md                  # product philosophy, tokens, UX vocabulary and accessibility
packages/ui/tokens/       # semantic color, spacing, type, radius, elevation, motion
packages/ui/components/   # tested shared components
apps/web/layouts/         # role-aware shells
apps/admin/layouts/       # clearly distinct control-plane surfaces
design/flows/             # login, workspace, rental, correction, install modules
design/screenshots/       # desktop/mobile/tablet reference baselines
design/usability/         # scripts, task completion and comprehension metrics
```

### Proposed design tokens (not yet approved visual identity)

- Grid: 4/8px spacing scale; stable component sizes and alignment; max readable line length.
- Typography: local-friendly high-legibility font, supported Indonesian glyphs; body 14–16px, large touch-target text; test Android font rendering.
- Colors: semantic tokens `surface`, `text`, `border`, `primary`, `success`, `warning`, `danger` with light/dark modes; no red as sole error signal.
- Rounded corners and shadows restrained; no glossy, gamified finance UI.
- Motion: meaningful progressive disclosure and guidance; respect `prefers-reduced-motion`, no forced auto-play.
- Mobile: primary action reachable one-handed where possible, 44x44px minimum target; desktop dense tables remain keyboard usable.
- Table design: fixed money column alignment, local date/number formatting, context filters and sticky headers, CSV export scoped to permissions.

## Navigation architecture

Business app default navigation built from enabled module capabilities AND RBAC, with max visible primary categories appropriate to screen size. Typical primary: `Beranda`, `Transaksi`, `Barang`, `Pelanggan`, `Laporan`, `Lainnya`. Business-specific activities override category labels. Avoid an empty menu simply because a module isn't installed; show helpful next action.

Admin UI uses a deliberately different visual treatment and access endpoint; never accidentally blend with business workspace role navigation.

## Beginner-friendly language mapping

| Internal | Shown to end users |
|---|---|
| rollback | Pulihkan Perubahan |
| branch | Ruang Percobaan |
| checkpoint | Titik Aman |
| merge | Terapkan Hasil Percobaan |
| revert transaction | Buat Perbaikan Transaksi |
| effective date | Berlaku Mulai Tanggal |
| ledger | Catatan Keuangan Lengkap |
| dependency conflict | Modul Ini Membutuhkan Fitur Lain |
| module downgrade | Gunakan Versi Lebih Sederhana |
| write permission | Boleh Mengubah Data |
| incident evidence | Bukti & Riwayat Kejadian |
| reconciliation | Cocokkan Data |

### Action labels by safety

- Read: `Lihat Riwayat`, `Lihat Perbedaannya`, `Lihat Bukti`, `Periksa Tarif`.
- Prepare: `Coba Dulu`, `Lihat Dampaknya`, `Simpan sebagai Draf`.
- Apply: `Terapkan Perubahan`, `Setujui Perbaikan`, `Kirim Pengembalian Dana` — require typed summary of effect and correct permission.
- High risk: explicit 2-stage review and accessible confirmation `Saya memahami perubahan ini`; avoid generic `OK` or `Ya` for destructive actions.
- Preserve action differentiation: `Nonaktifkan Modul` != `Hapus Data` != `Akhiri Langganan`.

## Screen requirements / acceptance

1. **Login & Register**: Email/phone + allowed methods (password, OTP email/SMS/WhatsApp); 2FA optional configured; passkey optional; errors cannot enumerate accounts; resend throttling; loading and recovery.
2. **Workspace Setup**: name business -> choose starter modules -> summary cost/duration -> confirm; default minimal setup, defer advanced optional settings.
3. **Dashboard**: role- and module-aware, actionable alerts, no irrelevant tiles; language novice comprehensible.
4. **Module Catalog**: search/filter, one-line benefit, per-day/month price, conflicts + before/after preview, data preservation explanation truthful.
5. **Rental**: fast check-out/return, available asset status, conditions/damage, effective prices, overdue and clear empty states.
6. **Correction**: `Laporkan Kesalahan`, evidence, affected list, preview, approvals; paid/posting states emphasized; no hidden destructive change.
7. **Restore Center**: `Perbaiki Data Salah`, `Kembalikan Data Terhapus`, `Pulihkan dari Salinan`; indicate operations not safely revertible.
8. **Spa therapist monitor**: public defaults code, internal opt-in name by authorized role, package editor versioned, accessible color + text statuses.
9. **Simulation**: mode label persistent 'Ruang Percobaan — tidak mengubah usaha asli', never ambiguous; copies vs synthetic clearly indicated.
10. **Finance**: income/expense novice copy, `Lihat Cara Menghitung` shows sources/formula; formal reports marked when incomplete/unverified.
11. **Mobile**: 320–430px + tablets + desktop; keyboard/low motion, portrait POS, offline messaging, slow network.
12. **Error pages**: useful recovery action, no stack traces or secret values.

## Required UI workflow / design review gates

A. **Problem brief** (audience, task, existing workflow, edge cases). B. **Wireframe** 2 alternatives for uncertain screens. C. **DESIGN.md freeze** with component tokens. D. **Visual prototype** OpenDesign MCP if installed. E. **Impeccable critique/polish** or independent manual critique. F. **Implementation** Vue/Nuxt components. G. **Screenshot verification** small mobile/tablet/desktop, portrait+landscape where relevant. H. **Accessibility** keyboard/focus/contrast/labels/voiceover basic test. I. **Usability** 3–5 novice participants as initial research target (not statistical validation); measure ability to complete tasks and comprehension. J. **Leader merge gate** compares DOM behavior to reviewed design; no claim 'pixel-perfect' without verified screenshots.

## Flash UI Task Packet example

- Task: `UMN-UI-011` — design `Titik Aman` restore center page only.
- Read `DESIGN.md`, UX mapping, permissions table, restore API contract.
- Allowed: UI route and localized tests; no backend changes.
- States: empty, loading, backups exist, backup corrupted/unavailable, lack permission, partial restore available.
- Primary button: `Lihat Cara Memulihkan`; dangerous action only after preview + server approval.
- Test: screenshot width 360/768/1440, keyboard tab/focus, `prefers-reduced-motion`, screen reader labels, disabled controls, locale formatting, logged-out redirect.
- Evidence: screenshots, test output and PR diff; independent design review before merging.

## Performance / low-spec policy

Use lazy module routes; split bundles; avoid autoplay video or large animation libs for onboarding; virtualize very large tables with accessible fallback; responsive components; compress images; loading skeletons but don't obscure error states. Real device metrics (FCP/LCP, interactions, memory) must be measured before setting product SLA.

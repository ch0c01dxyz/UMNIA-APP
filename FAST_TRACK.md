# UMNIA — FAST-TRACK EXECUTION PLAN v0.6

**BACA INI PERTAMA.** Ini **satu-satunya rencana eksekusi utama**. P0–P8 dari v0.5 disimpan hanya sebagai referensi teknis, **bukan urutan pekerjaan wajib**. Tujuannya: aplikasi yang bekerja sesegera mungkin, dengan pengujian minimum yang tidak boleh dilewati.

## Target rilis

- **MVP pertama:** pemilik usaha dapat registrasi/login, membuat workspace, menambah staf, mengaktifkan modul Rental + Stok Basic + Customer + Finance Basic, memproses rental dari awal sampai selesai, melihat hasil dan melakukan koreksi yang aman.
- **Setelah MVP:** aktifkan pembeda utama UMNIA (AI, BYOK, simulasi, migrasi edisi) dan perluas modul industri memakai komponen reusable. Tidak perlu menunggu semua industri selesai untuk memulai pilot yang aman.
- **Jangan mengklaim siap produksi** hanya karena tampilan terlihat berfungsi. Keamanan data, transaksi, restore, dan aturan keuangan yang digunakan wajib lolos pengujian.

## Empat fase; langsung implementasi

| Fase | Tugas langsung | Gate hasil (harus dapat didemonstrasikan) |
|---|---|---|
| **F1 — Foundation** | Repo + CI; Go/Nuxt; Windows **Docker Compose** dan Linux **native APT/systemd**; PostgreSQL bisnis/admin terpisah; RustFS adapter; login + undangan staf + workspace + RBAC/RLS; registry modul; audit & migration | Di Windows dan Linux: login → buat workspace → undang staf → aktifkan Rental. Tes isolasi lintas-tenant dan admin vs bisnis lulus. |
| **F2 — Usable Business MVP** | Customer + Inventory Basic + Rental Basic + tarif bertanggal efektif + retur/rusak/hilang; Finance Basic + pajak sesuai kasus; transaksi, pembayaran, laporan; koreksi minimal + audit/approval | Rental ujung-ke-ujung berjalan; harga historis benar; pembayaran yang sudah selesai tidak ditimpa; stok konsisten; koreksi aman. |
| **F3 — UMNIA Differentiators** | Core checkpoint + backup/restore teruji; simulation sandbox; AI UMNIA → akanesia-router; BYOK → provider langsung; validator + tool policy; import/export format pengguna; edition upgrade/downgrade dan preview migrasi; Finance Advanced bila data siap | Simulasi tidak mengubah produksi; AI tidak bisa akses tenant lain; data lama tetap tersedia sesudah perubahan modul; restore uji berhasil. |
| **F4 — Pilot & Expand** | Billing per-modul/promo, PWA mobile, white-label, API adaptif, operasi produksi; pilot terbatas; lalu factory modul reusable: Booking → Karaoke/Spa/Billiard/Cinema/Restaurant/Supplier/HR | Billing & akses modul konsisten; monitoring/backup berjalan; pilot lulus; modul baru memenuhi kontrak dan tes reusable. |

**Keamanan yang tidak boleh ditunda:** pengamanan autentikasi, otorisasi workspace dan endpoint, RLS, audit transaksi, aturan uang dan pembayaran, idempotensi, secret management, backup awal, serta validasi input harus dibangun **ketika fiturnya pertama kali dibuat**, bukan menunggu F3/F4. F3 memperluas pengalaman recovery dan AI, bukan memperbaiki fondasi yang sengaja tidak aman.

## Urutan pertama agar coding segera dimulai

1. **F1-01:** buat skeleton Go/Nuxt, lint, test, CI, `.env.example` (tanpa rahasia). Uji health/readiness.
2. **F1-02:** hidupkan PostgreSQL bisnis + admin dan RustFS; Windows via Compose, Linux via native services. Validasi migrasi dan konektivitas.
3. **F1-03:** implementasi identity, workspace, membership, izin, undangan sekali pakai, audit; tes request tenant A → data tenant B harus ditolak.
4. **F1-04:** manifest/capability module registry + aktif/nonaktif; tes dependensi, konflik, dan penolakan endpoint modul tidak aktif.
5. **F2-01:** workflow utama Rental + Customer + stok + harga efektif; tambah tes transaksi bersamaan.
6. **F2-02:** Finance Basic dan koreksi transaksi minimum, pembayaran dan pajak kasus yang sesuai; tes total, pembatalan/refund, laporan.

Setelah F2-02 lulus, **uji coba terbatas**, lalu teruskan F3/F4. Jangan memaksakan puluhan modul sekaligus.

## Cara kerja agent: hemat koordinasi

- **Frontier leader:** memutuskan kontrak sekali untuk area yang sedang dikerjakan, memecah menjadi tiket kecil, review keamanan/kompleksitas, menyelesaikan blocker. Tidak perlu membuat proposal berulang jika keputusan sudah ada.
- **Flash worker:** ambil tiket siap; kerjakan perubahan terkecil yang lengkap; langsung jalankan tes; laporkan diff + hasil singkat. Boleh mengusulkan solusi kreatif selama tetap di scope atau minta keputusan atas perubahan kontrak.
- **QA/reviewer:** utamakan otomatisasi tes. Review manual terfokus pada auth, RLS, uang/pajak/payroll, data migration, AI tools, backup, dan tindakan berisiko tinggi. Jangan membuat rapat/gate dokumen untuk setiap perubahan kosmetik.
- **Paralel:** UI, API, dan deployment boleh berjalan bersama **sesudah interface kontrak minimal disepakati**. Hindari dua worker menulis file/migrasi yang sama tanpa pemilik yang jelas.
- **Satu tiket =** hasil nyata, file/scope, kontrak terkait, perintah tes, kondisi lulus. Tidak perlu esai panjang atau tanggal target yang dibuat-buat.

## Fast task format (boleh teks biasa, bukan wajib JSON)

```text
TASK: F1-01 health & app skeleton
GOAL: Aplikasi web dan Go API menyala dengan health endpoint.
SCOPE: apps/web, services/business-api, CI config; jangan sentuh auth/finance.
DEPENDENCY: layout repository disetujui leader.
DONE WHEN: health 200; frontend route tampil; lint/test dan startup di jalur Windows/Linux lulus.
PROOF: daftar file, perintah tes, hasil, keterbatasan.
```

## Aturan stop / eskalasi

Hentikan **hanya tiket yang bermasalah** jika: kontrak ambigu yang bisa merusak data, migrasi destruktif, celah auth/RLS, perhitungan uang/pajak tidak berdasar, integrasi API/model belum terverifikasi, atau tes penting gagal. Lanjutkan tiket independen lainnya. Frontier mengambil alih jika masalah kompleks, bukan menahan seluruh proyek.

## Kontrol mutu tanpa birokrasi

**Wajib sebelum merge:** kompilasi/typecheck; unit dan integration test relevan; lint; diff review; migrasi up pada database sementara; no secrets; tidak ada klaim fitur selesai tanpa bukti.

**Tambahan per risiko:** multi-tenant isolation, race transaksi, billing/refund idempotency, backup restore drill, AI tool sandbox, browser QA desktop/mobile, simulasi tanpa side effects, payroll compliance review.

**Anti-halusinasi adaptif:** ide/eksplorasi LLM bebas; klaim implementasi harus didukung kode, dokumentasi aktual, atau tes. Tuliskan `UNVERIFIED` untuk hal yang belum diuji; jangan membuat verifikasi palsu. Lihat `docs/09-advanced-anti-hallucination.md`.

**UI/UX:** gunakan `DESIGN.md`, Impeccable atau OpenDesign MCP jika tersedia. Jangan jadikan pemasangan tool desain sebagai blocker untuk menulis dan menguji UI yang bersih. Tes setidaknya mobile dan desktop untuk setiap alur yang akan dirilis.

**Opsional:** Redis dan PgBouncer **tidak** dipasang dulu; aktifkan jika benchmark menunjukkan bottleneck. Perhatikan isolasi RLS jika PgBouncer digunakan.

## Dokumen sumber seperlunya (bukan wajib dibaca semua sekaligus)

- Arsitektur/kontrak data: `docs/01-architecture.md`, `docs/02-core-data.md`
- Kontrak modul: `docs/03-module-engine.md`, contoh `examples/*.module.json`
- Koreksi/AI: `docs/04-correction-engine.md`, `docs/05-ai-simulation.md`
- UX & operasi: `DESIGN.md`, `docs/06-ux-api.md`, `docs/07-pricing-tax-operations.md`, `docs/11-deployment-matrix.md`
- Detail lama: `docs/reference/13-detailed-phase-execution-v0.5.md` **hanya sebagai backlog referensi**, jangan diperlakukan sebagai sembilan gerbang wajib.

**Catatan:** Ini rencana eksekusi, bukan bukti bahwa aplikasi sudah dibuat atau diuji.

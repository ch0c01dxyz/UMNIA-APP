# F2-UI-06 — Serah terima + pengembalian unit (proposal)

Status: PROPOSED, docs-only. Baseline: `13bfa099cbb2a2cfbd71869d9db287c01e6c8c29`, branch `f1-03-identity`. Dokumen ini rekomendasi slice implementasi berikutnya; bukan bukti operasional dan bukan otorisasi implementasi. Implementasi menunggu pembekuan Leader (kontrak UI tersendiri, mengikuti pola F2-UI-05).

## 1. Masalah dan hasil yang dapat digunakan

Alur rental di UI hari ini berakhir di harga yang disepakati ("Harga draf", F2-UI-05). Staf tidak bisa mencatat serah terima unit ke pelanggan atau pengembaliannya dari UI: tidak ada halaman, tautan, atau panggilan proxy untuk itu. Akibatnya satu rental tidak pernah bisa diselesaikan di aplikasi — padahal target MVP `FAST_TRACK.md` adalah "memproses rental dari awal sampai selesai".

Slice ini memberi staf satu halaman per draf untuk menyerahkan satu unit fisik (handover) lalu mencatat pengembaliannya (utuh / rusak / hilang), dengan kondisi aset yang ikut berubah untuk rusak/hilang. Setelah slice ini, alur rental ujung-ke-ujung di UI menjadi: draf → harga disepakati → serah terima → kembali.

## 2. Bukti kondisi source saat ini (13bfa09)

Backend sudah lengkap dan diterima; UI-nya yang belum ada:

- `POST /workspaces/{id}/rental/contracts/{draft_id}/handover` (`services/business-api/main.go:156`, `custody.go:108`): body ketat `{asset_id, hold_id}` + satu Idempotency-Key. Guard beku (manage_business_data + instalasi rental.basic aktif + entitlement versi eksak + view_workspace) dicek SEBELUM replay. Replay kunci sama + draft/aset/hold sama → 200 catatan saat ini; beda → 409; draft sudah punya custody → 409. Prasyarat: fakta harga ada dan cocok dengan draft (409 bila belum ada), hold hidup (`held` DAN `expires_at` > cutoff, `custody.go:350-353`), hold milik draft+aset ini, item aset sama dengan item draft, cutoff sebelum `draft.ends_at` (`custody.go:358-364`, tanpa grace window). Hold dikonsumsi `held → consumed` dalam transaksi yang sama (`custody.go:415-420`).
- `POST /workspaces/{id}/rental/contracts/{draft_id}/return` (`main.go:157`, `custody.go:467`): body kosong / `{}` / `{"outcome": "returned"|"damaged"|"lost"}` saja; kosong → `"returned"`. Keyspace Idempotency-Key terpisah dari handover. Return TIDAK mensyaratkan instalasi/entitlement rental aktif (guard menyempit: manage_business_data + view_workspace), sehingga unit yang sudah keluar tetap bisa diterima sesudah revoke/deactivate. Terminal: `out → returned | damaged | lost` (`migrations/012_rental_damaged_lost_return.sql:73-76`); rusak/hilang memutasi kondisi aset `ready → damaged/lost` (`custody.go:677-684`); hilang membiarkan `returned_*` NULL dan mengisi `resolved_*` (`custody.go:161-172`).
- `GET .../custody` (`main.go:158`, `custody.go:730`): view_workspace saja; draft tidak ada → 404; draft tanpa custody → 404 spesifik (bukan fakta kosong).
- `POST .../holds` (`main.go:148`, `holds.go:105`): body `{asset_id}` + Idempotency-Key. `GET .../rental/holds` (`main.go:150`, `holds.go:537`): daftar seluruh hold workspace, paginasi limit/offset, tanpa filter per-draf.
- Skema: `rental_custody` (`migrations/010_rental_custody.sql`: status `out`/`returned`, UNIQUE per draft + per kunci, partial UNIQUE per aset saat `out`); migrasi 012 memperluas status + kondisi aset. Backend diuji `services/business-api/module_custody_integration_test.go`; Go suite 199/252 hijau pada SHA integrasi (CI run 44540362581).
- UI: tidak ada halaman custody/serah-terima/pengembalian (`apps/web/pages/` berisi 12 file; grep `handover|custody|serah` pada `apps/web/e2e/` kosong). Proxy `apps/web/server/api/[...path].ts` berisi 24 route shape; TIDAK SATU PUN bentuk handover/return/custody/holds/payments/finance/audit. `rentals.vue:4-5` menyatakan eksplisit halaman draf "tanpa stok/unit/ketersediaan, tanpa holds, tanpa serah-terima/retur/kerusakan, tanpa pembayaran". `rental-prices/[draftId].vue:5-7` menyatakan halaman harga "TIDAK memproses pembayaran".
- Kontrak backend F2-01-F (`docs/reviews/f2-01-f-handover-return-contract.md`, 53 baris, PROPOSED, baseline `7bb1489`) sudah diimplementasikan (`e3f48c6`, ancestor 13bfa09) dan diperluas F2-01-G (`645443a`, rusak/hilang). Keputusan D-HAND-1 (handover+return bersama), D-HAND-2 (hold live + cutoff sebelum ends_at; return kapan saja), D-HAND-3 (guard menyempit untuk return) terverifikasi di `custody.go`.

Yang juga belum ada di UI tetapi BUKAN rekomendasi ini: pembayaran (backend F2-02-A `payments.go`, 1608 baris, diterima; tanpa halaman), ringkasan keuangan (`GET .../finance/summary`, tanpa halaman), audit (`GET .../audit`, tanpa halaman), modul stok/inventaris (tidak ada manifest maupun endpoint — hanya string izin `inventory.stock.read`), pajak (tidak ada backend). Lihat §8.

## 3. Rekomendasi: F2-UI-06 rental custody UI

Satu halaman sibling baru `/workspaces/[id]/rental-custody/[draftId]` ("Serah terima"), mengikuti pola F2-UI-05: rute sibling (rentals.vue tidak punya outlet NuxtPage), tautan per-baris "Serah terima" di riwayat draf, server otoritatif, snapshot/kunci idempotency di memori, pemulihan hasil-tak-diketahui lewat GET. Satu slice mencakup handover DAN return bersama (D-HAND-1): handover saja menciptakan unit keluar permanen tanpa jalur kembali yang didukung.

## 4. Dependensi dan API

Tersedia dan beku — tidak ada backend/migrasi baru yang dibutuhkan:

- Handover, return, GET custody, buat hold, daftar hold: §2.
- Label aset: `GET .../assets` (100-baris, pola UI-02) + kondisi aset di DTO; ketersediaan per-aset (`GET .../assets/{id}/availability`) TIDAK dipakai slice ini — server tetap otoritatif lewat 409 (aset dengan custody terbuka / hold tidak live).
- Fakta harga konteks: `GET .../agreed-price` (pola UI-05); draf: `GET .../rental/contracts` (50-baris, pola UI-04).
- Kurang: 5 bentuk proxy (§5) + halaman + tes. Tidak ada endpoint yang kurang.

## 5. Allowlist yang diusulkan (15 file, cermin UI-05)

BARU: `apps/web/pages/workspaces/[id]/rental-custody/[draftId].vue`; `design/screenshots/f2-ui-06/` (8 PNG + manifest).
UBAH: `apps/web/server/api/[...path].ts` (5 bentuk: GET custody, GET holds `query:true`, POST holds, POST handover, POST return — tanpa query pada tulis); `apps/web/lib/api.ts` (satu alternatif safe-continuation untuk rute sibling, kedua UUID divalidasi); `apps/web/pages/workspaces/[id]/rentals.vue` (satu tautan 44px "Serah terima" per baris riwayat + komentar); `apps/web/e2e/owner-flow.spec.ts`; `apps/web/e2e/proxy-session.spec.ts`.
Tanpa backend/migrasi/auth/RLS/CLI, CSS/header/layout bersama, dependency/lockfile, workflow, atau perubahan timezone Playwright. Lima direktori screenshot lama dipertahankan byte-per-byte.

## 6. Perilaku utama

- Masuk dari tautan "Serah terima" tiap baris riwayat draf (tanpa N+1: tautan tanpa syarat; halaman yang memutuskan dari bacaan — pola "Lihat harga"). Tujuh tautan workspace tetap + `Kembali ke Draf sewa`; bukan destinasi workspace kedelapan. Deep-link/login continuation mendarat di halaman custody aktual.
- Keadaan halaman dari tiga bacaan independen (draft, fakta harga, custody), masing-masing loading/hasil/galat + stale-ownership guard: tanpa custody + tanpa fakta → handover diblokir dengan penjelasan ("Harga belum dicatat"); tanpa custody + fakta ada → formulir handover; custody `out` → formulir return + catatan (unit, aset, hold, `handed_out_at/by`); terminal (`returned`/`damaged`/`lost`) → catatan read-only (`returned_at/by`, `resolved_at/by` untuk hilang, kondisi aset).
- Handover: pilih aset (label map 100-baris, kondisi `ready`, item sama dengan item draft) → pakai hold live untuk draft+aset ini bila `GET holds` menunjukkannya, jika tidak `POST holds {asset_id}` dengan kunci baru → `POST handover {asset_id, hold_id}` dengan kunci baru. Dua POST berurutan, masing-masing snapshot + kunci sendiri; kegagalan langkah pertama tidak membuat kunci langkah kedua.
- Return: pilih hasil (`Utuh` / `Rusak` / `Hilang`) → panel konfirmasi eksplisit → `POST return {"outcome": ...}` dengan kunci return baru. Tanpa timestamp/aktor/biaya dari klien; tanpa kolom catatan (body backend hanya mengizinkan `outcome` — lihat §7).
- Idempotensi/pemulihan (pakai ulang mesin UI-05 R1): satu `crypto.randomUUID` + snapshot immutable per upaya; kunci mentah tidak di-log/persist; validasi 201 DAN 200 sebelum klaim tercatat; hasil tak-diketahui membekukan snapshot + aksi biasa; refresh GET-only (`GET custody`) tanpa mengganti snapshot; klasifikasi settlement 3-arah (kunci sama + semua field cocok → konfirmasi; kunci berbeda → riwayat independen, bukan sukses upaya ini; kunci sama tetapi tidak cocok → bukan keduanya, snapshot + ketidakpastian dipertahankan + galat settlement); pagari POST tertunda bila GET menyelesaikan (`postGen++`); gerbang tulis pertama mensyaratkan bacaan selesai (`canConfirm`/handler).
- Guard (pola D-UI-3): tulis hanya owner/manager (manage_business_data); cashier boleh baca (GET custody 200), POST 403 = hasil izin (data terbaca dipertahankan, sesi TIDAK dibersihkan); 401 = bersihkan sesi + redirect via safe continuation. 404 draf vs 404 tanpa-custody dibedakan. 409 spesifik (fakta belum ada, hold tidak live/cocok, draft sudah punya custody, kunci dipakai untuk permintaan berbeda) ditampilkan apa adanya, tanpa fiksi.
- Tampilan: stempel server UTC apa adanya (netral, tanpa konversi zona); fakta harga konteks memakai tampilan UI-05 (`{amount_minor} unit tarif · {currency}`); tanpa Number/parseFloat/toFixed/konversi rupiah untuk uang; tanpa hitung TTL hold di klien (server yang memutuskan live-ness; `expires_at` hanya ditampilkan).
- Screenshot (8 PNG + manifest): `01-custody-360/768/1440` (live `out`), `04-custody-nofact-360` (tanpa fakta, handover diblokir), `05-custody-noperm-360` (penolakan baca), `06-custody-unknown-360` (tak-diketahui injeksi, berlabel), `07-custody-loading-360`, `08-custody-error-360`.

## 7. Kegagalan/recovery dan batas scope

- Handover 409 (fakta belum ada / hold tidak live / aset salah / draft sudah punya custody) → pesan server, tanpa tulis, tanpa klaim. Hold kedaluwarsa di antara pilih dan kirim → 409 yang sama; staf membuat hold baru (kunci baru), bukan retry kunci lama.
- Return sesudah revoke/deactivate → 200 via guard menyempit (server); handover sesudah revoke → 403. Return pada draft tanpa custody → 404.
- Hasil tak-diketahui (500/502/jaringan) → panel tak-diketahui + `Kirim lagi (permintaan sama)` (kunci sama) dan `Mulai baru` yang diberi peringatan (baca ulang state, kunci baru hanya sesudah no-custody terkonfirmasi). Tanpa retry otomatis / loop kunci baru.
- BUKAN bagian slice: pembayaran (lihat §8), koreksi custody salah catat (butuh proposal koreksi terpisah, pola docs/02:43), catatan kerusakan (body backend hanya `{outcome}` — catatan butuh slice backend), deposit/denda/biaya/pajak, stok kuantitas, consent/tanda tangan pelanggan (custody adalah catatan staf, BUKAN perjanjian hukum), perubahan status draf (draft tetap `draft`; custody tidak memutasi draft/harga/fakta).

## 8. Yang tidak direkomendasikan sekarang

- **Pembayaran UI (kandidat F2-UI-07, sesudah slice ini):** backend F2-02-A diterima (proposal dua aktor + pembalikan), tetapi UI-nya lebih besar (6 bentuk proxy, alur dua aktor lintas pengguna, ringkasan) dan menyentuh `rentals.vue` yang sama — dikerjakan berurutan, bukan paralel, untuk menghindari konflik. Urutan ini dipilih karena serah terima menyelesaikan alur fisik rental (MVP), sementara pembayaran tidak mensyaratkan custody (`payments.go` hanya merujuk fakta harga).
- **Ringkasan keuangan UI / audit UI:** baca-saja, nilai lebih rendah dari menyelesaikan rental; ringkasan dapat digabung ke slice pembayaran.
- **Modul stok/inventaris, pajak:** butuh backend + migrasi baru (belum ada), bukan slice UI; di luar "terkecil yang berguna" saat ini.

## 9. Acceptance tests (sesuai risiko)

- Bahagia: draf+harga → buat hold → handover 201 → GET custody `out` → return 200 `returned` → GET terminal; audit `rental_handed_over` + `rental_returned` masing-masing 1; hold `consumed`.
- Rusak/hilang: return `damaged` → kondisi aset `damaged`, aset dikecualikan dari hold/handover berikutnya; return `lost` → `returned_*` NULL, `resolved_*` terisi, kondisi aset `lost`.
- Penolakan: cashier tulis → 403 (data dipertahankan); tanpa fakta → handover 409; hold kedaluwarsa/dilepas → 409; hold/aset salah → 409; handover kedua pada draft sama → 409; replay kunci sama → 200 tanpa audit ganda; kunci berbeda pada custody ada → 409.
- Revoke: return sesudah deactivate → 200; handover sesudah deactivate → 403; GET custody tetap 200.
- Ketidakpastian (cermin R1 UI-05): fakta kunci-sama tak cocok tidak dipromosikan; GET menyelesaikan upaya memagari POST lama; tulis pertama diblokir selama bacaan ditahan; tanpa POST saat panel diblokir.
- Screenshot: 1 tes mencakup 8 PNG + manifest (cermin UI-05). Suite penuh tetap hijau (143 + tes baru); tanpa perubahan backend/migrasi/auth/RLS/CLI.

## 10. Pertanyaan keputusan yang benar-benar belum terjawab

1. Bekukan rute sibling `/workspaces/[id]/rental-custody/[draftId]` + judul "Serah terima"? (Diperlukan; pola UI-05.)
2. Pembuatan hold on-demand di alur handover (diusulkan ya) + pakai ulang hold live yang sudah ada untuk draft+aset sama? (Diperlukan; tidak ada UI hold lain.)
3. Tanpa kolom catatan kerusakan di slice ini — terima, karena body backend hanya `{outcome}`? (Diperlukan; catatan butuh slice backend.)
4. Tautan "Serah terima" tanpa syarat di tiap baris riwayat (halaman yang memutuskan) vs hanya baris dengan fakta harga (butuh baca fakta per baris = N+1, pola yang dilarang)? Diusulkan tanpa syarat.
5. Konfirmasi F2-UI-07 (pembayaran) dikerjakan sesudah F2-UI-06, berurutan? (Diperlukan untuk penjadwalan.)

## 11. Label bukti

VERIFIED (kode/migrasi pada 13bfa09): endpoint + guard + prasyarat + skema + proxy 24 bentuk + 12 halaman + 0 tes E2E custody. REVIEWER-REPORTED: penerimaan F2-01-G (`645443a`) dan F2-02-A (`8010e84`) dari riwayat. PROPOSED: rute, judul, perilaku halaman, allowlist, tes §9, jawaban §10. NOT RUN: implementasi + CI slice ini. Respons injeksi/race terkontrol di tes UI mendatang adalah bukti UI, bukan bukti DB/RLS baru.

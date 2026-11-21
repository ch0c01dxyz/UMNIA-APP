# F2-UI-07 — Pembayaran rental (proposal)

Status: PROPOSED, docs-only. Baseline: `bd1d35b0de82314b4e5970d41aaf231ea01e7256`, branch `f1-03-identity`. Verifikasi integrasi terakhir: [CI run 44867779294](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/44867779294), SUCCESS (Go 199/252, web PASS, E2E 167/167). Dokumen ini rekomendasi slice implementasi berikutnya; bukan bukti operasional dan bukan otorisasi implementasi. Implementasi menunggu pembekuan Leader (kontrak UI tersendiri, mengikuti pola F2-UI-05/F2-UI-06).

## 1. Masalah dan hasil yang dapat digunakan

Alur rental di UI hari ini berakhir di serah terima dan pengembalian unit (F2-UI-06). Staf tidak bisa mencatat pembayaran manual (cash/transfer) atau pembalikannya dari UI: tidak ada halaman, tautan, atau panggilan proxy untuk itu. Akibatnya satu rental tidak pernah bisa dibayar — dan tidak bisa dikoreksi bila salah catat — di aplikasi, padahal target MVP `FAST_TRACK.md` adalah "memproses rental dari awal sampai selesai" dan gerbang F2 mensyaratkan "pembayaran yang sudah selesai tidak ditimpa".

Slice ini memberi staf satu halaman per draf untuk mengusulkan pembayaran, menyetujuinya sebagai aktor kedua, mengusulkan pembalikan catatan, dan menyetujuinya — dengan riwayat posted yang immutable dan total yang dihitung server. Setelah slice ini, alur rental ujung-ke-ujung di UI menjadi: draf → harga disepakati → serah terima → kembali → bayar (→ koreksi bila salah catat).

## 2. Bukti kondisi source saat ini (bd1d35b)

Backend sudah lengkap dan diterima; UI-nya yang belum ada:

- Enam endpoint pembayaran/keuangan (`services/business-api/main.go:156-163`, `payments.go`): POST `.../rental/contracts/{draft_id}/payment-proposals`, POST `.../payment-proposals/{proposal_id}/approvals`, POST `.../payments/{payment_id}/reversal-proposals`, POST `.../payments/{payment_id}/reversal-proposals/{proposal_id}/approvals`, GET `.../rental/contracts/{draft_id}/payments`, GET `/workspaces/{id}/finance/summary`.
- Guard tulis beku (`payments.go:696-751`, `paymentOpenTx`): auth → `manage_business_data` + `view_workspace` → lock baris workspace → `financeGuard`: `finance.basic` 0.1.0 AKTIF + entitlement versi eksak + manifest dikenal → satu cutoff DB. Tidak ada kapabilitas `finance.payment.write` di manifest mana pun; gerbangnya adalah modul/versi eksak + hak bisnis + approval dua aktor.
- Pemberian entitlement finance kini tersedia: migrasi 014 (`app.grant_finance_entitlement` / `app.revoke_finance_entitlement`) + CLI `grant-entitlement` (`--module finance.basic`; revoke hanya `finance.basic`) — F2-02-B DITERIMA di `39d9413`. Celah §9 kontrak F2-02-A ("tidak ada jalur pemberian entitlement finance produksi") SUDAH DITUTUP oleh F2-02-B; slice UI ini tidak perlu jalur grant. Aktivasi tetap aksi owner (`POST .../modules/finance.basic/activate`, `manage_modules`); tanpa aktivasi, tulis Finance tetap 403.
- Uang kanonis: `amount_minor` string (`"0"` atau `[1-9][0-9]*`, tanpa leading zero/tanda/fraksi/eksponen, ≤ MaxInt64; JSON number → 400 — `prices.go:38-61`); currency tepat 3 huruf A-Z dan HARUS sama dengan currency fakta (`prices.go:63-70`); method `cash`/`transfer`; reference opsional, disimpan verbatim 1-128 rune dengan konten non-whitespace; reason pembalikan wajib, di-trim 1-500 rune (`payments.go:310-340`). Total dihitung SQL dalam numeric (`sum(bigint) -> numeric`); handler tidak pernah menjumlah gross dalam int64 dan tidak pernah clamp; data korup → 500 `total tidak konsisten`.
- Model beku (kontrak F2-02-A, DITERIMA di `8010e84`): proposal append-only → fakta posted; tidak ada status yang dimutasi, tidak ada penolakan/pembatalan/kerangka approval umum; proposal pending TIDAK PERNAH memengaruhi total; pembalikan adalah pembatalan CATATAN penuh, BUKAN refund/uang keluar; tidak ada parsial, tidak ada un-reverse. Pembayaran bisa dicatat sesudah fakta harga ada, independen dari status custody; damaged/lost TIDAK menambah tagihan (migrasi 013).
- UI: tidak ada halaman, tautan, atau bentuk proxy pembayaran/keuangan/audit (grep `payment|finance|Pembayaran|Keuangan` pada `apps/web/pages/`, `apps/web/e2e/`, `apps/web/plugins/` kosong; proxy `apps/web/server/api/[...path].ts` berisi 29 bentuk, tidak satu pun `payments`/`finance`/`audit`). `rentals.vue:4-7` menyatakan halaman draf "tanpa pembayaran"; `rental-prices/[draftId].vue:5-7` menyatakan halaman harga "TIDAK memproses pembayaran".
- Tes backend: `services/business-api/module_payments_integration_test.go`, 24 tes (proposal tanpa efek saldo, approval dua aktor, self-approval ditolak, satu aktor gagal tertutup, revoke/deactivate, replay empat keyspace, approval bersamaan, plafon, batas uang, pembalikan penuh, aritmetika gross melebihi BIGINT, RLS langsung, fault injection, upgrade skema, konsistensi snapshot GET, plus 9 tes ringkasan). Go suite 199/252 hijau pada SHA integrasi (CI run 44867779294).

## 3. Matriks aksi

Untuk setiap aksi: endpoint, aktor, izin, prasyarat, request/response, status, dan replay. Semua tulis memakai header `Idempotency-Key` tepat satu yang valid (1-128 karakter `[A-Za-z0-9_.:-]`, `rental.go:28`); empat keyspace terpisah — satu per tabel 013 (`UNIQUE (workspace_id, client_request_id)`).

### 3.1 Usulkan pembayaran

- Endpoint: POST `/workspaces/{id}/rental/contracts/{draft_id}/payment-proposals` (`main.go:156`, `payments.go:780`).
- Aktor dan izin: pengusul (subject dari token); anggota + `manage_business_data` + `view_workspace` + `finance.basic` 0.1.0 aktif + entitlement versi eksak (`paymentOpenTx`, `payments.go:696-751`).
- Prasyarat: draft ada; fakta harga ada; currency sama dengan currency fakta.
- Request: header `Idempotency-Key` (keyspace proposal-pembayaran); body JSON ketat `{amount_minor: string kanonis > 0, currency: 3 huruf A-Z, method: cash | transfer, reference?: string verbatim 1-128 rune non-whitespace atau null}`.
- Respons: 201 proposal DTO (`status: "pending"`); 400 body/kunci tidak valid; 403 guard; 404 `draft tidak ditemukan`; 409 `harga disepakati belum ada` / `mata uang tidak cocok` / `kunci idempotency sudah dipakai untuk permintaan yang berbeda`.
- Replay: kunci sama + draft sama + payload ternormalisasi sama → 200 proposal yang sama, tanpa tulis/audit baru; kunci sama isi/target berbeda → 409. Plafon TIDAK dicek di tahap ini (usulan inert, tidak mereservasi saldo).

### 3.2 Setujui pembayaran

- Endpoint: POST `/workspaces/{id}/rental/contracts/{draft_id}/payment-proposals/{proposal_id}/approvals` (`main.go:157`, `payments.go:917`).
- Aktor dan izin: penyetuju (subject dari token) + guard tulis yang sama; HARUS berbeda dari pengusul → 403 `penyetuju harus berbeda dari pengusul` (identitas dari token, bukan body; ditegakkan server + CHECK DB).
- Prasyarat: proposal ada pada draft ini; belum pernah disetujui; amount ≤ outstanding SAAT INI (dihitung ulang di bawah lock, numeric SQL).
- Request: header `Idempotency-Key` (keyspace approval-pembayaran, TERPISAH dari keyspace proposal); body HARUS tepat `{}` (field apa pun → 400).
- Respons: 201 payment DTO (`reversed: false`); 400 body tidak valid; 403 self-approval/guard; 404 `proposal tidak ditemukan`; 409 `proposal sudah disetujui` / `pembayaran melebihi sisa` / kunci dipakai untuk permintaan berbeda.
- Replay: kunci sama + draft + proposal sama → 200 fakta posted itu; berbeda → 409. Jumlah disalin verbatim dari usulan, bukan dari body approval.

### 3.3 Usulkan pembalikan

- Endpoint: POST `/workspaces/{id}/rental/contracts/{draft_id}/payments/{payment_id}/reversal-proposals` (`main.go:158`, `payments.go:1082`).
- Aktor dan izin: pengusul; guard tulis yang sama.
- Prasyarat: pembayaran ada pada draft ini; belum dibatalkan oleh fakta posted.
- Request: header `Idempotency-Key` (keyspace proposal-pembalikan); body JSON ketat `{reason: wajib, di-trim 1-500 rune}`.
- Respons: 201 reversal-proposal DTO (`status: "pending"`); 400 `alasan tidak valid`; 404 `pembayaran tidak ditemukan`; 409 `pembayaran sudah dibatalkan` / kunci dipakai untuk permintaan berbeda.
- Replay: kunci sama + draft + pembayaran + reason (trimmed) sama → 200; berbeda → 409. Beberapa usulan pending untuk pembayaran yang sama DIIZINKAN (hanya fakta posted yang menghalangi).

### 3.4 Setujui pembalikan

- Endpoint: POST `/workspaces/{id}/rental/contracts/{draft_id}/payments/{payment_id}/reversal-proposals/{proposal_id}/approvals` (`main.go:159`, `payments.go:1214`).
- Aktor dan izin: penyetuju + guard tulis yang sama; HARUS berbeda dari pengusul → 403.
- Prasyarat: usulan pembalikan ada pada draft + pembayaran ini; pembayaran belum dibatalkan fakta lain.
- Request: header `Idempotency-Key` (keyspace approval-pembalikan); body HARUS tepat `{}`.
- Respons: 201 reversal DTO; 400 body tidak valid; 403 self-approval/guard; 404 `proposal pembalikan tidak ditemukan`; 409 `pembayaran sudah dibatalkan` / kunci dipakai untuk permintaan berbeda.
- Replay: kunci sama + draft + pembayaran + usulan sama → 200 fakta pembalikan; berbeda → 409. Jumlah penuh diturunkan dari pembayaran immutable (tidak ada jumlah baru dari klien).

### 3.5 Baca riwayat per draf

- Endpoint: GET `/workspaces/{id}/rental/contracts/{draft_id}/payments` (`main.go:160`, `payments.go:1353`).
- Aktor dan izin: pembaca; anggota + `view_workspace` SAJA — tanpa `financeGuard`, tanpa `manage_business_data`; tetap 200 sesudah deactivate/revoke.
- Prasyarat: draft ada; fakta harga ada.
- Request: tanpa body, tanpa query.
- Respons: 200 `paymentsHistoryDTO`: `rental_contract_id`, `rental_agreed_price_id`, `currency`, `total_minor` + `paid_minor` + `outstanding_minor` (semua string desimal kanonis), `payments[]` (urut `approved_at`, `id`; tiap entri membawa `reversed` + `reversal_id`), `reversals[]`, `pending_payment_proposals[]`, `pending_reversal_proposals[]`; 404 draft tidak ditemukan; 409 fakta belum ada; 500 `total tidak konsisten`.
- Replay: bukan aksi tulis — tidak ada kunci. Satu snapshot REPEATABLE READ untuk seluruh respons.

### 3.6 Baca ringkasan workspace (DITUNDA — lihat §4)

- Endpoint: GET `/workspaces/{id}/finance/summary` (`main.go:163`, `payments.go:1548`).
- Aktor dan izin: pembaca; anggota + `view_workspace` SAJA (tanpa gerbang finance; tetap 200 sesudah deactivate/revoke).
- Prasyarat: tidak ada (tanpa fakta harga → `currencies: []`).
- Request: tanpa query apa pun (query → 400 `parameter query tidak didukung`).
- Respons: 200 `financeSummaryDTO`: `workspace_id`, `observed_at` (satu sampel jam DB — BUKAN ID snapshot, watermark, atau token rekonstruksi historis), `currencies[]` (`currency`, `total_minor`/`paid_minor`/`outstanding_minor` string, `payments_count`, `reversals_count`, `pending_payment_proposals_count`, `pending_reversal_proposals_count`); 500 data korup.
- Replay: bukan aksi tulis. Satu snapshot; tanpa DML/audit/cache/materialisasi.

### 3.7 Apa yang bisa dibuktikan GET untuk setiap aksi

GET per-draf membuktikan: usulan tercatat (ada di `pending_payment_proposals[]` dengan `client_request_id` + status turunan `"pending"`); pembayaran tercatat (ada di `payments[]` dengan `proposal_id` + `reversed: false`); pembalikan tercatat (`payments[]` dengan `reversed: true` + `reversal_id`, dan entri di `reversals[]`); plafon saat ini (`total_minor`/`paid_minor`/`outstanding_minor` konsisten). Yang TIDAK bisa dibuktikan GET: apakah satu POST spesifik yang hasilnya tak-diketahui sudah commit — hanya replay kunci yang sama yang menjawabnya (200 = fakta/usulan itu; 409 = kunci dipakai permintaan berbeda). `observed_at` ringkasan bukan watermark: tidak bisa merekonstruksi keadaan historis.

## 4. Rekomendasi: F2-UI-07 pembayaran rental (termasuk pembalikan; ringkasan DITUNDA)

Satu halaman sibling baru `/workspaces/[id]/rental-payments/[draftId]` ("Pembayaran"), mengikuti pola F2-UI-05/06: rute sibling (rentals.vue tidak punya outlet NuxtPage), tautan per-baris "Pembayaran" di riwayat draf, server otoritatif, snapshot/kunci idempotency di memori, pemulihan hasil-tak-diketahui lewat GET.

- Pembalikan WAJIB disertakan (D3 diamandemen, kontrak F2-02-A §1/§12): memisah approval pembayaran dari approval pembalikan membuat pembayaran posted tidak dapat dikoreksi — penundaan kontrol yang diwajibkan, dilarang. Slice pembayaran tanpa pembalikan tidak dapat dipakai dengan aman: satu salah catat nominal/metode tidak punya jalur koreksi selain menunggu slice berikutnya. Karena itu 4 tulis + 1 baca per-draf adalah minimum yang aman, bukan terlalu besar.
- Ringkasan keuangan DITUNDA ke slice berikut (kandidat F2-UI-08): (a) alur pembayaran per-draf sudah lengkap tanpa ringkasan — GET per-draf sudah memberi total/paid/outstanding yang dibutuhkan untuk mengusulkan dan menyetujui; (b) ringkasan adalah workspace-level lintas draft — butuh permukaan sendiri (halaman workspace baru + keputusan navigasi), bukan bagian dari halaman per-draf; menempelkannya di halaman per-draf menyesatkan (total seluruh workspace di halaman satu draft); (c) menunda menjaga slice ini di kelas ukuran UI-06 (16 path) dan tidak mengikat keputusan navigasi keuangan sebelum alur intinya terbukti. Alternatif ditolak: gabung ringkasan ke slice ini (+1 bentuk proxy, +1 halaman/section, +2-3 tes E2E) — diterima hanya bila Leader menginginkan satu permukaan keuangan sekaligus.

## 5. Dependensi dan API

Tersedia dan beku — tidak ada backend/migrasi baru yang dibutuhkan:

- Empat tulis + GET per-draf (§3.1-3.5). Kurang: 5 bentuk proxy (§6) + halaman + tes. Tidak ada endpoint yang kurang.
- Fakta harga konteks: GET `.../agreed-price` (pola UI-05); draf: GET `.../rental/contracts` (50-baris, pola UI-04).
- Entitlement/aktivasi finance: di luar UI (CLI operator + aktivasi owner); UI hanya menampilkan 403 server apa adanya.
- Yang juga belum ada di UI tetapi BUKAN rekomendasi ini: ringkasan keuangan (§4, ditunda), audit UI (GET `.../audit`, baca-saja), modul stok/inventaris (tidak ada manifest maupun endpoint), pajak (tidak ada backend).

## 6. Allowlist yang diusulkan (16 file, cermin UI-06)

BARU: `apps/web/pages/workspaces/[id]/rental-payments/[draftId].vue`; `design/screenshots/f2-ui-07/` (9 PNG + manifest).
UBAH: `apps/web/server/api/[...path].ts` (5 bentuk: GET payments, POST payment-proposals, POST payment-proposals/{uuid}/approvals, POST payments/{uuid}/reversal-proposals, POST payments/{uuid}/reversal-proposals/{uuid}/approvals — semuanya tanpa query); `apps/web/lib/api.ts` (satu alternatif safe-continuation untuk rute sibling, kedua UUID divalidasi); `apps/web/pages/workspaces/[id]/rentals.vue` (satu tautan 44px "Pembayaran" per baris riwayat + komentar); `apps/web/e2e/owner-flow.spec.ts`; `apps/web/e2e/proxy-session.spec.ts`.
Tanpa backend/migrasi/auth/RLS/CLI, CSS/header/layout bersama, dependency/lockfile, workflow, atau perubahan timezone Playwright. Lima direktori screenshot lama dipertahankan byte-per-byte.

## 7. Perilaku utama

- Masuk dari tautan "Pembayaran" tiap baris riwayat draf (tanpa N+1: tautan tanpa syarat; halaman yang memutuskan dari bacaan — pola "Lihat harga"). Tujuh tautan workspace tetap + `Kembali ke Draf sewa`; bukan destinasi workspace kedelapan. Deep-link/login continuation mendarat di halaman pembayaran aktual.
- Keadaan halaman dari tiga bacaan independen (draft, fakta harga, riwayat pembayaran), masing-masing loading/hasil/galat + stale-ownership guard: tanpa fakta harga → semua tulis diblokir dengan penjelasan ("Harga belum dicatat"); fakta ada → panel usulan pembayaran + riwayat; usulan pending → panel setujui (tombol setujui dinonaktifkan untuk pengusul — bandingkan `proposed_by` dengan `session.subjectId`, yang tersedia dari login dan sama dengan identitas backend; server tetap otoritatif lewat 403); pembayaran posted belum dibatalkan → panel usulan pembalikan; pembayaran dibatalkan → baris bertanda "Dibatalkan" + alasan + penyetuju pembalikan, tanpa aksi lanjut.
- Usulan pembayaran: nominal sebagai string unit eksak (input angka → string kanonis, tanpa Number/parseFloat/toFixed/konversi rupiah; tampil `{amount_minor} · {currency}`), metode cash/transfer, referensi opsional → POST dengan kunci baru + snapshot immutable. Tanpa cek plafon di klien (server yang memutuskan di approval; outstanding hanya ditampilkan dari GET).
- Setujui pembayaran: tombol "Setujui" pada usulan pending → panel konfirmasi eksplisit (nominal + metode + pengusul) → POST `{}` dengan kunci baru. Tanpa jumlah dari klien (server menyalin dari usulan).
- Usulan pembalikan: pilih pembayaran posted → alasan wajib (1-500 rune, di-trim) → POST dengan kunci baru. Pembalikan penuh saja; tanpa jumlah, tanpa parsial.
- Setujui pembalikan: tombol pada usulan pembalikan pending → konfirmasi (alasan + pembayaran target) → POST `{}` dengan kunci baru.
- Idempotensi/pemulihan (pakai ulang mesin UI-05 R1 / UI-06): satu `crypto.randomUUID` + snapshot immutable per upaya; kunci mentah tidak di-log/persist; validasi 201 DAN 200 sebelum klaim tercatat; hasil tak-diketahui membekukan snapshot + aksi biasa; refresh GET-only (GET payments) tanpa mengganti snapshot; klasifikasi settlement 3-arah (kunci sama + semua field cocok → konfirmasi; kunci berbeda → riwayat independen, bukan sukses upaya ini; kunci sama tetapi tidak cocok → bukan keduanya, snapshot + ketidakpastian dipertahankan + galat settlement); pagari POST tertunda bila GET menyelesaikan (`postGen++`); gerbang tulis pertama mensyaratkan bacaan selesai.
- Guard (pola D-UI-3): tulis hanya owner/manager (`manage_business_data`); cashier boleh baca (GET payments 200), POST 403 = hasil izin (data terbaca dipertahankan, sesi TIDAK dibersihkan); 401 = bersihkan sesi + redirect via safe continuation. 404 draf vs 409 fakta-belum-ada dibedakan. 409 spesifik (fakta belum ada, currency tidak cocok, melebihi sisa, sudah disetujui/dibatalkan, kunci dipakai untuk permintaan berbeda) ditampilkan apa adanya, tanpa fiksi. 403 finance (`finance belum aktif di workspace ini` / `finance belum menjadi hak workspace ini`) ditampilkan apa adanya dengan penjelasan: minta operator grant + owner aktivasi — UI tidak menawarkan grant.
- Tampilan: stempel server UTC apa adanya (netral, tanpa konversi zona); nominal tampil sebagai string unit tersimpan + currency, tanpa konversi; tanpa hitung outstanding di klien (server yang menghitung; outstanding hanya ditampilkan dari GET). Copy jujur (F2-02-C §5): "pembayaran posted aktif", "jumlah harga disepakati (plafon acuan internal)", "sisa plafon acuan", "pembatalan catatan (bukan pengembalian uang)". Dilarang menyebut pendapatan, laba, utang hukum, tagihan, settlement bank, atau pajak.
- Screenshot (9 PNG + manifest): `01-payments-360`, `02-payments-768`, `03-payments-1440` (riwayat + panel usulan), `04-payments-nofact-360` (tanpa fakta, tulis diblokir), `05-payments-noperm-360` (penolakan baca), `06-payments-unknown-360` (tak-diketahui injeksi, berlabel), `07-payments-loading-360`, `08-payments-error-360`, `09-payments-reversed-360` (baris dibatalkan + alasan).

## 8. Kegagalan/recovery dan batas scope

- Usulan 409 (fakta belum ada / currency tidak cocok / kunci dipakai berbeda) → pesan server, tanpa tulis, tanpa klaim. Approval 409 (melebihi sisa / sudah disetujui) → pesan server; usulan tetap pending (tidak dihapus) — staf dapat mengusulkan ulang dengan nominal baru (kunci baru) bila perlu.
- Approval pembalikan 409 (sudah dibatalkan fakta lain) → usulan pending lain tetap inert; tidak ada aksi.
- Hasil tak-diketahui (500/502/jaringan) → panel tak-diketahui + `Kirim lagi (permintaan sama)` (kunci sama) dan `Mulai baru` yang diberi peringatan (baca ulang state, kunci baru hanya sesudah GET memastikan tidak ada fakta/usulan cocok). Tanpa retry otomatis / loop kunci baru.
- Workspace satu aktor: usulan bisa dibuat tetapi tidak pernah bisa disetujui (server 403 self-approval) — UI menampilkan penjelasan itu pada usulan milik sendiri, bukan menyembunyikan masalahnya. Tidak ada pengecualian self-approval.
- BUKAN bagian slice: ringkasan keuangan (§4), audit UI, koreksi fakta harga, pembatalan draft, pajak/deposit/denda, refund uang keluar, gateway/settlement, ledger/jurnal, ekspor, perubahan status draf (draft tetap draft; pembayaran tidak memutasi draft/harga/fakta).

## 9. Acceptance tests (sesuai risiko)

- Bahagia: draf+harga → usulkan pembayaran 201 → GET pending → aktor kedua setujui 201 → GET `payments[]` + paid/outstanding berubah; audit `rental_payment_proposed` + `rental_payment_posted` masing-masing 1.
- Pembalikan: usulkan pembalikan 201 → aktor kedua setujui 201 → GET `payments[]` `reversed: true` + `reversal_id`, `reversals[]` 1 entri, paid/outstanding kembali; audit `rental_payment_reversal_proposed` + `rental_payment_reversal_posted` masing-masing 1.
- Penolakan: cashier tulis → 403 (data dipertahankan); tanpa fakta → usulan 409; currency tidak cocok → 409; amount melebihi sisa di approval → 409; self-approval → 403 tanpa fakta/audit; replay kunci sama → 200 tanpa audit ganda; kunci berbeda pada usulan sama → 409.
- Satu aktor: propose 201, approve 403 (gagal tertutup).
- Revoke/deactivate: GET tetap 200 sesudah deactivate/revoke; POST baru → 403; finance belum diaktivasi → 403 `finance belum aktif di workspace ini`.
- Ketidakpastian (cermin R1 UI-05/UI-06): fakta kunci-sama tak cocok tidak dipromosikan; GET menyelesaikan upaya memagari POST lama; tulis pertama diblokir selama bacaan ditahan; tanpa POST saat panel diblokir.
- Screenshot: 1 tes mencakup 9 PNG + manifest (cermin UI-05/UI-06). Suite penuh tetap hijau (167 + tes baru); tanpa perubahan backend/migrasi/auth/RLS/CLI.

## 10. Pertanyaan keputusan yang benar-benar belum terjawab

1. Bekukan rute sibling `/workspaces/[id]/rental-payments/[draftId]` + judul "Pembayaran"? (Diperlukan; pola UI-05/06.)
2. Ringkasan keuangan ditunda ke slice berikut (direkomendasikan §4) vs digabung ke slice ini (+1 bentuk proxy, +1 halaman, +2-3 tes E2E)? (Diperlukan untuk scope.)
3. Tautan "Pembayaran" tanpa syarat di tiap baris riwayat (halaman yang memutuskan) vs hanya baris dengan fakta harga (butuh baca fakta per baris = N+1, pola yang dilarang)? Diusulkan tanpa syarat.
4. Copy 403 finance: tampilkan pesan server + penjelasan grant/aktivasi (diusulkan) vs pesan server saja? (Diperlukan; memengaruhi teks.)
5. GET payments tanpa paginasi (satu GET penuh) — terima untuk slice ini? Riwayat per draft diperkirakan kecil; bila satu draft punya ratusan pembayaran, respons tetap satu GET tanpa limit. Alternatif (endpoint paginasi backend) di luar slice UI. (Perlu keputusan.)
6. Tombol setujui pada usulan milik sendiri: nonaktifkan + penjelasan (diusulkan) vs sembunyikan? Perbandingan `proposed_by` dengan `session.subjectId` terverifikasi tersedia (`login.vue:50`, `auth.go:186`); server tetap otoritatif lewat 403. (Diperlukan; memengaruhi perilaku.)
7. Audit UI: tidak ada endpoint audit di proxy; riwayat pembayaran tidak menampilkan audit — terima untuk slice ini? (Diperlukan hanya bila Leader ingin jejak audit terlihat di UI.)

## 11. Label bukti

VERIFIED (kode/migrasi pada bd1d35b): 6 endpoint + guard tulis + 4 keyspace + DTO + GET per-draf + ringkasan + proxy 29 bentuk + 0 halaman/tautan pembayaran + `session.subjectId` dari login + 24 tes backend + Go suite 199/252 dan E2E 167/167 pada CI run 44867779294. REVIEWER-REPORTED: penerimaan F2-02-A (`8010e84`), F2-02-B (`39d9413`), F2-02-C (`82bfaab`) dari riwayat. PROPOSED: rute, judul, perilaku halaman, allowlist, tes §9, jawaban §10. NOT RUN: implementasi + CI slice ini. Respons injeksi/race terkontrol di tes UI mendatang adalah bukti UI, bukan bukti DB/RLS baru.

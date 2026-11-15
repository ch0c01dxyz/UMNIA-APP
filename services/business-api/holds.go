package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Handler hold rental F2-01-C. Hold adalah penahanan eksklusif satu
// unit fisik untuk periode satu draft yang sudah ada: bukan harga,
// perjanjian, handover, pembayaran, atau rental operasional. Hold
// dibuat di dalam handler (guard terpasang di sini), dengan workspace
// lock B3 yang sama dengan draft; constraint EXCLUDE di database
// adalah backstop independen untuk penulis yang melewati lock.
// TTL 24 jam, dikontrol server, tanpa renewal. Status yang dibaca
// adalah status EFEKTIF: baris held yang deadline-nya sudah lewat
// pada cutoff observasi diekspos sebagai expired walau persistensi
// malasnya belum berjalan; pembacaan tidak memutasi apa pun.

// holdTTL adalah masa hidup hold sejak cutoff pembuatan (server-side,
// bukan input). Kedaluwarsa dihitung dari cutoff jam database yang
// diambil SESUDAH workspace lock, bukan dari awal transaksi.
const holdTTL = 24 * time.Hour

// captureCutoff mengambil SATU nilai clock_timestamp() database dalam
// pernyataan terpisah. clock_timestamp() mencerminkan jam aktual saat
// pernyataan berjalan — berbeda dengan now()/CURRENT_TIMESTAMP yang
// adalah transaction_timestamp (awal transaksi). Karena itu cutoff
// HARUS diambil sesudah lock didapat: permintaan yang menunggu lock
// melewati deadline harus melihat hold lama sebagai kedaluwarsa.
func captureCutoff(tx *sql.Tx) (time.Time, error) {
	var cutoff time.Time
	err := tx.QueryRow(`SELECT clock_timestamp()`).Scan(&cutoff)
	return cutoff, err
}

// effectiveHoldStatus memetakan status tersimpan ke status efektif
// pada cutoff: held yang expires_at-nya sudah lewat adalah expired.
// updated_at TIDAK ikut berubah pada pemetaan ini — expiry turunan
// waktu bukan tulis, dan pembacaan tidak memutasi baris.
func effectiveHoldStatus(raw string, expiresAt, cutoff time.Time) string {
	if raw == "held" && !expiresAt.After(cutoff) {
		return "expired"
	}
	return raw
}

type holdRow struct {
	ID              string
	WorkspaceID     string
	DraftID         string
	AssetID         string
	ClientRequestID string
	Status          string // status mentah tersimpan
	StartsAt        time.Time
	EndsAt          time.Time
	ExpiresAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type holdDTO struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	DraftID         string `json:"draft_id"`
	AssetID         string `json:"asset_id"`
	ClientRequestID string `json:"client_request_id"`
	Status          string `json:"status"`
	StartsAt        string `json:"starts_at"`
	EndsAt          string `json:"ends_at"`
	ExpiresAt       string `json:"expires_at"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// holdToDTO memakai status EFEKTIF pada cutoff; semua timestamp UTC
// RFC3339Nano supaya mikrodetik tersimpan tetap konsisten.
func holdToDTO(h holdRow, cutoff time.Time) holdDTO {
	return holdDTO{
		ID: h.ID, WorkspaceID: h.WorkspaceID, DraftID: h.DraftID, AssetID: h.AssetID,
		ClientRequestID: h.ClientRequestID,
		Status:          effectiveHoldStatus(h.Status, h.ExpiresAt, cutoff),
		StartsAt:        formatRentalTimeUTC(h.StartsAt),
		EndsAt:          formatRentalTimeUTC(h.EndsAt),
		ExpiresAt:       formatRentalTimeUTC(h.ExpiresAt),
		CreatedAt:       formatRentalTimeUTC(h.CreatedAt),
		UpdatedAt:       formatRentalTimeUTC(h.UpdatedAt),
	}
}

const holdColumns = `id, workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at, created_at, updated_at`

func scanHoldRow(row *sql.Row) (holdRow, error) {
	var h holdRow
	err := row.Scan(&h.ID, &h.WorkspaceID, &h.DraftID, &h.AssetID, &h.ClientRequestID, &h.Status,
		&h.StartsAt, &h.EndsAt, &h.ExpiresAt, &h.CreatedAt, &h.UpdatedAt)
	return h, err
}

func (s *server) handleCreateHold(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	wsID := r.PathValue("id")
	if !isValidUUID(wsID) {
		writeError(w, http.StatusBadRequest, "id workspace tidak valid")
		return
	}
	draftID := r.PathValue("draft_id")
	if !isValidUUID(draftID) {
		writeError(w, http.StatusBadRequest, "id draft tidak valid")
		return
	}
	draftID = strings.ToLower(draftID)
	// Header: tepat satu nilai Idempotency-Key yang valid (aturan
	// yang sama dengan draft); kunci mentah tidak pernah di-log.
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
		writeError(w, http.StatusBadRequest, "header Idempotency-Key tidak valid")
		return
	}
	key := keys[0]
	var body struct {
		AssetID string `json:"asset_id"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	assetID := strings.TrimSpace(body.AssetID)
	if !isValidUUID(assetID) {
		writeError(w, http.StatusBadRequest, "asset_id tidak valid")
		return
	}
	assetID = strings.ToLower(assetID)
	id, err := uuidNew()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id gagal dibuat")
		return
	}
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	allowed, err := s.hasPermission(tx, wsID, subject, "manage_business_data")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin mengelola data usaha")
		return
	}
	// Lock baris workspace yang sama dengan B3/draft, sesudah
	// otorisasi dan sebelum semua bacaan guard/state/referensi/
	// replay; dipegang sampai commit/rollback.
	if err := lockWorkspace(tx, wsID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Cutoff diambil SESUDAH lock: permintaan yang menunggu lock
	// melewati deadline harus memakai jam sesudah lock didapat.
	cutoff, err := captureCutoff(tx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Guard: instalasi rental.basic harus aktif; entitlement dicek
	// ulang tiap request untuk versi aktif yang tepat.
	inst, err := loadInstallation(tx, wsID, "rental.basic")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if inst == nil || inst.Status != "active" {
		writeError(w, http.StatusForbidden, "rental belum aktif di workspace ini")
		return
	}
	entitled, err := hasEntitlement(tx, wsID, "rental.basic", inst.Version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if !entitled {
		writeError(w, http.StatusForbidden, "rental belum menjadi hak workspace ini")
		return
	}
	// Izin baca core dicek eksplisit SEBELUM payload draft maupun
	// referensi diakses: sukses bacaan bukan bukti otorisasi.
	allowed, err = s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return
	}
	// Replay: cari hold yang sudah tercatat dengan kunci yang sama,
	// di bawah lock dan RLS normal — SEBELUM cek kelayakan referensi
	// dan SEBELUM mutasi expiry. Replay adalah bacaan atas histori
	// yang sudah commit: kunci yang sudah released/expired tetap
	// terikat pada hold aslinya dan tidak bisa membuat hold lain.
	// Bandingkan kedua field ternormalisasi (draft_id, asset_id):
	// sama → 200 hold efektif saat ini (tanpa tulis/audit/perpanjangan
	// TTL); berbeda → 409, tanpa tulis.
	existing, err := scanHoldRow(tx.QueryRow(
		`SELECT `+holdColumns+`
		 FROM rental_holds WHERE workspace_id = $1 AND client_request_id = $2`,
		wsID, key))
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		if existing.DraftID == draftID && existing.AssetID == assetID {
			writeJSON(w, http.StatusOK, holdToDTO(existing, cutoff))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	// Rujukan untuk hold BARU: draft harus ada di workspace ini;
	// hilang/lintas-tenant → 404 dengan pesan yang sama.
	var draftItemID string
	var draftStarts, draftEnds time.Time
	err = tx.QueryRow(
		`SELECT catalog_item_id, starts_at, ends_at FROM rental_contracts WHERE workspace_id = $1 AND id = $2`,
		wsID, draftID).Scan(&draftItemID, &draftStarts, &draftEnds)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "draft tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Aset harus ada di workspace ini; hilang/lintas-tenant → 404
	// dengan pesan yang sama.
	var assetItemID, assetCondition string
	err = tx.QueryRow(
		`SELECT catalog_item_id, condition FROM physical_assets WHERE workspace_id = $1 AND id = $2`,
		wsID, assetID).Scan(&assetItemID, &assetCondition)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "aset tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Item aset harus sama dengan item draft; beda → 409.
	if assetItemID != draftItemID {
		writeError(w, http.StatusConflict, "aset tidak cocok dengan draft")
		return
	}
	// Status item SAAT INI dibaca ulang untuk hold baru: item yang
	// diarsipkan sesudah draft dibuat → 409. Ini berbeda dengan
	// replay (bacaan histori), yang tidak memvalidasi ulang status.
	// FK komposit menjamin baris item ada; hilang di sini adalah
	// inkonsistensi database → 500.
	var itemStatus string
	err = tx.QueryRow(
		`SELECT status FROM catalog_items WHERE workspace_id = $1 AND id = $2`,
		wsID, draftItemID).Scan(&itemStatus)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if itemStatus != "active" {
		writeError(w, http.StatusConflict, "barang tidak aktif")
		return
	}
	// Unit yang sedang keluar (custody out terbuka) tidak bisa
	// ditahan lagi, untuk periode apa pun. Cek ini sesudah replay +
	// referensi (404 didahulukan) dan sebelum cek akhir periode.
	// Replay kunci yang sudah consumed tetap 200 di atas, bukan
	// hold baru — cek ini hanya untuk hold BARU.
	var assetOut bool
	err = tx.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM rental_custody WHERE workspace_id = $1 AND asset_id = $2 AND status = 'out')`,
		wsID, assetID).Scan(&assetOut)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if assetOut {
		writeError(w, http.StatusConflict, "unit sedang dipinjam")
		return
	}
	// Unit rusak/hilang tidak bisa ditahan lagi (kondisi dibaca
	// bersama item aset di atas). Replay kunci yang sudah ada tetap
	// 200 di atas, bukan hold baru — cek ini hanya untuk hold BARU.
	if assetCondition == "damaged" || assetCondition == "lost" {
		writeError(w, http.StatusConflict, "unit tidak tersedia")
		return
	}
	// Hold baru untuk periode draft yang sudah berakhir → 409.
	// starts_at TIDAK wajib di masa depan: periode yang sedang
	// berjalan (starts lalu, ends masih depan) tetap boleh ditahan.
	if !draftEnds.After(cutoff) {
		writeError(w, http.StatusConflict, "periode draft sudah berakhir")
		return
	}
	// Lazy expiry di bawah lock: hanya baris held milik workspace
	// ini yang expires_at-nya sudah lewat yang dibalik ke expired,
	// dengan updated_at=cutoff. Insert yang gagal (atau audit yang
	// gagal) me-rollback cleanup ini juga — tidak ada commit parsial.
	if _, err := tx.Exec(
		`UPDATE rental_holds SET status = 'expired', updated_at = $2
		 WHERE workspace_id = $1 AND status = 'held' AND expires_at <= $2`,
		wsID, cutoff); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Periode disalin dari draft tersimpan (sudah ternormalisasi UTC
	// mikrodetik); expires_at = cutoff + TTL 24 jam, immutable
	// sesudahnya; created_at/updated_at memakai cutoff yang sama.
	expiresAt := cutoff.Add(holdTTL)
	var storedStarts, storedEnds, storedExpires, created, updated time.Time
	err = tx.QueryRow(
		`INSERT INTO rental_holds (id, workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, 'held', $6, $7, $8, $9, $9) RETURNING starts_at, ends_at, expires_at, created_at, updated_at`,
		id, wsID, draftID, assetID, key, draftStarts, draftEnds, expiresAt, cutoff).Scan(&storedStarts, &storedEnds, &storedExpires, &created, &updated)
	if err != nil {
		// 409 HANYA untuk pelanggaran EXCLUDE overlap bernama
		// (SQLSTATE 23P01); galat lain → 500 tanpa bocor detail SQL
		// dan tanpa menebak jenis galat dari teks pesan.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" &&
			pgErr.ConstraintName == "rental_holds_no_overlap" {
			writeError(w, http.StatusConflict, "unit tidak tersedia pada periode itu")
			return
		}
		writeError(w, http.StatusInternalServerError, "hold gagal dibuat")
		return
	}
	details, _ := json.Marshal(map[string]string{"draft_id": draftID, "asset_id": assetID})
	if err := writeBusinessAudit(tx, wsID, subject, "hold_created", "rental_hold", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "hold gagal dibuat")
		return
	}
	writeJSON(w, http.StatusCreated, holdDTO{
		ID: id, WorkspaceID: wsID, DraftID: draftID, AssetID: assetID,
		ClientRequestID: key, Status: "held",
		StartsAt: formatRentalTimeUTC(storedStarts), EndsAt: formatRentalTimeUTC(storedEnds),
		ExpiresAt: formatRentalTimeUTC(storedExpires),
		CreatedAt: formatRentalTimeUTC(created), UpdatedAt: formatRentalTimeUTC(updated),
	})
}

func (s *server) handleReleaseHold(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	wsID := r.PathValue("id")
	if !isValidUUID(wsID) {
		writeError(w, http.StatusBadRequest, "id workspace tidak valid")
		return
	}
	holdID := r.PathValue("hold_id")
	if !isValidUUID(holdID) {
		writeError(w, http.StatusBadRequest, "id hold tidak valid")
		return
	}
	holdID = strings.ToLower(holdID)
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	allowed, err := s.hasPermission(tx, wsID, subject, "manage_business_data")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin mengelola data usaha")
		return
	}
	// Izin baca dicek eksplisit sebelum hold dibaca; release TIDAK
	// mensyaratkan instalasi/entitlement Rental aktif, supaya
	// owner/manager yang sah tetap bisa membebaskan hold sesudah
	// revoke/deactivate.
	allowed, err = s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return
	}
	if err := lockWorkspace(tx, wsID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	cutoff, err := captureCutoff(tx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Baca hold TANPA FOR UPDATE: SELECT ... FOR UPDATE juga
	// menerapkan kebijakan UPDATE (USING status='held'), sehingga
	// baris yang sudah released/expired tidak terbaca. Serialisasi
	// transisi dijamin UPDATE ... WHERE status='held' di bawah:
	// UPDATE mengunci baris; UPDATE kedua yang bersamaan melihat 0
	// baris dan mengembalikan keadaan saat ini tanpa audit.
	existing, err := scanHoldRow(tx.QueryRow(
		`SELECT `+holdColumns+`
		 FROM rental_holds WHERE workspace_id = $1 AND id = $2`,
		wsID, holdID))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "hold tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Hold yang sudah dipakai serah terima (consumed) TIDAK bisa
	// dilepas: 409 eksplisit, SEBELUM no-op terminal generik di
	// bawah (yang akan menjawab 200 untuk released/expired).
	if existing.Status == "consumed" {
		writeError(w, http.StatusConflict, "hold sudah dipakai serah terima")
		return
	}
	eff := effectiveHoldStatus(existing.Status, existing.ExpiresAt, cutoff)
	// Hold sudah released/expired (mentah): idempotent — 200
	// catatan saat ini, tanpa tulis/audit/perpanjangan TTL.
	if eff != "held" {
		// Bila kedaluwarsa baru terlihat secara efektif, persistensi
		// expiry-nya boleh disimpan dalam transaksi yang sama, tanpa
		// audit release (bukan aksi aktor).
		if existing.Status == "held" {
			res, err := tx.Exec(
				`UPDATE rental_holds SET status = 'expired', updated_at = $2
				 WHERE workspace_id = $1 AND id = $3 AND status = 'held'`,
				wsID, cutoff, holdID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "transaksi gagal")
				return
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				// Balapan: hold dilepas/dikedaluarsakan penulis lain
				// di antara baca dan UPDATE — kembalikan keadaan
				// saat ini, tanpa audit.
				current, err := scanHoldRow(tx.QueryRow(
					`SELECT `+holdColumns+`
					 FROM rental_holds WHERE workspace_id = $1 AND id = $2`,
					wsID, holdID))
				if err != nil {
					writeError(w, http.StatusInternalServerError, "transaksi gagal")
					return
				}
				writeJSON(w, http.StatusOK, holdToDTO(current, cutoff))
				return
			}
			if err := tx.Commit(); err != nil {
				writeError(w, http.StatusInternalServerError, "transaksi gagal")
				return
			}
			existing.Status = "expired"
			existing.UpdatedAt = cutoff
		}
		writeJSON(w, http.StatusOK, holdToDTO(existing, cutoff))
		return
	}
	// Hold hidup → released + audit, dalam transaksi yang sama.
	// UPDATE kondisional pada status='held': bila 0 baris, hold
	// sudah dilepas/dikedaluarsakan penulis lain — kembalikan keadaan
	// saat ini tanpa audit kedua.
	res, err := tx.Exec(
		`UPDATE rental_holds SET status = 'released', updated_at = $2
		 WHERE workspace_id = $1 AND id = $3 AND status = 'held'`,
		wsID, cutoff, holdID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		current, err := scanHoldRow(tx.QueryRow(
			`SELECT `+holdColumns+`
			 FROM rental_holds WHERE workspace_id = $1 AND id = $2`,
			wsID, holdID))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		writeJSON(w, http.StatusOK, holdToDTO(current, cutoff))
		return
	}
	details, _ := json.Marshal(map[string]string{"draft_id": existing.DraftID, "asset_id": existing.AssetID})
	if err := writeBusinessAudit(tx, wsID, subject, "hold_released", "rental_hold", holdID, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "hold gagal dilepas")
		return
	}
	existing.Status = "released"
	existing.UpdatedAt = cutoff
	writeJSON(w, http.StatusOK, holdToDTO(existing, cutoff))
}

func (s *server) handleListHolds(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	wsID := r.PathValue("id")
	if !isValidUUID(wsID) {
		writeError(w, http.StatusBadRequest, "id workspace tidak valid")
		return
	}
	limit, offset, ok := parsePagination(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "limit/offset tidak valid")
		return
	}
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	// Bacaan histori: hanya membership + view_workspace, tanpa syarat
	// aktivasi/entitlement — hold yang sudah tercatat tetap terbaca
	// sesudah deaktivasi/pencabutan.
	allowed, err := s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	// Cutoff diamati dari database supaya status efektif konsisten
	// dengan jalur hold lain; pembacaan tidak memutasi baris.
	cutoff, err := captureCutoff(tx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "daftar hold gagal dibaca")
		return
	}
	rows, err := tx.Query(
		`SELECT `+holdColumns+`
		 FROM rental_holds WHERE workspace_id = $1 ORDER BY created_at, id LIMIT $2 OFFSET $3`,
		wsID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "daftar hold gagal dibaca")
		return
	}
	defer rows.Close()
	items := []holdDTO{}
	for rows.Next() {
		var h holdRow
		if err := rows.Scan(&h.ID, &h.WorkspaceID, &h.DraftID, &h.AssetID, &h.ClientRequestID, &h.Status,
			&h.StartsAt, &h.EndsAt, &h.ExpiresAt, &h.CreatedAt, &h.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "daftar hold gagal dibaca")
			return
		}
		items = append(items, holdToDTO(h, cutoff))
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "daftar hold gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

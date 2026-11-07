package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Handler kontrak rental DRAFT F2-01-B. Draft hanya mencatat pelanggan,
// barang, dan periode yang dimaksud — tanpa stok, harga, uang, handover,
// pembayaran, ketersediaan fisik, atau lifecycle. Satu baris
// catalog_items BUKAN unit fisik (physical_asset belum ada); draft tidak
// mereservasi apa pun dan tidak membuktikan overlap aman. Guard
// (instalasi aktif + entitlement + izin) terpasang DI DALAM handler
// create, bukan helper terpisah. Bacaan list adalah histori: hanya
// membership + view_workspace, tanpa syarat aktivasi/entitlement Rental,
// supaya draft tetap terbaca sesudah deaktivasi/pencabutan.
// Setiap create menulis draft + audit append-only dalam satu transaksi,
// dan baru membalas 201 sesudah commit; replay kunci yang sama tidak
// menulis apa pun.

// validIdempotencyKey memeriksa kunci permintaan: 1-128 karakter ASCII
// [huruf, digit, -, _, ., :], tanpa whitespace/kosong. Kunci adalah
// identitas permintaan per workspace, bukan secret atau entitlement.
func validIdempotencyKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}

// normalizeInstant mengubah instant ke UTC dengan presisi mikrodetik —
// sama dengan presisi penyimpanan TIMESTAMPTZ PostgreSQL — supaya
// perbandingan periode dan perbandingan replay memakai nilai yang sama
// persis dengan yang tersimpan, bukan presisi nanodetik input.
func normalizeInstant(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// formatRentalTimeUTC memformat timestamp kontrak ke UTC dengan
// presisi mikrodetik bila ada. formatTimeUTC yang dipakai endpoint
// lain memakai RFC3339 tanpa fraksi — itu akan menghilangkan
// mikrodetik periode dan membuat respons tidak konsisten dengan nilai
// ternormalisasi yang dipakai perbandingan replay (klien yang memakai
// ulang starts_at respons akan mendapat 409). RFC3339Nano hanya
// mencetak fraksi bila bukan nol, jadi detik bulat tetap tampil sama.
func formatRentalTimeUTC(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

type rentalContractRow struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	CustomerID      string `json:"customer_id"`
	CatalogItemID   string `json:"catalog_item_id"`
	ClientRequestID string `json:"client_request_id"`
	Status          string `json:"status"`
	StartsAt        string `json:"starts_at"`
	EndsAt          string `json:"ends_at"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func (s *server) handleCreateRentalContract(w http.ResponseWriter, r *http.Request) {
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
	// Header: tepat satu nilai Idempotency-Key yang valid. Header
	// ganda (atau satu nilai berisi koma) ditolak; kunci mentah tidak
	// pernah di-log — hanya dipakai sebagai client_request_id.
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
		writeError(w, http.StatusBadRequest, "header Idempotency-Key tidak valid")
		return
	}
	key := keys[0]
	var body struct {
		CustomerID    string `json:"customer_id"`
		CatalogItemID string `json:"catalog_item_id"`
		StartsAt      string `json:"starts_at"`
		EndsAt        string `json:"ends_at"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	customerID := strings.TrimSpace(body.CustomerID)
	if !isValidUUID(customerID) {
		writeError(w, http.StatusBadRequest, "customer_id tidak valid")
		return
	}
	catalogItemID := strings.TrimSpace(body.CatalogItemID)
	if !isValidUUID(catalogItemID) {
		writeError(w, http.StatusBadRequest, "catalog_item_id tidak valid")
		return
	}
	// Tanggal wajib RFC3339 dengan zona waktu eksplisit (tanpa offset
	// → 400) dan tahun 1-9999 (dicek SEBELUM normalisasi UTC, karena
	// konversi zona bisa menggeser tahun di tepi 0001/9999).
	starts, err := time.Parse(time.RFC3339, strings.TrimSpace(body.StartsAt))
	if err != nil || starts.Year() < 1 || starts.Year() > 9999 {
		writeError(w, http.StatusBadRequest, "starts_at tidak valid")
		return
	}
	ends, err := time.Parse(time.RFC3339, strings.TrimSpace(body.EndsAt))
	if err != nil || ends.Year() < 1 || ends.Year() > 9999 {
		writeError(w, http.StatusBadRequest, "ends_at tidak valid")
		return
	}
	starts = normalizeInstant(starts)
	ends = normalizeInstant(ends)
	if !ends.After(starts) {
		writeError(w, http.StatusBadRequest, "ends_at harus sesudah starts_at")
		return
	}
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
	// Lock baris workspace yang sama dengan B3, sesudah otorisasi dan
	// sebelum semua bacaan guard/state/referensi/replay; dipegang
	// sampai commit/rollback. Mutasi modul dan grant/revoke fixture
	// memakai lock dan urutan yang sama.
	if err := lockWorkspace(tx, wsID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Guard: instalasi rental.basic harus aktif; entitlement dicek
	// ulang tiap request untuk versi aktif yang tepat — pencabutan
	// memblokir create baru walau baris instalasi masih aktif
	// (snapshot intent B3). Galat query → 500, bukan 403.
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
	// referensi core diakses: sukses bacaan bukan bukti otorisasi
	// (RLS menyaring tanpa galat). Penolakan → 403, galat query → 500.
	allowed, err = s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return
	}
	// Replay: cari draft yang sudah tercatat dengan kunci yang sama,
	// di bawah lock dan RLS normal. Replay adalah bacaan atas histori
	// yang sudah commit: status referensi TIDAK divalidasi ulang di
	// sini — barang yang belakangan diarsipkan tidak menghalangi
	// replay selama otorisasi/guard di atas masih berlaku. Bandingkan
	// keempat field ternormalisasi: sama → 200 baris asli (tanpa
	// tulis/audit/perubahan timestamp); berbeda → 409, tanpa tulis.
	var existing rentalContractRow
	var exStarts, exEnds, exCreated, exUpdated time.Time
	err = tx.QueryRow(
		`SELECT id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at, created_at, updated_at
		 FROM rental_contracts WHERE workspace_id = $1 AND client_request_id = $2`,
		wsID, key).Scan(&existing.ID, &existing.WorkspaceID, &existing.CustomerID, &existing.CatalogItemID,
		&existing.ClientRequestID, &existing.Status, &exStarts, &exEnds, &exCreated, &exUpdated)
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		if existing.CustomerID == customerID && existing.CatalogItemID == catalogItemID &&
			exStarts.Equal(starts) && exEnds.Equal(ends) {
			existing.StartsAt = formatRentalTimeUTC(exStarts)
			existing.EndsAt = formatRentalTimeUTC(exEnds)
			existing.CreatedAt = formatRentalTimeUTC(exCreated)
			existing.UpdatedAt = formatRentalTimeUTC(exUpdated)
			writeJSON(w, http.StatusOK, existing)
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	// Rujukan untuk create BARU: pelanggan harus ada di workspace ini
	// dan bertipe customer; hilang/lintas-tenant → 404 dengan pesan
	// yang sama (tanpa membocorkan keberadaan). Barang dibaca TANPA
	// filter status di WHERE supaya arsip bisa dibedakan dari hilang:
	// hilang/lintas-tenant → 404, milik sendiri tapi archived → 409.
	var found string
	err = tx.QueryRow(
		`SELECT id FROM parties WHERE workspace_id = $1 AND id = $2 AND type = 'customer'`,
		wsID, customerID).Scan(&found)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "pelanggan tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	var itemStatus string
	err = tx.QueryRow(
		`SELECT id, status FROM catalog_items WHERE workspace_id = $1 AND id = $2`,
		wsID, catalogItemID).Scan(&found, &itemStatus)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "barang tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if itemStatus != "active" {
		writeError(w, http.StatusConflict, "barang tidak aktif")
		return
	}
	var storedStarts, storedEnds, created, updated time.Time
	err = tx.QueryRow(
		`INSERT INTO rental_contracts (id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at)
		 VALUES ($1, $2, $3, $4, $5, 'draft', $6, $7) RETURNING starts_at, ends_at, created_at, updated_at`,
		id, wsID, customerID, catalogItemID, key, starts, ends).Scan(&storedStarts, &storedEnds, &created, &updated)
	if err != nil {
		// Kunci unik (workspace_id, client_request_id) adalah backstop
		// DB; dengan lock + lookup di atas, benturan tidak terjangkau
		// lewat API. Semua galat insert yang tak terduga → 500
		// terkendali tanpa bocor detail SQL, dan tanpa menebak jenis
		// galat dari teks pesannya.
		writeError(w, http.StatusInternalServerError, "kontrak gagal dibuat")
		return
	}
	details, _ := json.Marshal(map[string]string{"customer_id": customerID, "catalog_item_id": catalogItemID})
	if err := writeBusinessAudit(tx, wsID, subject, "rental_contract_created", "rental_contract", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "kontrak gagal dibuat")
		return
	}
	writeJSON(w, http.StatusCreated, rentalContractRow{
		ID: id, WorkspaceID: wsID, CustomerID: customerID, CatalogItemID: catalogItemID,
		ClientRequestID: key, Status: "draft",
		StartsAt: formatRentalTimeUTC(storedStarts), EndsAt: formatRentalTimeUTC(storedEnds),
		CreatedAt: formatRentalTimeUTC(created), UpdatedAt: formatRentalTimeUTC(updated),
	})
}

func (s *server) handleListRentalContracts(w http.ResponseWriter, r *http.Request) {
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
	// Bacaan histori: hanya membership + view_workspace. Sengaja
	// TIDAK memeriksa instalasi/entitlement Rental — draft yang sudah
	// tercatat tetap terbaca sesudah deaktivasi/pencabutan.
	allowed, err := s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	rows, err := tx.Query(
		`SELECT id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at, created_at, updated_at
		 FROM rental_contracts WHERE workspace_id = $1 ORDER BY created_at, id LIMIT $2 OFFSET $3`,
		wsID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "daftar kontrak gagal dibaca")
		return
	}
	defer rows.Close()
	items := []rentalContractRow{}
	for rows.Next() {
		var c rentalContractRow
		var starts, ends, created, updated time.Time
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.CustomerID, &c.CatalogItemID, &c.ClientRequestID, &c.Status,
			&starts, &ends, &created, &updated); err != nil {
			writeError(w, http.StatusInternalServerError, "daftar kontrak gagal dibaca")
			return
		}
		c.StartsAt = formatRentalTimeUTC(starts)
		c.EndsAt = formatRentalTimeUTC(ends)
		c.CreatedAt = formatRentalTimeUTC(created)
		c.UpdatedAt = formatRentalTimeUTC(updated)
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "daftar kontrak gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

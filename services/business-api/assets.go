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

// Handler aset fisik F2-01-C. Satu baris physical_assets adalah satu
// unit fisik yang teridentifikasi (serial) dari satu catalog item —
// BUKAN stok kuantitas dan BUKAN harga. Registrasi aset adalah master
// data core (seperti pelanggan/katalog): izin manage_business_data,
// tanpa syarat aktivasi/entitlement modul. Ketersediaan diturunkan
// dari hold yang aktif; tidak ada kolom status pada aset di slice ini.

type assetRow struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	CatalogItemID string `json:"catalog_item_id"`
	Serial        string `json:"serial"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func (s *server) handleCreateAsset(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		CatalogItemID string `json:"catalog_item_id"`
		Serial        string `json:"serial"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	catalogItemID := strings.TrimSpace(body.CatalogItemID)
	if !isValidUUID(catalogItemID) {
		writeError(w, http.StatusBadRequest, "catalog_item_id tidak valid")
		return
	}
	// UUID divalidasi dengan sintaks hyphenated kanonis; PostgreSQL
	// mengembalikan UUID dalam huruf kecil — kanonikalisasi sebelum
	// dipakai, seperti pada create draft (F2-01-B review fix).
	catalogItemID = strings.ToLower(catalogItemID)
	serial := strings.TrimSpace(body.Serial)
	if serial == "" || len([]rune(serial)) > 64 {
		writeError(w, http.StatusBadRequest, "serial tidak valid")
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
	// Izin baca core dicek eksplisit SEBELUM bacaan item: sukses
	// bacaan bukan bukti otorisasi (RLS menyaring tanpa galat).
	allowed, err = s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return
	}
	// Barang dibaca TANPA filter status di WHERE supaya arsip bisa
	// dibedakan dari hilang: hilang/lintas-tenant → 404, milik
	// sendiri tapi archived → 409 (aturan yang sama dengan draft).
	var found string
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
	var created, updated time.Time
	err = tx.QueryRow(
		`INSERT INTO physical_assets (id, workspace_id, catalog_item_id, serial)
		 VALUES ($1, $2, $3, $4) RETURNING created_at, updated_at`,
		id, wsID, catalogItemID, serial).Scan(&created, &updated)
	if err != nil {
		// 409 HANYA untuk duplikat serial asli (SQLSTATE 23505 pada
		// constraint serial); galat lain → 500 tanpa bocor detail SQL
		// dan tanpa menebak jenis galat dari teks pesan.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
			pgErr.ConstraintName == "physical_assets_workspace_id_serial_key" {
			writeError(w, http.StatusConflict, "serial sudah dipakai")
			return
		}
		writeError(w, http.StatusInternalServerError, "aset gagal dibuat")
		return
	}
	details, _ := json.Marshal(map[string]string{"catalog_item_id": catalogItemID})
	if err := writeBusinessAudit(tx, wsID, subject, "asset_registered", "physical_asset", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "aset gagal dibuat")
		return
	}
	writeJSON(w, http.StatusCreated, assetRow{
		ID: id, WorkspaceID: wsID, CatalogItemID: catalogItemID, Serial: serial,
		CreatedAt: formatRentalTimeUTC(created), UpdatedAt: formatRentalTimeUTC(updated),
	})
}

func (s *server) handleListAssets(w http.ResponseWriter, r *http.Request) {
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
	// Bacaan histori/core: hanya membership + view_workspace, tanpa
	// syarat aktivasi/entitlement.
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
		`SELECT id, workspace_id, catalog_item_id, serial, created_at, updated_at
		 FROM physical_assets WHERE workspace_id = $1 ORDER BY created_at, id LIMIT $2 OFFSET $3`,
		wsID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "daftar aset gagal dibaca")
		return
	}
	defer rows.Close()
	items := []assetRow{}
	for rows.Next() {
		var a assetRow
		var created, updated time.Time
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.CatalogItemID, &a.Serial, &created, &updated); err != nil {
			writeError(w, http.StatusInternalServerError, "daftar aset gagal dibaca")
			return
		}
		a.CreatedAt = formatRentalTimeUTC(created)
		a.UpdatedAt = formatRentalTimeUTC(updated)
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "daftar aset gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *server) handleGetAssetAvailability(w http.ResponseWriter, r *http.Request) {
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
	assetID := r.PathValue("asset_id")
	if !isValidUUID(assetID) {
		writeError(w, http.StatusBadRequest, "id aset tidak valid")
		return
	}
	assetID = strings.ToLower(assetID)
	// Validasi tanggal sama dengan draft: RFC3339 dengan zona
	// eksplisit, tahun input DAN tahun ternormalisasi UTC 1-9999,
	// normalisasi mikrodetik UTC, ends>starts sesudah normalisasi.
	starts, err := time.Parse(time.RFC3339, strings.TrimSpace(r.URL.Query().Get("starts_at")))
	if err != nil || starts.Year() < 1 || starts.Year() > 9999 {
		writeError(w, http.StatusBadRequest, "starts_at tidak valid")
		return
	}
	ends, err := time.Parse(time.RFC3339, strings.TrimSpace(r.URL.Query().Get("ends_at")))
	if err != nil || ends.Year() < 1 || ends.Year() > 9999 {
		writeError(w, http.StatusBadRequest, "ends_at tidak valid")
		return
	}
	starts = normalizeInstant(starts)
	ends = normalizeInstant(ends)
	if starts.Year() < 1 || starts.Year() > 9999 {
		writeError(w, http.StatusBadRequest, "starts_at tidak valid")
		return
	}
	if ends.Year() < 1 || ends.Year() > 9999 {
		writeError(w, http.StatusBadRequest, "ends_at tidak valid")
		return
	}
	if !ends.After(starts) {
		writeError(w, http.StatusBadRequest, "ends_at harus sesudah starts_at")
		return
	}
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	allowed, err := s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	// Cutoff diamati dari database (jam DB, bukan jam aplikasi),
	// supaya batas expiry sama dengan yang dipakai jalur hold.
	var cutoff time.Time
	if err := tx.QueryRow(`SELECT clock_timestamp()`).Scan(&cutoff); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	var found string
	err = tx.QueryRow(
		`SELECT id FROM physical_assets WHERE workspace_id = $1 AND id = $2`,
		wsID, assetID).Scan(&found)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "aset tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Advisory: hanya hold HIDUP yang belum kedaluwarsa pada cutoff
	// yang memblokir. Hasil ini bukan jaminan reservasi (TOCTOU) —
	// hanya create hold yang menjamin.
	var blocked bool
	err = tx.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM rental_holds
		 WHERE workspace_id = $1 AND asset_id = $2 AND status = 'held' AND expires_at > $3
		   AND period && tstzrange($4, $5, '[)'))`,
		wsID, assetID, cutoff, starts, ends).Scan(&blocked)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ketersediaan gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"asset_id":  assetID,
		"starts_at": formatRentalTimeUTC(starts),
		"ends_at":   formatRentalTimeUTC(ends),
		"available": !blocked,
	})
}

package main

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

// Handler master data core F2-01-A (catalog items). Trusted core: selalu
// tersedia bagi anggota workspace berizin, tanpa instalasi/entitlement
// modul. Hanya create + list; tanpa update/delete/archive endpoint,
// tanpa stok/harga/konversi. API membuat baris aktif dengan revision 0.
// SKU unik per workspace (case-sensitive, sesudah trim); duplikat → 409.
// Keunikan SKU BUKAN idempotensi HTTP penuh, dan tidak membuat create
// customer menjadi idempotent.

type catalogItemRow struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	SKU         string `json:"sku"`
	Name        string `json:"name"`
	Unit        string `json:"unit"`
	Status      string `json:"status"`
	Revision    int64  `json:"revision"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func (s *server) handleCreateCatalogItem(w http.ResponseWriter, r *http.Request) {
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
		SKU  string `json:"sku"`
		Name string `json:"name"`
		Unit string `json:"unit"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	sku := strings.TrimSpace(body.SKU)
	name := strings.TrimSpace(body.Name)
	unit := strings.TrimSpace(body.Unit)
	if sku == "" || utf8.RuneCountInString(sku) > 64 ||
		name == "" || utf8.RuneCountInString(name) > 200 ||
		unit == "" || utf8.RuneCountInString(unit) > 32 {
		writeError(w, http.StatusBadRequest, "sku, name, dan unit wajib diisi (sku maks 64, name maks 200, unit maks 32 karakter)")
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
	var created, updated time.Time
	err = tx.QueryRow(
		`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, $3, $4, $5, 'active') RETURNING created_at, updated_at`,
		id, wsID, sku, name, unit).Scan(&created, &updated)
	if err != nil {
		// 409 HANYA untuk pelanggaran unik nyata pada constraint
		// (workspace_id, sku): kenali lewat SQLSTATE 23505 + nama
		// constraint, bukan lewat teks pesan galat. Pesan backend lain
		// — termasuk yang kebetulan mengandung kata "unique" atau
		// "duplicate key" (mis. P0001 dari trigger) — tetap 500
		// terkendali tanpa bocor detail SQL.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "catalog_items_workspace_id_sku_key" {
			writeError(w, http.StatusConflict, "sku sudah dipakai di workspace ini")
			return
		}
		writeError(w, http.StatusInternalServerError, "barang gagal dibuat")
		return
	}
	if err := writeBusinessAudit(tx, wsID, subject, "catalog_item_created", "catalog_item", id, `{"type":"catalog_item"}`); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "barang gagal dibuat")
		return
	}
	writeJSON(w, http.StatusCreated, catalogItemRow{
		ID: id, WorkspaceID: wsID, SKU: sku, Name: name, Unit: unit,
		Status: "active", Revision: 0,
		CreatedAt: formatTimeUTC(created), UpdatedAt: formatTimeUTC(updated),
	})
}

func (s *server) handleListCatalogItems(w http.ResponseWriter, r *http.Request) {
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
		`SELECT id, workspace_id, sku, name, unit, status, revision, created_at, updated_at
		 FROM catalog_items WHERE workspace_id = $1 ORDER BY created_at, id LIMIT $2 OFFSET $3`,
		wsID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "daftar barang gagal dibaca")
		return
	}
	defer rows.Close()
	items := []catalogItemRow{}
	for rows.Next() {
		var c catalogItemRow
		var created, updated time.Time
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.SKU, &c.Name, &c.Unit, &c.Status, &c.Revision, &created, &updated); err != nil {
			writeError(w, http.StatusInternalServerError, "daftar barang gagal dibaca")
			return
		}
		c.CreatedAt = formatTimeUTC(created)
		c.UpdatedAt = formatTimeUTC(updated)
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "daftar barang gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

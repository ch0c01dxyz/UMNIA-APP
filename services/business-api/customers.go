package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Handler master data core F2-01-A (customers). Trusted core: selalu
// tersedia bagi anggota workspace berizin, tanpa instalasi/entitlement
// modul. Hanya create + list; tanpa update/delete, tanpa supplier,
// tanpa kolom kontak (PII yang tidak dibutuhkan tidak dikumpulkan).
// Setiap create menulis baris + audit append-only dalam satu transaksi,
// dan baru membalas 201 sesudah commit.

// maxMasterDataBodyBytes membatasi body create master data ke 16 KiB.
const maxMasterDataBodyBytes = 16 << 10

// decodeStrictJSON mendekode satu dokumen JSON dengan batas 16 KiB,
// menolak field tak dikenal dan dokumen JSON tambahan sesudahnya.
// Parser auth yang sudah ada (decodeJSON) tidak diubah; pembatasan ini
// hanya untuk endpoint master data.
func decodeStrictJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMasterDataBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("dokumen JSON tambahan")
		}
		return err
	}
	return nil
}

// parsePagination membaca limit/offset dari query: limit default 50
// (1-100), offset default 0 (bilangan bulat nonnegatif yang muat int).
// Nilai tidak valid → ok=false. Offset pagination bukan snapshot:
// baris yang disisipkan bersamaan dapat menggeser halaman.
func parsePagination(r *http.Request) (limit, offset int, ok bool) {
	q := r.URL.Query()
	limit, offset = 50, 0
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			return 0, 0, false
		}
		limit = n
	}
	if s := q.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

// writeBusinessAudit menulis satu baris audit append-only untuk create
// master data. details minimal (hanya tipe entitas); tidak pernah
// menyalin nama, body request, kontak, atau token.
func writeBusinessAudit(tx *sql.Tx, wsID, subject, action, entityType, entityID, details string) error {
	_, err := tx.Exec(
		`INSERT INTO audit_events (workspace_id, actor_subject_id, action, entity_type, entity_id, details)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		wsID, subject, action, entityType, entityID, details)
	return err
}

type customerRow struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func formatTimeUTC(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func (s *server) handleCreateCustomer(w http.ResponseWriter, r *http.Request) {
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
		DisplayName string `json:"display_name"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if name == "" || utf8.RuneCountInString(name) > 200 {
		writeError(w, http.StatusBadRequest, "display_name wajib diisi dan maksimal 200 karakter")
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
		`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', $3) RETURNING created_at, updated_at`,
		id, wsID, name).Scan(&created, &updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pelanggan gagal dibuat")
		return
	}
	if err := writeBusinessAudit(tx, wsID, subject, "customer_created", "customer", id, `{"type":"customer"}`); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "pelanggan gagal dibuat")
		return
	}
	writeJSON(w, http.StatusCreated, customerRow{
		ID: id, WorkspaceID: wsID, Type: "customer", DisplayName: name,
		CreatedAt: formatTimeUTC(created), UpdatedAt: formatTimeUTC(updated),
	})
}

func (s *server) handleListCustomers(w http.ResponseWriter, r *http.Request) {
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
		`SELECT id, workspace_id, type, display_name, created_at, updated_at
		 FROM parties WHERE workspace_id = $1 ORDER BY created_at, id LIMIT $2 OFFSET $3`,
		wsID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "daftar pelanggan gagal dibaca")
		return
	}
	defer rows.Close()
	items := []customerRow{}
	for rows.Next() {
		var c customerRow
		var created, updated time.Time
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Type, &c.DisplayName, &created, &updated); err != nil {
			writeError(w, http.StatusInternalServerError, "daftar pelanggan gagal dibaca")
			return
		}
		c.CreatedAt = formatTimeUTC(created)
		c.UpdatedAt = formatTimeUTC(updated)
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "daftar pelanggan gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

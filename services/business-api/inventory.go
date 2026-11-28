package main

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const inventoryModuleID = "inventory.basic"
const inventoryModuleVersion = "0.1.0"

const inventoryBalanceSQL = `
	SELECT i.catalog_item_id, c.sku, c.name, i.unit, COALESCE(SUM(m.delta), 0)::text
	FROM inventory_items i
	JOIN catalog_items c ON c.workspace_id = i.workspace_id AND c.id = i.catalog_item_id
	LEFT JOIN inventory_movements m ON m.workspace_id = i.workspace_id AND m.catalog_item_id = i.catalog_item_id
	WHERE i.workspace_id = $1
	GROUP BY i.catalog_item_id, c.sku, c.name, i.unit
	ORDER BY c.sku, i.catalog_item_id LIMIT $2 OFFSET $3`

type inventoryStock struct {
	ItemID   string `json:"catalog_item_id"`
	SKU      string `json:"sku"`
	Name     string `json:"name"`
	Unit     string `json:"unit"`
	Quantity string `json:"quantity"`
}

type inventoryMovement struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ItemID      string `json:"catalog_item_id"`
	Unit        string `json:"unit"`
	Kind        string `json:"kind"`
	Delta       string `json:"delta"`
	Quantity    string `json:"quantity"`
	Reference   string `json:"source_reference"`
	ReversesID  string `json:"reverses_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
	RecordedBy  string `json:"recorded_by"`
	RecordedAt  string `json:"recorded_at"`
	Reversed    bool   `json:"reversed,omitempty"`
}

type inventoryRequest struct {
	ItemID     string  `json:"catalog_item_id"`
	Kind       string  `json:"kind"`
	Quantity   *string `json:"quantity"`
	Reference  string  `json:"source_reference"`
	ReversesID string  `json:"reverses_id"`
	Reason     string  `json:"reason"`
}

const inventoryMovementColumns = `id, workspace_id, catalog_item_id, unit, kind, delta,
	source_reference, COALESCE(reverses_id::text, ''), COALESCE(reason, ''), recorded_by, recorded_at`

func scanInventoryMovement(row interface{ Scan(...any) error }) (inventoryMovement, error) {
	var m inventoryMovement
	var delta int64
	var at time.Time
	err := row.Scan(&m.ID, &m.WorkspaceID, &m.ItemID, &m.Unit, &m.Kind, &delta,
		&m.Reference, &m.ReversesID, &m.Reason, &m.RecordedBy, &at, &m.Reversed)
	m.Delta = strconv.FormatInt(delta, 10)
	m.Quantity = strings.TrimPrefix(m.Delta, "-")
	m.RecordedAt = formatRentalTimeUTC(at)
	return m, err
}

func inventoryTrackingConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "inventory_tracking_exclusive"
}

func inventoryAccess(tx *sql.Tx, w http.ResponseWriter, wsID string) bool {
	var allowed bool
	err := tx.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM module_installations i
		JOIN module_entitlements e USING (workspace_id, module_id, version)
		JOIN module_registry r USING (module_id, version)
		WHERE i.workspace_id = $1 AND i.module_id = $2 AND i.version = $3 AND i.status = 'active'
	)`, wsID, inventoryModuleID, inventoryModuleVersion).Scan(&allowed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "akses stok gagal diperiksa")
	} else if !allowed {
		writeError(w, http.StatusForbidden, "aktifkan Stok Sederhana dengan hak modul yang berlaku")
	}
	return err == nil && allowed
}

func (s *server) inventoryTx(w http.ResponseWriter, r *http.Request) (*sql.Tx, string, string) {
	if !s.requireDB(w) {
		return nil, "", ""
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return nil, "", ""
	}
	wsID := strings.ToLower(r.PathValue("id"))
	if !isValidUUID(wsID) {
		writeError(w, http.StatusBadRequest, "id workspace tidak valid")
		return nil, "", ""
	}
	var tx *sql.Tx
	var err error
	if r.Method == http.MethodGet {
		tx, err = beginTenantSnapshot(r.Context(), s.db, subject, wsID)
	} else {
		tx, err = beginTenant(s.db, subject, wsID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return nil, "", ""
	}
	allowed, err := s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil || !allowed {
		tx.Rollback()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		} else {
			writeError(w, http.StatusForbidden, "tidak punya izin melihat workspace")
		}
		return nil, "", ""
	}
	return tx, wsID, subject
}

func (s *server) handleListInventory(w http.ResponseWriter, r *http.Request) {
	tx, wsID, subject := s.inventoryTx(w, r)
	if tx == nil {
		return
	}
	defer tx.Rollback()
	limit, offset, ok := parsePagination(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "halaman tidak valid")
		return
	}
	fail := func() { writeError(w, http.StatusInternalServerError, "catatan stok gagal dibaca") }
	if strings.HasSuffix(r.URL.Path, "/stock") {
		if !inventoryAccess(tx, w, wsID) {
			return
		}
		var canPost, canReverse bool
		err := tx.QueryRow(`SELECT app.has_permission(id, $2, 'manage_business_data'), owner_subject_id = $2
			FROM workspaces WHERE id = $1`, wsID, subject).Scan(&canPost, &canReverse)
		if err != nil {
			fail()
			return
		}
		rows, err := tx.Query(inventoryBalanceSQL, wsID, limit, offset)
		if err != nil {
			fail()
			return
		}
		defer rows.Close()
		items := []inventoryStock{}
		for rows.Next() {
			var item inventoryStock
			if err := rows.Scan(&item.ItemID, &item.SKU, &item.Name, &item.Unit, &item.Quantity); err != nil {
				fail()
				return
			}
			items = append(items, item)
		}
		if rows.Err() != nil {
			fail()
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "can_post": canPost, "can_reverse": canReverse})
		return
	}
	rows, err := tx.Query(`SELECT `+inventoryMovementColumns+`,
		EXISTS(SELECT 1 FROM inventory_movements r WHERE r.workspace_id = m.workspace_id AND r.reverses_id = m.id)
		FROM inventory_movements m WHERE workspace_id = $1 ORDER BY recorded_at DESC, id DESC LIMIT $2 OFFSET $3`, wsID, limit, offset)
	if err != nil {
		fail()
		return
	}
	defer rows.Close()
	items := []inventoryMovement{}
	for rows.Next() {
		item, err := scanInventoryMovement(rows)
		if err != nil {
			fail()
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		fail()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (body *inventoryRequest) normalize() (int64, bool) {
	body.ItemID = strings.ToLower(body.ItemID)
	body.ReversesID = strings.ToLower(body.ReversesID)
	body.Reference = strings.TrimSpace(body.Reference)
	body.Reason = strings.TrimSpace(body.Reason)
	if !isValidUUID(body.ItemID) || body.Reference == "" ||
		strings.ContainsRune(body.Reference, 0) || utf8.RuneCountInString(body.Reference) > 200 {
		return 0, false
	}
	switch body.Kind {
	case "receipt", "issue":
		if body.Quantity == nil || body.ReversesID != "" || body.Reason != "" {
			return 0, false
		}
		delta, err := strconv.ParseInt(*body.Quantity, 10, 64)
		if err != nil || delta <= 0 || !isCanonicalNonNegativeInt(*body.Quantity) {
			return 0, false
		}
		if body.Kind == "issue" {
			delta = -delta
		}
		return delta, true
	case "reversal":
		return 0, body.Quantity == nil && isValidUUID(body.ReversesID) && body.Reason != "" &&
			!strings.ContainsRune(body.Reason, 0) && utf8.RuneCountInString(body.Reason) <= 500
	default:
		return 0, false
	}
}

func (s *server) handlePostInventory(w http.ResponseWriter, r *http.Request) {
	tx, wsID, subject := s.inventoryTx(w, r)
	if tx == nil {
		return
	}
	defer tx.Rollback()
	key, ok := paymentKey(w, r)
	if !ok {
		return
	}
	var body inventoryRequest
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	delta, ok := body.normalize()
	if !ok {
		writeError(w, http.StatusBadRequest, "periksa barang, jumlah bulat, referensi, atau alasan mutasi")
		return
	}
	fail := func() { writeError(w, http.StatusInternalServerError, "mutasi stok gagal diproses") }
	var allowed bool
	var err error
	if body.Kind == "reversal" {
		err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM workspaces WHERE id = $1 AND owner_subject_id = $2)`,
			wsID, subject).Scan(&allowed)
	} else {
		allowed, err = s.hasPermission(tx, wsID, subject, "manage_business_data")
	}
	if err != nil {
		fail()
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin mencatat jenis mutasi ini")
		return
	}
	if err := lockWorkspace(tx, wsID); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusForbidden, "tidak punya izin mengubah workspace")
		} else {
			fail()
		}
		return
	}
	if !inventoryAccess(tx, w, wsID) {
		return
	}

	previous, err := scanInventoryMovement(tx.QueryRow(`SELECT `+inventoryMovementColumns+`, false FROM inventory_movements
		WHERE workspace_id = $1 AND client_request_id = $2`, wsID, key))
	if err == nil {
		if previous.ItemID != body.ItemID || previous.Kind != body.Kind || previous.Reference != body.Reference ||
			previous.ReversesID != body.ReversesID || previous.Reason != body.Reason ||
			(body.Quantity != nil && previous.Quantity != *body.Quantity) || previous.RecordedBy != subject {
			writeError(w, http.StatusConflict, "kunci pengiriman sudah dipakai untuk mutasi lain")
			return
		}
		writeJSON(w, http.StatusOK, previous)
		return
	}
	if err != sql.ErrNoRows {
		fail()
		return
	}

	var catalogUnit, status string
	err = tx.QueryRow(`SELECT unit, status FROM catalog_items WHERE workspace_id = $1 AND id = $2`,
		wsID, body.ItemID).Scan(&catalogUnit, &status)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "barang tidak ditemukan")
		return
	}
	if err != nil {
		fail()
		return
	}
	if status != "active" && body.Kind != "reversal" {
		writeError(w, http.StatusConflict, "barang tidak aktif")
		return
	}
	var unit string
	err = tx.QueryRow(`SELECT unit FROM inventory_items WHERE workspace_id = $1 AND catalog_item_id = $2`,
		wsID, body.ItemID).Scan(&unit)
	if err == sql.ErrNoRows && body.Kind == "receipt" {
		unit = catalogUnit
		_, err = tx.Exec(`INSERT INTO inventory_items (workspace_id, catalog_item_id, unit) VALUES ($1, $2, $3)`, wsID, body.ItemID, unit)
	}
	if err == sql.ErrNoRows {
		writeError(w, http.StatusConflict, "barang belum terdaftar sebagai stok")
		return
	}
	if inventoryTrackingConflict(err) {
		writeError(w, http.StatusConflict, "barang sudah dilacak sebagai aset berserial")
		return
	}
	if err != nil {
		fail()
		return
	}
	if body.Kind == "reversal" {
		var reversed bool
		err = tx.QueryRow(`SELECT -m.delta, EXISTS(SELECT 1 FROM inventory_movements r
			WHERE r.workspace_id = $1 AND r.reverses_id = m.id)
			FROM inventory_movements m WHERE m.workspace_id = $1 AND m.catalog_item_id = $2 AND m.id = $3
			AND m.kind IN ('receipt', 'issue')`, wsID, body.ItemID, body.ReversesID).Scan(&delta, &reversed)
		if err == sql.ErrNoRows || reversed {
			writeError(w, http.StatusConflict, "mutasi asal tidak dapat dibalik")
			return
		}
		if err != nil {
			fail()
			return
		}
	}
	var balance int64
	err = tx.QueryRow(`SELECT COALESCE(SUM(delta), 0)::bigint FROM inventory_movements
		WHERE workspace_id = $1 AND catalog_item_id = $2`, wsID, body.ItemID).Scan(&balance)
	if err != nil || balance < 0 {
		fail()
		return
	}
	if (delta > 0 && balance > math.MaxInt64-delta) || (delta < 0 && balance < -delta) {
		writeError(w, http.StatusConflict, "mutasi melebihi batas saldo stok")
		return
	}
	movement, err := scanInventoryMovement(tx.QueryRow(`INSERT INTO inventory_movements
		(workspace_id, catalog_item_id, unit, kind, delta, source_reference, reverses_id, reason, client_request_id, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid, NULLIF($8, ''), $9, $10)
		RETURNING `+inventoryMovementColumns+`, false`, wsID, body.ItemID, unit, body.Kind, delta, body.Reference, body.ReversesID, body.Reason, key, subject))
	if err != nil {
		fail()
		return
	}
	if err := writeBusinessAudit(tx, wsID, subject, "inventory_movement_posted", "inventory_movement", movement.ID, "{}"); err != nil {
		fail()
		return
	}
	if err := tx.Commit(); err != nil {
		fail()
		return
	}
	writeJSON(w, http.StatusCreated, movement)
}

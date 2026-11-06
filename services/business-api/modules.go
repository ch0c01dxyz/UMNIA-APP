package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Handler katalog modul F1-04A (READ-ONLY). Mengembalikan katalog global
// (empat manifest contoh sebagai deklarasi) kepada anggota workspace yang
// punya izin view_workspace. Katalog bukan bukti modul bisa dipakai:
// tidak ada status aktif/entitled, tidak ada aktivasi, tidak ada
// kapabilitas yang bisa dieksekusi lewat endpoint ini.
func (s *server) handleListModules(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !isValidUUID(id) {
		writeError(w, http.StatusBadRequest, "id workspace tidak valid")
		return
	}
	tx, err := beginTenant(s.db, subject, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	// Kasir punya view_workspace, jadi boleh membaca katalog (tindakan
	// ini tidak memberi kuasa baru); izin lain tidak dicek di sini.
	allowed, err := s.hasPermission(tx, id, subject, "view_workspace")
	if err != nil {
		// Kesalahan query izin adalah kesalahan backend, bukan
		// penolakan izin: balas 500 terkendali tanpa detail SQL.
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	// Registry global tanpa RLS; satu-satunya penjaga adalah izin di
	// atas. Query ini read-only di dalam transaksi tenant request ini.
	rows, err := tx.Query(`SELECT manifest FROM module_registry ORDER BY module_id, version`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "katalog modul gagal dibaca")
		return
	}
	defer rows.Close()
	modules := []json.RawMessage{}
	for rows.Next() {
		var m json.RawMessage
		if err := rows.Scan(&m); err != nil {
			writeError(w, http.StatusInternalServerError, "katalog modul gagal dibaca")
			return
		}
		modules = append(modules, m)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "katalog modul gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": modules})
}

// Handler aktivasi/deaktivasi/state modul F1-04B-B3 (metadata-only).
// Mencatat maksud instalasi yang berhak (entitled intent), BUKAN bukti
// layanan modul berjalan/sehat atau kemampuan bisa dieksekusi. Tidak ada
// endpoint bisnis, guard, atau otoritas pemberian entitlement di sini.

// moduleStateResponse adalah bentuk respons state instalasi (B3).
// Version/ActivatedAt/DeactivatedAt null bila modul belum pernah
// diinstal di workspace ini.
type moduleStateResponse struct {
	ModuleID      string  `json:"module_id"`
	Version       *string `json:"version"`
	Status        string  `json:"status"`
	ActivatedAt   *string `json:"activated_at"`
	DeactivatedAt *string `json:"deactivated_at"`
}

// moduleManifest adalah subset field manifest yang dibaca resolver:
// provides, requires (capability + optional), conflicts, exclusive_group.
type moduleManifest struct {
	ID             string            `json:"id"`
	Version        string            `json:"version"`
	Provides       []string          `json:"provides"`
	Requires       []manifestRequire `json:"requires"`
	Conflicts      []string          `json:"conflicts"`
	ExclusiveGroup *string           `json:"exclusive_group"`
}

type manifestRequire struct {
	Capability string `json:"capability"`
	Optional   bool   `json:"optional"`
}

// installationRow adalah baris module_installations untuk satu modul.
type installationRow struct {
	Version       string
	Status        string
	ActivatedAt   sql.NullTime
	DeactivatedAt sql.NullTime
}

// activeModule adalah modul aktif di workspace beserta manifest dan
// apakah versi aktifnya berhak (entitled) — penyedia yang eligible.
type activeModule struct {
	ModuleID string
	Version  string
	Manifest moduleManifest
	Entitled bool
}

// moduleAuditDetails adalah isi details audit aktivasi/deaktivasi.
// OldStatus null pada aktivasi pertama (belum ada state sebelumnya).
type moduleAuditDetails struct {
	ModuleID  string  `json:"module_id"`
	Version   string  `json:"version"`
	OldStatus *string `json:"old_status"`
	NewStatus string  `json:"new_status"`
}

func formatNullTime(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	// Selalu UTC (Z) supaya respons mutasi (waktu Go UTC) dan respons
	// baca (nilai TIMESTAMPTZ dari DB) memakai format yang sama.
	s := t.Time.UTC().Format(time.RFC3339)
	return &s
}

func stateResponse(moduleID string, inst *installationRow) moduleStateResponse {
	if inst == nil {
		return moduleStateResponse{ModuleID: moduleID, Version: nil, Status: "inactive", ActivatedAt: nil, DeactivatedAt: nil}
	}
	return moduleStateResponse{
		ModuleID: moduleID, Version: &inst.Version, Status: inst.Status,
		ActivatedAt: formatNullTime(inst.ActivatedAt), DeactivatedAt: formatNullTime(inst.DeactivatedAt),
	}
}

// lockWorkspace mengunci baris workspace untuk serialisasi mutasi modul
// di workspace ini. Dipanggil sesudah otorisasi berhasil, supaya
// pemanggil tak berizin tidak pernah memegang lock. ErrNoRows berarti
// baris tidak terlihat (workspace tak dikenal/bukan anggota) — pemanggil
// membalas 403 tanpa membocorkan keberadaan workspace.
func lockWorkspace(tx *sql.Tx, wsID string) error {
	var id string
	return tx.QueryRow(`SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, wsID).Scan(&id)
}

func loadManifest(tx *sql.Tx, moduleID, version string) (moduleManifest, bool, error) {
	var raw []byte
	err := tx.QueryRow(`SELECT manifest FROM module_registry WHERE module_id = $1 AND version = $2`, moduleID, version).Scan(&raw)
	if err == sql.ErrNoRows {
		return moduleManifest{}, false, nil
	}
	if err != nil {
		return moduleManifest{}, false, err
	}
	var m moduleManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return moduleManifest{}, false, err
	}
	return m, true, nil
}

func moduleKnown(tx *sql.Tx, moduleID string) (bool, error) {
	var known bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM module_registry WHERE module_id = $1)`, moduleID).Scan(&known)
	return known, err
}

func loadInstallation(tx *sql.Tx, wsID, moduleID string) (*installationRow, error) {
	var inst installationRow
	err := tx.QueryRow(
		`SELECT version, status, activated_at, deactivated_at FROM module_installations WHERE workspace_id = $1 AND module_id = $2`,
		wsID, moduleID).Scan(&inst.Version, &inst.Status, &inst.ActivatedAt, &inst.DeactivatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

// loadActiveModules mengembalikan modul aktif di workspace beserta
// manifest dan status entitlement versi aktifnya, terurut module_id
// (supaya pelaporan resolver deterministik).
func loadActiveModules(tx *sql.Tx, wsID string) ([]activeModule, error) {
	rows, err := tx.Query(
		`SELECT i.module_id, i.version, r.manifest,
			EXISTS (SELECT 1 FROM module_entitlements e WHERE e.workspace_id = i.workspace_id AND e.module_id = i.module_id AND e.version = i.version)
		 FROM module_installations i
		 JOIN module_registry r ON r.module_id = i.module_id AND r.version = i.version
		 WHERE i.workspace_id = $1 AND i.status = 'active'
		 ORDER BY i.module_id`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []activeModule
	for rows.Next() {
		var m activeModule
		var raw []byte
		if err := rows.Scan(&m.ModuleID, &m.Version, &raw, &m.Entitled); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &m.Manifest); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func hasEntitlement(tx *sql.Tx, wsID, moduleID, version string) (bool, error) {
	var ok bool
	err := tx.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM module_entitlements WHERE workspace_id = $1 AND module_id = $2 AND version = $3)`,
		wsID, moduleID, version).Scan(&ok)
	return ok, err
}

// missingRequirement mengembalikan kapabilitas non-optional pertama yang
// tidak dipenuhi penyedia eligible (aktif + entitled), atau "" bila
// semua terpenuhi. Requires optional tidak pernah memblokir. Urutan
// mengikuti array requires di manifest (deterministik).
func missingRequirement(candidate moduleManifest, providers []activeModule) string {
	for _, req := range candidate.Requires {
		if req.Optional {
			continue
		}
		found := false
		for _, p := range providers {
			if !p.Entitled {
				continue
			}
			for _, cap := range p.Manifest.Provides {
				if cap == req.Capability {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return req.Capability
		}
	}
	return ""
}

// conflictingModule mengembalikan modul aktif yang bentrok dengan
// kandidat (kedua arah daftar conflicts), atau "". Daftar conflicts
// kandidat dicek dulu (sesuai urutan manifest), lalu arah sebaliknya
// (modul aktif terurut module_id).
func conflictingModule(candidate moduleManifest, active []activeModule) string {
	activeByID := map[string]bool{}
	for _, m := range active {
		activeByID[m.ModuleID] = true
	}
	for _, other := range candidate.Conflicts {
		if activeByID[other] {
			return other
		}
	}
	for _, m := range active {
		for _, c := range m.Manifest.Conflicts {
			if c == candidate.ID {
				return m.ModuleID
			}
		}
	}
	return ""
}

// exclusiveConflict mengembalikan modul aktif lain dalam exclusive_group
// yang sama (non-null), atau "".
func exclusiveConflict(candidate moduleManifest, active []activeModule) string {
	if candidate.ExclusiveGroup == nil {
		return ""
	}
	for _, m := range active {
		if m.Manifest.ExclusiveGroup != nil && *m.Manifest.ExclusiveGroup == *candidate.ExclusiveGroup {
			return m.ModuleID
		}
	}
	return ""
}

// strandedRequirement memeriksa apakah menonaktifkan `removing` membuat
// dependent aktif kehilangan satu-satunya penyedia eligible untuk suatu
// kapabilitas non-optional. Mengembalikan (dependent, capability)
// pertama yang terdampar, atau ("",""). Dependent dinilai dari status
// aktifnya saja; penyedia pengganti harus aktif + entitled dan bukan
// modul yang dinonaktifkan.
func strandedRequirement(removing string, removingManifest moduleManifest, active []activeModule) (string, string) {
	provides := map[string]bool{}
	for _, cap := range removingManifest.Provides {
		provides[cap] = true
	}
	for _, d := range active {
		if d.ModuleID == removing {
			continue
		}
		for _, req := range d.Manifest.Requires {
			if req.Optional || !provides[req.Capability] {
				continue
			}
			remaining := false
			for _, p := range active {
				if p.ModuleID == removing || !p.Entitled {
					continue
				}
				for _, cap := range p.Manifest.Provides {
					if cap == req.Capability {
						remaining = true
						break
					}
				}
				if remaining {
					break
				}
			}
			if !remaining {
				return d.ModuleID, req.Capability
			}
		}
	}
	return "", ""
}

func writeModuleAudit(tx *sql.Tx, wsID, subject, action string, details moduleAuditDetails) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO audit_events (workspace_id, actor_subject_id, action, entity_type, entity_id, details)
		 VALUES ($1, $2, $3, 'module_installation', NULL, $4::jsonb)`,
		wsID, subject, action, string(raw))
	return err
}

func (s *server) handleActivateModule(w http.ResponseWriter, r *http.Request) {
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
	moduleID := r.PathValue("module_id")
	if moduleID == "" {
		writeError(w, http.StatusNotFound, "modul tidak dikenal")
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Version) == "" {
		writeError(w, http.StatusBadRequest, "versi modul wajib diisi")
		return
	}
	version := strings.TrimSpace(body.Version)
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	allowed, err := s.hasPermission(tx, wsID, subject, "manage_modules")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin mengelola modul")
		return
	}
	if err := lockWorkspace(tx, wsID); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		} else {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
		}
		return
	}
	// Manifest versi yang diminta: modul tak dikenal 404, versi tak
	// terdaftar 404 — keduanya sesudah otorisasi workspace.
	manifest, found, err := loadManifest(tx, moduleID, version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if !found {
		known, err := moduleKnown(tx, moduleID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		if !known {
			writeError(w, http.StatusNotFound, "modul tidak dikenal")
		} else {
			writeError(w, http.StatusNotFound, "versi modul tidak terdaftar")
		}
		return
	}
	inst, err := loadInstallation(tx, wsID, moduleID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if inst != nil {
		// Sudah aktif dengan versi yang sama: no-op 200 — tanpa cek
		// entitlement, tanpa audit, tanpa perubahan timestamp. Ini
		// mengembalikan snapshot maksud yang ada; bukan bukti hak
		// efektif (entitlement mungkin sudah dicabut).
		if inst.Status == "active" && inst.Version == version {
			writeJSON(w, http.StatusOK, stateResponse(moduleID, inst))
			return
		}
		// Versi pin berbeda: 409, walau sedang inactive.
		if inst.Version != version {
			writeError(w, http.StatusConflict, "versi berbeda: gunakan alur upgrade")
			return
		}
	}
	// Perubahan state nyata: entitlement dulu (403), baru dependensi/
	// konflik (409), lalu upsert + audit dalam transaksi yang sama.
	entitled, err := hasEntitlement(tx, wsID, moduleID, version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if !entitled {
		writeError(w, http.StatusForbidden, "modul ini belum menjadi hak workspace ini")
		return
	}
	active, err := loadActiveModules(tx, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if missing := missingRequirement(manifest, active); missing != "" {
		writeError(w, http.StatusConflict, "kapabilitas "+missing+" belum tersedia di workspace ini")
		return
	}
	if other := conflictingModule(manifest, active); other != "" {
		writeError(w, http.StatusConflict, "modul "+moduleID+" bentrok dengan "+other+" yang sedang aktif")
		return
	}
	if other := exclusiveConflict(manifest, active); other != "" {
		writeError(w, http.StatusConflict, "modul "+moduleID+" bentrok dengan "+other+" yang sedang aktif")
		return
	}
	now := time.Now().UTC()
	var oldStatus *string
	if inst == nil {
		if _, err := tx.Exec(
			`INSERT INTO module_installations (workspace_id, module_id, version, status, activated_at, activated_by)
			 VALUES ($1, $2, $3, 'active', $4, $5)`,
			wsID, moduleID, version, now, subject); err != nil {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
	} else {
		if _, err := tx.Exec(
			`UPDATE module_installations SET status = 'active', activated_at = $3, activated_by = $4, deactivated_at = NULL, updated_at = $3
			 WHERE workspace_id = $1 AND module_id = $2`,
			wsID, moduleID, now, subject); err != nil {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		inactive := "inactive"
		oldStatus = &inactive
	}
	if err := writeModuleAudit(tx, wsID, subject, "module_activated", moduleAuditDetails{
		ModuleID: moduleID, Version: version, OldStatus: oldStatus, NewStatus: "active",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	activated := now.Format(time.RFC3339)
	writeJSON(w, http.StatusOK, moduleStateResponse{
		ModuleID: moduleID, Version: &version, Status: "active",
		ActivatedAt: &activated, DeactivatedAt: nil,
	})
}

func (s *server) handleDeactivateModule(w http.ResponseWriter, r *http.Request) {
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
	moduleID := r.PathValue("module_id")
	if moduleID == "" {
		writeError(w, http.StatusNotFound, "modul tidak dikenal")
		return
	}
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	allowed, err := s.hasPermission(tx, wsID, subject, "manage_modules")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin mengelola modul")
		return
	}
	if err := lockWorkspace(tx, wsID); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		} else {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
		}
		return
	}
	// Modul tak dikenal 404; modul dikenal tanpa baris (atau sudah
	// inactive) adalah no-op 200 tanpa baris/audit baru.
	known, err := moduleKnown(tx, moduleID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if !known {
		writeError(w, http.StatusNotFound, "modul tidak dikenal")
		return
	}
	inst, err := loadInstallation(tx, wsID, moduleID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if inst == nil || inst.Status == "inactive" {
		writeJSON(w, http.StatusOK, stateResponse(moduleID, inst))
		return
	}
	// Aktif: nonaktifkan hanya bila tidak membuat dependent aktif
	// kehilangan satu-satunya penyedia eligible. Deaktivasi tidak
	// butuh entitlement (owner tetap bisa mematikan modul sesudah hak
	// dicabut).
	active, err := loadActiveModules(tx, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	manifest, found, err := loadManifest(tx, moduleID, inst.Version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if !found {
		// Tidak mungkin lewat FK registry, tapi jangan lanjut tanpa
		// manifest penyedia.
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if dependent, cap := strandedRequirement(moduleID, manifest, active); dependent != "" {
		writeError(w, http.StatusConflict, "modul "+dependent+" masih membutuhkan "+cap+" dari modul ini")
		return
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(
		`UPDATE module_installations SET status = 'inactive', deactivated_at = $3, updated_at = $3
		 WHERE workspace_id = $1 AND module_id = $2`,
		wsID, moduleID, now); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	old := "active"
	if err := writeModuleAudit(tx, wsID, subject, "module_deactivated", moduleAuditDetails{
		ModuleID: moduleID, Version: inst.Version, OldStatus: &old, NewStatus: "inactive",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	deactivated := now.Format(time.RFC3339)
	writeJSON(w, http.StatusOK, moduleStateResponse{
		ModuleID: moduleID, Version: &inst.Version, Status: "inactive",
		ActivatedAt: formatNullTime(inst.ActivatedAt), DeactivatedAt: &deactivated,
	})
}

func (s *server) handleGetModuleState(w http.ResponseWriter, r *http.Request) {
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
	moduleID := r.PathValue("module_id")
	if moduleID == "" {
		writeError(w, http.StatusNotFound, "modul tidak dikenal")
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
	known, err := moduleKnown(tx, moduleID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if !known {
		writeError(w, http.StatusNotFound, "modul tidak dikenal")
		return
	}
	inst, err := loadInstallation(tx, wsID, moduleID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	writeJSON(w, http.StatusOK, stateResponse(moduleID, inst))
}

// isValidUUID memeriksa bentuk UUID kanonis (8-4-4-4-12 heksadesimal)
// tanpa dependensi eksternal; dipakai untuk menolak input malformed
// sebelum menyentuhnya ke SQL.
func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

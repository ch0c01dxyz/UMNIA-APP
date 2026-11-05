package main

import (
	"database/sql"
	"net/http"
	"strings"
	"time"
)

// Handler workspace/undangan/audit F1-03. Semua query berjalan dalam
// transaksi tenant (beginTenant) sehingga RLS PostgreSQL ikut menegakkan
// isolasi, bukan hanya filter WHERE di aplikasi.

func (s *server) authedSubject(w http.ResponseWriter, r *http.Request) (string, bool) {
	subject, ok := subjectFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "token tidak ada")
		return "", false
	}
	return subject, true
}

func (s *server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "nama workspace wajib diisi")
		return
	}
	id, err := uuidNew()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id gagal dibuat")
		return
	}
	tx, err := beginTenant(s.db, subject, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO workspaces (id, name, owner_subject_id) VALUES ($1, $2, $3)`, id, body.Name, subject); err != nil {
		writeError(w, http.StatusInternalServerError, "workspace gagal dibuat")
		return
	}
	if _, err := tx.Exec(`INSERT INTO workspace_memberships (workspace_id, subject_id, status) VALUES ($1, $2, 'active')`, id, subject); err != nil {
		writeError(w, http.StatusInternalServerError, "membership gagal dibuat")
		return
	}
	if _, err := tx.Exec(`INSERT INTO membership_roles (workspace_id, subject_id, role) VALUES ($1, $2, 'owner')`, id, subject); err != nil {
		writeError(w, http.StatusInternalServerError, "role gagal dibuat")
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO audit_events (workspace_id, actor_subject_id, action, entity_type, entity_id) VALUES ($1, $2, 'workspace_created', 'workspace', $3)`,
		id, subject, id); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "workspace gagal dibuat")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "name": body.Name})
}

func (s *server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	tx, err := beginTenant(s.db, subject, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	var name, owner string
	err = tx.QueryRow(`SELECT name, owner_subject_id FROM workspaces WHERE id = $1`, id).Scan(&name, &owner)
	if err != nil {
		// RLS mengembalikan 0 baris untuk non-anggota; jangan bocorkan
		// apakah workspace itu ada: balas 403, bukan 404.
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "name": name, "owner_subject_id": owner})
}

func (s *server) hasPermission(tx *sql.Tx, workspace, subject, perm string) (bool, error) {
	var ok bool
	err := tx.QueryRow(`SELECT app.has_permission($1, $2, $3)`, workspace, subject, perm).Scan(&ok)
	return ok, err
}

func (s *server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decodeJSON(r, &body); err != nil || validateEmail(body.Email) != nil || body.Role == "" {
		writeError(w, http.StatusBadRequest, "email dan role wajib valid")
		return
	}
	token, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token gagal dibuat")
		return
	}
	tx, err := beginTenant(s.db, subject, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	allowed, err := s.hasPermission(tx, id, subject, "invite_members")
	if err != nil || !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin mengundang")
		return
	}
	var inviteID string
	err = tx.QueryRow(
		`INSERT INTO staff_invites (workspace_id, email, role, token_hash, invited_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		id, body.Email, body.Role, hashToken(token), subject, time.Now().Add(72*time.Hour)).Scan(&inviteID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "undangan gagal dibuat (periksa role)")
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO audit_events (workspace_id, actor_subject_id, action, entity_type, entity_id) VALUES ($1, $2, 'invite_created', 'staff_invite', $3)`,
		id, subject, inviteID); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "undangan gagal dibuat")
		return
	}
	// Token dikembalikan sekali ke pengundang (F1-03 belum punya mailer;
	// di produksi token harus dikirim lewat email, bukan respons API).
	writeJSON(w, http.StatusCreated, map[string]string{
		"id": inviteID, "token": token, "email": body.Email, "role": body.Role,
	})
}

func (s *server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	var body struct {
		WorkspaceID string `json:"workspace_id"`
		Token       string `json:"token"`
	}
	if err := decodeJSON(r, &body); err != nil || body.WorkspaceID == "" || body.Token == "" {
		writeError(w, http.StatusBadRequest, "workspace_id dan token wajib diisi")
		return
	}
	var role string
	err := s.db.QueryRow(`SELECT app.accept_invite($1, $2, $3)`, body.WorkspaceID, subject, hashToken(body.Token)).Scan(&role)
	if err != nil {
		if strings.Contains(err.Error(), "does not match") {
			writeError(w, http.StatusForbidden, "undangan ini bukan untuk akun ini")
			return
		}
		writeError(w, http.StatusConflict, "undangan tidak valid, kedaluwarsa, atau sudah dipakai")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"workspace_id": body.WorkspaceID, "role": role})
}

type memberRow struct {
	SubjectID string `json:"subject_id"`
	Email     string `json:"email"`
	Status    string `json:"status"`
	Role      string `json:"role"`
}

func (s *server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	tx, err := beginTenant(s.db, subject, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	// Kasir hanya punya view_workspace, bukan view_members: daftar anggota
	// wajib ditolak 403, bukan sekadar disaring RLS (RLS saja mengizinkan
	// anggota mana pun membaca membership workspace-nya sendiri).
	allowed, err := s.hasPermission(tx, id, subject, "view_members")
	if err != nil || !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin melihat anggota")
		return
	}
	rows, err := tx.Query(
		`SELECT m.subject_id, s.email, m.status, COALESCE(mr.role, '')
		 FROM workspace_memberships m
		 JOIN identity_subjects s ON s.id = m.subject_id
		 LEFT JOIN membership_roles mr ON mr.workspace_id = m.workspace_id AND mr.subject_id = m.subject_id
		 WHERE m.workspace_id = $1 ORDER BY s.email`, id)
	if err != nil {
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	defer rows.Close()
	members := []memberRow{}
	for rows.Next() {
		var m memberRow
		if err := rows.Scan(&m.SubjectID, &m.Email, &m.Status, &m.Role); err != nil {
			writeError(w, http.StatusInternalServerError, "daftar anggota gagal dibaca")
			return
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "daftar anggota gagal dibaca")
		return
	}
	if len(members) == 0 {
		// RLS mengembalikan kosong untuk non-anggota; balas 403.
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

type auditRow struct {
	ID        string `json:"id"`
	Actor     string `json:"actor_subject_id"`
	Action    string `json:"action"`
	Entity    string `json:"entity_type"`
	CreatedAt string `json:"created_at"`
}

func (s *server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	tx, err := beginTenant(s.db, subject, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	allowed, err := s.hasPermission(tx, id, subject, "view_audit")
	if err != nil || !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin melihat audit")
		return
	}
	rows, err := tx.Query(
		`SELECT id, actor_subject_id, action, entity_type, created_at
		 FROM audit_events WHERE workspace_id = $1 ORDER BY created_at`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal dibaca")
		return
	}
	defer rows.Close()
	events := []auditRow{}
	for rows.Next() {
		var e auditRow
		var created time.Time
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Entity, &created); err != nil {
			writeError(w, http.StatusInternalServerError, "audit gagal dibaca")
			return
		}
		e.CreatedAt = created.Format(time.RFC3339)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

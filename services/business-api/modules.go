package main

import (
	"encoding/json"
	"net/http"
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
	if err != nil || !allowed {
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

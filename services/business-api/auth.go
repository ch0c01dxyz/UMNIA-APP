package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type server struct {
	db *sql.DB
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// hashToken menyimpan hash SHA-256 token, bukan token mentahnya, supaya
// kebocoran database tidak langsung memberikan sesi/undangan yang berlaku.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func validateEmail(email string) error {
	if strings.Contains(email, " ") || strings.Count(email, "@") != 1 {
		return errors.New("email tidak valid")
	}
	local, domain, _ := strings.Cut(email, "@")
	if local == "" || domain == "" || !strings.Contains(domain, ".") {
		return errors.New("email tidak valid")
	}
	return nil
}

func hashPasswordCheck(password string) error {
	if len(password) < 8 {
		return errors.New("password minimal 8 karakter")
	}
	return nil
}

func hashPassword(password string) (string, error) {
	if err := hashPasswordCheck(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func verifyPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

func (s *server) requireDB(w http.ResponseWriter) bool {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database tidak dikonfigurasi")
		return false
	}
	return true
}

// authMiddleware menukar Bearer token menjadi subject_id lewat fungsi
// SECURITY DEFINER app.session_subject (melewati RLS, karena sebelum
// autentikasi belum ada subjek aktif). Token kedaluwarsa → 401.
func (s *server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireDB(w) {
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			writeError(w, http.StatusUnauthorized, "token tidak ada")
			return
		}
		var subject string
		err := s.db.QueryRow(`SELECT app.session_subject($1)`, hashToken(token)).Scan(&subject)
		if err != nil || subject == "" {
			writeError(w, http.StatusUnauthorized, "token tidak valid atau kedaluwarsa")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxSubject, subject)))
	}
}

func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	if err := validateEmail(body.Email); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Registrasi lewat fungsi SECURITY DEFINER: INSERT langsung ditolak
	// RLS yang di-FORCE (lihat migrasi 002).
	var id string
	err = s.db.QueryRow(`SELECT app.register_subject($1, $2)`, body.Email, hash).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "email sudah terdaftar")
			return
		}
		writeError(w, http.StatusInternalServerError, "registrasi gagal")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "email": body.Email})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	var subject, hash string
	err := s.db.QueryRow(`SELECT id, password_hash FROM app.subject_by_email($1)`, body.Email).Scan(&subject, &hash)
	if err != nil || verifyPassword(hash, body.Password) != nil {
		// Pesan sama untuk email tidak ada maupun password salah.
		writeError(w, http.StatusUnauthorized, "email atau password salah")
		return
	}
	token, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token gagal dibuat")
		return
	}
	tx, err := beginTenant(s.db, subject, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sesi gagal dibuat")
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO sessions (subject_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		subject, hashToken(token), time.Now().Add(24*time.Hour)); err != nil {
		writeError(w, http.StatusInternalServerError, "sesi gagal dibuat")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "sesi gagal dibuat")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token, "subject_id": subject})
}

// Package main adalah business API UMNIA (F1-01 skeleton + F1-02 database
// + F1-03 identity/workspace).
//
// Readiness memeriksa PostgreSQL bisnis bila BUSINESS_DATABASE_URL terisi;
// tanpa URL itu, database dilaporkan "not_configured" (bukan bukti database
// siap). Endpoint auth/workspace mengembalikan 503 bila db nil.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

type readinessResponse struct {
	Status  string            `json:"status"`
	Service string            `json:"service"`
	Checks  map[string]string `json:"checks"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Service: "business-api"})
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Service: "business-api"})
}

// databaseStatus mem-ping PostgreSQL bisnis bila URL-nya dikonfigurasi.
// Ping dibatasi 2 detik supaya readiness tidak menggantung pada DB mati.
// Ping saja tidak cukup: database kosong tanpa skema harus dilaporkan
// "error" (degraded), bukan "ok" — jadi readiness juga memeriksa migrasi
// identity F1-03 sudah terapan.
func databaseStatus() string {
	url := os.Getenv("BUSINESS_DATABASE_URL")
	if url == "" {
		return "not_configured"
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		return "error"
	}
	defer db.Close()
	return checkDatabase(db)
}

func checkDatabase(db *sql.DB) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return "error"
	}
	var one int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM schema_migrations WHERE version = '002_identity'`).Scan(&one)
	if err != nil {
		return "error"
	}
	return "ok"
}

func handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dbStatus := databaseStatus()
	status := "ok"
	code := http.StatusOK
	if dbStatus == "error" {
		// DB dikonfigurasi tapi tidak terjangkau: belum siap melayani.
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, readinessResponse{
		Status:  status,
		Service: "business-api",
		Checks:  map[string]string{"http": "ok", "database": dbStatus},
	})
}

// newMux dipakai main dan tes, supaya tes menguji handler yang sama
// dengan yang benar-benar dijalankan server. db boleh nil (untuk tes
// health/readiness); handler auth/workspace membalas 503 dalam kasus itu.
func newMux(db *sql.DB) *http.ServeMux {
	srv := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleRoot)
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/readyz", handleReadyz)
	mux.HandleFunc("POST /auth/register", srv.handleRegister)
	mux.HandleFunc("POST /auth/login", srv.handleLogin)
	mux.HandleFunc("POST /workspaces", srv.authMiddleware(srv.handleCreateWorkspace))
	mux.HandleFunc("GET /workspaces/{id}", srv.authMiddleware(srv.handleGetWorkspace))
	mux.HandleFunc("GET /workspaces/{id}/members", srv.authMiddleware(srv.handleListMembers))
	mux.HandleFunc("POST /workspaces/{id}/invites", srv.authMiddleware(srv.handleCreateInvite))
	mux.HandleFunc("POST /invites/accept", srv.authMiddleware(srv.handleAcceptInvite))
	mux.HandleFunc("GET /workspaces/{id}/audit", srv.authMiddleware(srv.handleListAudit))
	return mux
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := envOr("HTTP_LISTEN_ADDR", ":8080")
	// Koneksi dibuka tanpa ping: server tetap menyala bila DB mati, dan
	// /readyz yang melaporkan status database (pola F1-02).
	var db *sql.DB
	if url := os.Getenv("BUSINESS_DATABASE_URL"); url != "" {
		var err error
		db, err = sql.Open("pgx", url)
		if err != nil {
			log.Fatalf("open database: %v", err)
		}
		defer db.Close()
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(db),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("business-api listening on %s (env=%s)", addr, envOr("APP_ENV", "development"))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// Package main adalah business API UMNIA (F1-01 skeleton + F1-02 database).
//
// Belum ada auth, workspace, maupun logika bisnis — semua itu masuk di
// F1-03 dan seterusnya. Readiness memeriksa PostgreSQL bisnis bila
// BUSINESS_DATABASE_URL terisi; tanpa URL itu, database dilaporkan
// "not_configured" (bukan bukti database siap).
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
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
// dengan yang benar-benar dijalankan server.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleRoot)
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/readyz", handleReadyz)
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
	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("business-api listening on %s (env=%s)", addr, envOr("APP_ENV", "development"))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// Package main adalah skeleton business API UMNIA (F1-01).
//
// Hanya menyediakan health/readiness HTTP. Belum ada auth, workspace,
// database, maupun logika bisnis — semua itu masuk di F1-02/F1-03 dan
// seterusnya. Readiness sengaja melaporkan database sebagai
// "not_configured" sampai F1-02 memasang PostgreSQL; jangan dibaca
// sebagai bukti database sudah siap.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
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

func handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// shortcut: database belum dipasang (F1-02), jadi readiness hanya
	// menyatakan lapisan HTTP siap; upgrade saat BUSINESS_DATABASE_URL terisi.
	writeJSON(w, http.StatusOK, readinessResponse{
		Status:  "ok",
		Service: "business-api",
		Checks:  map[string]string{"http": "ok", "database": "not_configured"},
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

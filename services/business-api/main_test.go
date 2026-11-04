package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	rec := get(t, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: got status %d, want 200", rec.Code)
	}
	var body healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("healthz: decode: %v", err)
	}
	if body.Status != "ok" || body.Service != "business-api" {
		t.Fatalf("healthz: unexpected body %+v", body)
	}
}

func TestReadyzReportsDatabaseNotConfigured(t *testing.T) {
	rec := get(t, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz: got status %d, want 200", rec.Code)
	}
	var body readinessResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("readyz: decode: %v", err)
	}
	// F1-01 belum memasang database; tes ini mengunci kejujuran itu
	// sampai F1-02 mengubahnya menjadi pemeriksaan nyata.
	if body.Checks["database"] != "not_configured" {
		t.Fatalf("readyz: database check = %q, want not_configured", body.Checks["database"])
	}
}

func TestRootAndUnknownPaths(t *testing.T) {
	if rec := get(t, "/"); rec.Code != http.StatusOK {
		t.Fatalf("root: got status %d, want 200", rec.Code)
	}
	if rec := get(t, "/tidak-ada"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: got status %d, want 404", rec.Code)
	}
}

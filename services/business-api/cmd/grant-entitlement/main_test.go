package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsValidUUID(t *testing.T) {
	valid := "123e4567-e89b-12d3-a456-426614174000"
	if !isValidUUID(valid) {
		t.Fatalf("UUID valid ditolak: %s", valid)
	}
	for _, bad := range []string{
		"", "123e4567-e89b-12d3-a456-42661417400", "123e4567-e89b-12d3-a456-4266141740000",
		"123e4567e89b-12d3-a456-426614174000", "123e4567-e89b-12d3-a456-42661417400g",
		"not-a-uuid-at-all----------------------",
	} {
		if isValidUUID(bad) {
			t.Fatalf("UUID tidak valid diterima: %q", bad)
		}
	}
}

func TestRun_Validation(t *testing.T) {
	ws := "123e4567-e89b-12d3-a456-426614174000"
	reason := "alasan rahasia tes-123"
	cases := []struct {
		name string
		args []string
	}{
		{"uuid kosong", []string{"--workspace", "", "--module", "rental.basic", "--version", "0.1.0", "--reason", reason}},
		{"uuid bukan format", []string{"--workspace", "bukan-uuid", "--module", "rental.basic", "--version", "0.1.0", "--reason", reason}},
		{"modul kosong", []string{"--workspace", ws, "--module", "", "--version", "0.1.0", "--reason", reason}},
		{"modul terlalu panjang", []string{"--workspace", ws, "--module", strings.Repeat("m", 129), "--version", "0.1.0", "--reason", reason}},
		{"versi kosong", []string{"--workspace", ws, "--module", "rental.basic", "--version", "", "--reason", reason}},
		{"alasan kosong", []string{"--workspace", ws, "--module", "rental.basic", "--version", "0.1.0", "--reason", "   "}},
		{"alasan terlalu panjang", []string{"--workspace", ws, "--module", "rental.basic", "--version", "0.1.0", "--reason", strings.Repeat("a", 501)}},
		{"alasan multibyte terlalu panjang", []string{"--workspace", ws, "--module", "rental.basic", "--version", "0.1.0", "--reason", strings.Repeat("é", 501)}},
		{"modul multibyte terlalu panjang", []string{"--workspace", ws, "--module", strings.Repeat("é", 129), "--version", "0.1.0", "--reason", reason}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			code := run(c.args, func(string) string { return "postgres://x" }, nil, &out, &errBuf)
			if code != exitInput {
				t.Fatalf("want exit %d, got %d (out=%q err=%q)", exitInput, code, out.String(), errBuf.String())
			}
			if out.Len() != 0 {
				t.Fatalf("stdout harus kosong, got %q", out.String())
			}
			if !strings.Contains(errBuf.String(), "invalid input") {
				t.Fatalf("stderr harus berisi 'invalid input', got %q", errBuf.String())
			}
			if strings.Contains(errBuf.String(), reason) || strings.Contains(errBuf.String(), "rahasia") {
				t.Fatalf("stderr membocorkan alasan: %q", errBuf.String())
			}
		})
	}
}

// TestRun_MultibyteReasonBounds: batas 500 adalah KARAKTER (seperti
// char_length SQL), bukan byte. 500 karakter multibyte = 1000 byte:
// harus LOLOS validasi; 501 karakter multibyte harus ditolak.
func TestRun_MultibyteReasonBounds(t *testing.T) {
	ws := "123e4567-e89b-12d3-a456-426614174000"
	reason500 := strings.Repeat("é", 500)
	if len(reason500) <= 500 {
		t.Fatalf("fixture: 500 karakter 'é' harus > 500 byte (got %d)", len(reason500))
	}
	openFail := func(_ context.Context, _ string) (*sql.DB, error) { return nil, errors.New("boom") }
	var out, errBuf bytes.Buffer
	code := run([]string{"--workspace", ws, "--module", "rental.basic", "--version", "0.1.0", "--reason", reason500}, func(string) string { return "postgres://x" }, openFail, &out, &errBuf)
	if code != exitUnavailable {
		t.Fatalf("500 karakter multibyte: want exit %d (lolos validasi, gagal buka DB), got %d (err=%q)", exitUnavailable, code, errBuf.String())
	}
	if strings.Contains(errBuf.String(), "invalid input") {
		t.Fatalf("500 karakter multibyte ditolak sebagai invalid: %q", errBuf.String())
	}
	reason501 := strings.Repeat("é", 501)
	out.Reset()
	errBuf.Reset()
	code = run([]string{"--workspace", ws, "--module", "rental.basic", "--version", "0.1.0", "--reason", reason501}, func(string) string { return "postgres://x" }, nil, &out, &errBuf)
	if code != exitInput || !strings.Contains(errBuf.String(), "invalid input") {
		t.Fatalf("501 karakter multibyte: want exit %d + invalid input, got %d (err=%q)", exitInput, code, errBuf.String())
	}
}

func TestRun_MissingDSN(t *testing.T) {
	var out, errBuf bytes.Buffer
	args := []string{"--workspace", "123e4567-e89b-12d3-a456-426614174000", "--module", "rental.basic", "--version", "0.1.0", "--reason", "tes"}
	code := run(args, func(string) string { return "" }, nil, &out, &errBuf)
	if code != exitInput {
		t.Fatalf("want exit %d, got %d", exitInput, code)
	}
	if !strings.Contains(errBuf.String(), "operator database URL is not set") {
		t.Fatalf("stderr tidak sesuai: %q", errBuf.String())
	}
}

func TestClassifyCallError(t *testing.T) {
	reason := "alasan rahasia tes-123"
	detail := "DETAIL: baris rahasia"
	cases := []struct {
		code     string
		wantMsg  string
		wantExit int
	}{
		{errNotReadyCode, "not ready", exitInput},
		{errInvalidCode, "invalid input", exitInput},
		{errNotFoundCode, "not found", exitInput},
		{errConflictCode, "conflict", exitConflict},
		{"42501", "unavailable", exitUnavailable},
		{"08006", "outcome unknown", exitUnavailable},
		{"XX000", "unavailable", exitUnavailable},
	}
	for _, c := range cases {
		err := &pgconn.PgError{Code: c.code, Message: "not_ready: " + detail, Detail: detail}
		msg, code := classifyCallError(err)
		if code != c.wantExit {
			t.Fatalf("code %s: want exit %d, got %d (%q)", c.code, c.wantExit, code, msg)
		}
		if !strings.Contains(msg, c.wantMsg) {
			t.Fatalf("code %s: want msg berisi %q, got %q", c.code, c.wantMsg, msg)
		}
		if strings.Contains(msg, reason) || strings.Contains(msg, detail) {
			t.Fatalf("code %s: pesan membocorkan detail: %q", c.code, msg)
		}
	}
	// Fallback prefiks (bila server tidak membawa kode kustom).
	for _, tc := range []struct {
		prefix   string
		wantMsg  string
		wantExit int
	}{
		{"not_ready: x", "not ready", exitInput},
		{"invalid: x", "invalid input", exitInput},
		{"not_found: x", "not found", exitInput},
		{"conflict: x", "conflict", exitConflict},
	} {
		msg, code := classifyCallError(errors.New(tc.prefix))
		if code != tc.wantExit || !strings.Contains(msg, tc.wantMsg) {
			t.Fatalf("prefix %q: want (%q, %d), got (%q, %d)", tc.prefix, tc.wantMsg, tc.wantExit, msg, code)
		}
	}
}

func TestClassifyPingFailure(t *testing.T) {
	if got := classifyPingFailure(&pgconn.PgError{Code: "28P01", Message: "password authentication failed"}); !strings.Contains(got, "authentication failed") {
		t.Fatalf("28P01: want 'authentication failed', got %q", got)
	}
	if got := classifyPingFailure(&net.OpError{Op: "dial", Err: errors.New("boom")}); !strings.Contains(got, "unreachable") {
		t.Fatalf("dial error: want 'unreachable', got %q", got)
	}
}

// TestRun_PingFailureRedaction: DSN berisi kata sandi palsu dan alasan
// berisi teks sensitif; kegagalan Ping tidak boleh mencetak keduanya.
func TestRun_PingFailureRedaction(t *testing.T) {
	secret := "SECRET-PALSU-123"
	reason := "alasan rahasia tes-123"
	dsn := "postgres://umnia_op_test:" + secret + "@127.0.0.1:1/umnia_test?sslmode=disable"
	var out, errBuf bytes.Buffer
	args := []string{"--workspace", "123e4567-e89b-12d3-a456-426614174000", "--module", "rental.basic", "--version", "0.1.0", "--reason", reason}
	open := func(_ context.Context, d string) (*sql.DB, error) { return sql.Open("pgx", d) }
	code := run(args, func(string) string { return dsn }, open, &out, &errBuf)
	if code != exitUnavailable {
		t.Fatalf("want exit %d, got %d (out=%q err=%q)", exitUnavailable, code, out.String(), errBuf.String())
	}
	if strings.Contains(errBuf.String(), secret) || strings.Contains(out.String(), secret) {
		t.Fatalf("output membocorkan DSN/kata sandi: err=%q out=%q", errBuf.String(), out.String())
	}
	if strings.Contains(errBuf.String(), reason) || strings.Contains(out.String(), reason) {
		t.Fatalf("output membocorkan alasan: err=%q out=%q", errBuf.String(), out.String())
	}
}

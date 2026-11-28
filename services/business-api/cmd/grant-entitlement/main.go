// Command grant-entitlement adalah satu-satunya cara operator memberi
// atau mencabut entitlement ke satu workspace: ia memanggil rutin
// SECURITY DEFINER lewat DSN operator, bukan lewat DML mentah.
// --operation grant (default) memanggil app.grant_module_entitlement
// untuk rental.basic/0.1.0 atau app.grant_finance_entitlement untuk
// finance.basic/0.1.0, atau app.grant_inventory_entitlement untuk inventory.basic/0.1.0.
// --operation revoke hanya untuk finance.basic
// dan memanggil app.revoke_finance_entitlement (tiga argumen).
// CLI tidak membaca .env aplikasi, tidak menerima --operator/--source,
// tidak pernah mencetak DSN, alasan (reason), atau galat mentah
// driver. Satu pemanggilan = satu percobaan; tidak ada retry internal.
// Retry BUKAN observasi read-only: retry adalah mutasi desired-state
// — perintah lawan yang masuk di antaranya (mis. revoke sesudah grant
// yang hasilnya tidak diketahui) membuat retry menjadi transisi BARU,
// bukan pengulangan yang lama. Bila hasil tidak diketahui dan ada
// operator lain yang bersaing, rekonsiliasi state/audit yang sah
// dulu sebelum menjalankan ulang; tidak ada deduplikasi seumur hidup
// tanpa operation ID.
//
// Kode keluar: 0 sukses (granted / already_granted / legacy_existing /
// revoked / already_revoked); 2 input tidak valid, kombinasi tidak
// didukung, workspace tidak ditemukan, atau skema operator belum siap
// (tidak ada tulis yang dicoba); 3 konflik versi (tidak ada tulis);
// 1 tidak tersedia / hasil tidak diketahui (mungkin sudah tertulis —
// jalankan ulang untuk mengamati state, dengan catatan retry di atas).
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	exitOK          = 0
	exitUnavailable = 1
	exitInput       = 2
	exitConflict    = 3
)

const (
	grantRoutineSignature          = `app.grant_module_entitlement(uuid, text, text, text)`
	grantFinanceRoutineSignature   = `app.grant_finance_entitlement(uuid, text, text, text)`
	grantInventoryRoutineSignature = `app.grant_inventory_entitlement(uuid, text, text, text)`
	revokeRoutineSignature         = `app.revoke_finance_entitlement(uuid, text, text)`
	grantQuery                     = `SELECT * FROM app.grant_module_entitlement($1, $2, $3, $4)`
	grantFinanceQuery              = `SELECT * FROM app.grant_finance_entitlement($1, $2, $3, $4)`
	grantInventoryQuery            = `SELECT * FROM app.grant_inventory_entitlement($1, $2, $3, $4)`
	revokeQuery                    = `SELECT * FROM app.revoke_finance_entitlement($1, $2, $3)`
)

const (
	errNotReadyCode = "U11NR"
	errInvalidCode  = "U11IV"
	errNotFoundCode = "U11NF"
	errConflictCode = "U11CF"
)

const (
	errNotReadyCode14 = "U14NR"
	errInvalidCode14  = "U14IV"
	errNotFoundCode14 = "U14NF"
	errConflictCode14 = "U14CF"
)

type grantResult struct {
	result       string
	workspaceID  string
	moduleID     string
	version      string
	grantedBy    string
	grantedAt    time.Time
	auditPresent bool
}

// revokeResult mencerminkan hasil rutin revoke: revoked_by/revoked_at
// NULL untuk already_revoked (tanpa baris saat ini), jadi dipakai
// sql.NullString/sql.NullTime agar kebenaran itu tercetak jujur
// (kosong, bukan aktor/waktu palsu).
type revokeResult struct {
	result       string
	workspaceID  string
	moduleID     string
	version      string
	revokedBy    sql.NullString
	revokedAt    sql.NullTime
	auditPresent bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, openRealDB, os.Stdout, os.Stderr))
}

func openRealDB(_ context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// run menjalankan satu grant/revoke dan mengembalikan kode keluar.
// getenv dan openDB diinjeksikan supaya tes unit bisa memeriksa
// validasi/redaksi tanpa database sungguhan.
func run(args []string, getenv func(string) string, openDB func(context.Context, string) (*sql.DB, error), stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("grant-entitlement", flag.ContinueOnError)
	fs.SetOutput(stderr)
	operation := fs.String("operation", "grant", "grant atau revoke (default grant; revoke hanya finance.basic)")
	workspace := fs.String("workspace", "", "workspace UUID target")
	module := fs.String("module", "", "module id (rental.basic, finance.basic, atau inventory.basic)")
	version := fs.String("version", "", "versi katalog (0.1.0; wajib 0.1.0 untuk finance dan inventory)")
	reason := fs.String("reason", "", "alasan wajib (1-500 karakter)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitInput
	}
	op := strings.TrimSpace(*operation)
	ws := strings.TrimSpace(*workspace)
	mod := strings.TrimSpace(*module)
	ver := strings.TrimSpace(*version)
	rsn := strings.TrimSpace(*reason)
	if op != "grant" && op != "revoke" {
		fmt.Fprintln(stderr, "grant-entitlement: invalid input")
		return exitInput
	}
	// Revoke boleh tanpa --version: defaultnya 0.1.0 (satu-satunya
	// versi Finance yang didukung). Grant tetap wajib menyebut versi.
	if op == "revoke" && ver == "" {
		ver = "0.1.0"
	}
	// Batas dihitung dalam rune (karakter), sama seperti char_length
	// di sisi SQL — bukan byte, agar alasan multibyte yang valid
	// tidak ditolak.
	if !isValidUUID(ws) || mod == "" || utf8.RuneCountInString(mod) > 128 || ver == "" || utf8.RuneCountInString(ver) > 64 || rsn == "" || utf8.RuneCountInString(rsn) > 500 {
		fmt.Fprintln(stderr, "grant-entitlement: invalid input")
		return exitInput
	}
	// Kombinasi yang tidak didukung ditolak SEBELUM akses DB.
	if op == "revoke" && mod != "finance.basic" {
		fmt.Fprintln(stderr, "grant-entitlement: invalid input")
		return exitInput
	}
	if (mod == "finance.basic" || mod == "inventory.basic") && ver != "0.1.0" {
		fmt.Fprintln(stderr, "grant-entitlement: invalid input")
		return exitInput
	}
	if op == "grant" && mod != "rental.basic" && mod != "finance.basic" && mod != "inventory.basic" {
		fmt.Fprintln(stderr, "grant-entitlement: invalid input")
		return exitInput
	}
	dsn := getenv("UMNIA_OPERATOR_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(stderr, "grant-entitlement: operator database URL is not set")
		return exitInput
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := openDB(ctx, dsn)
	if err != nil {
		fmt.Fprintln(stderr, "grant-entitlement: unavailable")
		return exitUnavailable
	}
	defer db.Close()

	// Ping dulu: gagal di sini berarti tidak ada pernyataan yang
	// terkirim (belum ada yang bisa tertulis).
	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintln(stderr, "grant-entitlement: "+classifyPingFailure(err))
		return exitUnavailable
	}

	routineSig, callQuery := grantRoutineSignature, grantQuery
	isRevoke := op == "revoke"
	switch {
	case isRevoke:
		routineSig, callQuery = revokeRoutineSignature, revokeQuery
	case mod == "finance.basic":
		routineSig, callQuery = grantFinanceRoutineSignature, grantFinanceQuery
	case mod == "inventory.basic":
		routineSig, callQuery = grantInventoryRoutineSignature, grantInventoryQuery
	}
	metadataQuery := `SELECT has_function_privilege('` + routineSig + `'::regprocedure, 'EXECUTE')`

	// Metadata fungsi/EXECUTE untuk tanda tangan YANG DIPILIH:
	// rutin tidak ada atau tidak bisa dieksekusi peran ini = skema
	// belum siap (fail closed).
	var canExecute bool
	if err := db.QueryRowContext(ctx, metadataQuery).Scan(&canExecute); err != nil {
		msg, code := classifyMetadataError(err)
		fmt.Fprintln(stderr, "grant-entitlement: "+msg)
		return code
	}
	if !canExecute {
		fmt.Fprintln(stderr, "grant-entitlement: not ready (operator routine is not available)")
		return exitInput
	}

	// Satu pemanggilan rutin; tidak ada retry internal.
	if isRevoke {
		var r revokeResult
		err = db.QueryRowContext(ctx, callQuery, ws, mod, rsn).Scan(
			&r.result, &r.workspaceID, &r.moduleID, &r.version, &r.revokedBy, &r.revokedAt, &r.auditPresent)
		if err != nil {
			msg, code := classifyCallError(err)
			fmt.Fprintln(stderr, "grant-entitlement: "+msg)
			return code
		}
		revokedBy, revokedAt := "", ""
		if r.revokedBy.Valid {
			revokedBy = r.revokedBy.String
		}
		if r.revokedAt.Valid {
			revokedAt = r.revokedAt.Time.Format(time.RFC3339)
		}
		fmt.Fprintf(stdout, "%s workspace=%s module=%s version=%s revoked_by=%s revoked_at=%s audit_present=%t\n",
			r.result, r.workspaceID, r.moduleID, r.version, revokedBy, revokedAt, r.auditPresent)
		return exitOK
	}
	var r grantResult
	err = db.QueryRowContext(ctx, callQuery, ws, mod, ver, rsn).Scan(
		&r.result, &r.workspaceID, &r.moduleID, &r.version, &r.grantedBy, &r.grantedAt, &r.auditPresent)
	if err != nil {
		msg, code := classifyCallError(err)
		fmt.Fprintln(stderr, "grant-entitlement: "+msg)
		return code
	}
	fmt.Fprintf(stdout, "%s workspace=%s module=%s version=%s granted_by=%s granted_at=%s audit_present=%t\n",
		r.result, r.workspaceID, r.moduleID, r.version, r.grantedBy, r.grantedAt.Format(time.RFC3339), r.auditPresent)
	return exitOK
}

// classifyPingFailure memetakan kegagalan koneksi awal ke teks tetap
// (tanpa detail driver/DSN). Ping gagal = tidak ada yang terkirim.
func classifyPingFailure(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "28P01" {
			return "unavailable (authentication failed)"
		}
		return "unavailable"
	}
	if isConnectionError(err) {
		return "unavailable (database unreachable)"
	}
	return "unavailable"
}

// classifyMetadataError memetakan kegagalan pemeriksaan metadata
// fungsi. Di tahap ini belum ada mutasi yang dikirim.
func classifyMetadataError(err error) (string, int) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42883", "42501":
			return "not ready (operator routine is not available)", exitInput
		}
	}
	if msg, ok := mapRoutinePrefix(err); ok {
		return msg, exitInput
	}
	return "unavailable", exitUnavailable
}

// classifyCallError memetakan kegagalan pemanggilan rutin. Galat
// koneksi SESUDAH pemanggilan dikirim berarti hasil tidak diketahui
// (bukan jaminan rollback); operator harus menjalankan ulang CLI untuk
// mengamati state. Galat 42501 di tahap ini adalah kegagalan internal
// rutin (mis. INSERT audit ditolak) — bukan "tidak siap".
func classifyCallError(err error) (string, int) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case errNotReadyCode, errNotReadyCode14:
			return "not ready (operator schema is not provisioned)", exitInput
		case errInvalidCode, errInvalidCode14:
			return "invalid input", exitInput
		case errNotFoundCode, errNotFoundCode14:
			return "not found", exitInput
		case errConflictCode, errConflictCode14:
			return "conflict (a different version is already granted)", exitConflict
		}
		if isConnectionCode(pgErr.Code) {
			return "outcome unknown; re-run to observe state", exitUnavailable
		}
		return "unavailable", exitUnavailable
	}
	if isConnectionError(err) {
		return "outcome unknown; re-run to observe state", exitUnavailable
	}
	if msg, code, ok := mapRoutinePrefixWithCode(err); ok {
		return msg, code
	}
	return "unavailable", exitUnavailable
}

// mapRoutinePrefix memetakan prefiks pesan rutin ke kategori not-ready
// (dipakai bila server tidak membawa kode kustom). Pesan mentah tidak
// pernah dicetak; hanya kategori tetap yang dipakai.
func mapRoutinePrefix(err error) (string, bool) {
	msg, _, ok := mapRoutinePrefixWithCode(err)
	return msg, ok
}

func mapRoutinePrefixWithCode(err error) (string, int, bool) {
	m := err.Error()
	switch {
	case strings.HasPrefix(m, "not_ready:"):
		return "not ready (operator schema is not provisioned)", exitInput, true
	case strings.HasPrefix(m, "invalid:"):
		return "invalid input", exitInput, true
	case strings.HasPrefix(m, "not_found:"):
		return "not found", exitInput, true
	case strings.HasPrefix(m, "conflict:"):
		return "conflict (a different version is already granted)", exitConflict, true
	}
	return "", 0, false
}

// isConnectionError mendeteksi galat transport (dial/timeout/reset)
// tanpa memeriksa teks galat.
func isConnectionError(err error) bool {
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, sql.ErrConnDone) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return isConnectionCode(pgErr.Code)
	}
	return false
}

// isConnectionCode: kelas SQLSTATE 08 = connection exception.
func isConnectionCode(code string) bool {
	return strings.HasPrefix(code, "08")
}

// isValidUUID memeriksa format 8-4-4-4-12 heksadesimal tanpa dependensi
// baru. Versi/varian UUID tidak ditegakkan (rutin/DB yang menolaknya).
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

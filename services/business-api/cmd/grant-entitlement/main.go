// Command grant-entitlement adalah satu-satunya cara operator memberi
// entitlement rental.basic/0.1.0 ke satu workspace: ia memanggil rutin
// app.grant_module_entitlement (SECURITY DEFINER) lewat DSN operator,
// bukan lewat DML mentah. CLI tidak membaca .env aplikasi, tidak
// menerima --operator/--source, tidak pernah mencetak DSN, alasan
// (reason), atau galat mentah driver. Satu pemanggilan = satu percobaan;
// tidak ada retry internal (retry = jalankan ulang CLI, yang mengamati
// state yang ada tanpa tulis bila sudah granted).
//
// Kode keluar: 0 sukses (granted / already_granted / legacy_existing);
// 2 input tidak valid, workspace tidak ditemukan, atau skema operator
// belum siap (tidak ada tulis yang dicoba); 3 konflik versi (tidak ada
// tulis); 1 tidak tersedia / hasil tidak diketahui (mungkin sudah
// tertulis — jalankan ulang untuk mengamati state).
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
	routineSignature = `app.grant_module_entitlement(uuid, text, text, text)`
	metadataQuery    = `SELECT has_function_privilege('` + routineSignature + `'::regprocedure, 'EXECUTE')`
	grantQuery       = `SELECT * FROM app.grant_module_entitlement($1, $2, $3, $4)`
)

const (
	errNotReadyCode = "U11NR"
	errInvalidCode  = "U11IV"
	errNotFoundCode = "U11NF"
	errConflictCode = "U11CF"
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

// run menjalankan satu grant dan mengembalikan kode keluar. getenv dan
// openDB diinjeksikan supaya tes unit bisa memeriksa validasi/redaksi
// tanpa database sungguhan.
func run(args []string, getenv func(string) string, openDB func(context.Context, string) (*sql.DB, error), stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("grant-entitlement", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workspace := fs.String("workspace", "", "workspace UUID target")
	module := fs.String("module", "", "module id (hanya rental.basic)")
	version := fs.String("version", "", "versi katalog (hanya 0.1.0)")
	reason := fs.String("reason", "", "alasan wajib (1-500 karakter)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitInput
	}
	ws := strings.TrimSpace(*workspace)
	mod := strings.TrimSpace(*module)
	ver := strings.TrimSpace(*version)
	rsn := strings.TrimSpace(*reason)
	// Batas dihitung dalam rune (karakter), sama seperti char_length
	// di sisi SQL — bukan byte, agar alasan multibyte yang valid
	// tidak ditolak.
	if !isValidUUID(ws) || mod == "" || utf8.RuneCountInString(mod) > 128 || ver == "" || utf8.RuneCountInString(ver) > 64 || rsn == "" || utf8.RuneCountInString(rsn) > 500 {
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

	// Metadata fungsi/EXECUTE: rutin tidak ada atau tidak bisa
	// dieksekusi peran ini = skema belum siap (fail closed).
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
	var r grantResult
	err = db.QueryRowContext(ctx, grantQuery, ws, mod, ver, rsn).Scan(
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
		case errNotReadyCode:
			return "not ready (operator schema is not provisioned)", exitInput
		case errInvalidCode:
			return "invalid input", exitInput
		case errNotFoundCode:
			return "not found", exitInput
		case errConflictCode:
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

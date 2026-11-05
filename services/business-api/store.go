package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
)

type ctxKey string

const ctxSubject ctxKey = "subject"

// uuidNew membuat UUID v4 dari crypto/rand (stdlib saja, tanpa dependensi
// uuid) — dipakai untuk id workspace yang harus diketahui sebelum INSERT,
// supaya konteks RLS (app.current_workspace_id) bisa diset lebih dulu.
func uuidNew() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// beginTenant memulai transaksi dengan konteks RLS: subjek selalu diset,
// workspace diset bila tidak kosong (SET LOCAL, hanya berlaku di transaksi).
func beginTenant(db *sql.DB, subject, workspace string) (*sql.Tx, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`SELECT set_config('app.current_subject_id', $1, true)`, subject); err != nil {
		tx.Rollback()
		return nil, err
	}
	if workspace != "" {
		if _, err := tx.Exec(`SELECT set_config('app.current_workspace_id', $1, true)`, workspace); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	return tx, nil
}

func subjectFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxSubject).(string)
	return v, ok && v != ""
}

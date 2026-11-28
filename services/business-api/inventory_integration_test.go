package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInventoryBasicLedger(t *testing.T) {
	env := setupActivation(t, nil)
	base := "/workspaces/" + env.wsA
	postPath := base + "/inventory/movements"
	opURL := os.Getenv("OPERATOR_TEST_DATABASE_URL")
	if opURL == "" {
		t.Skip("OPERATOR_TEST_DATABASE_URL required")
	}
	_, err := env.db.Exec(`SELECT * FROM app.grant_inventory_entitlement($1, $2, $3, 'runtime denial')`,
		env.wsB, inventoryModuleID, inventoryModuleVersion)
	assertPermissionDenied(t, err, "inventory grant from runtime role")
	operator, err := sql.Open("pgx", opURL)
	if err != nil {
		t.Fatal(err)
	}
	defer operator.Close()
	if _, err := operator.Exec(`SELECT * FROM app.grant_inventory_entitlement($1, $2, '9.9.9', 'wrong version')`,
		env.wsA, inventoryModuleID); err == nil || !strings.Contains(err.Error(), "SQLSTATE U14IV") {
		t.Fatalf("operator grant accepted wrong version: %v", err)
	}
	bin := buildGrantCLI(t)
	args := []string{"--workspace", env.wsA, "--module", inventoryModuleID, "--version", inventoryModuleVersion, "--reason", "inventory pilot fixture"}
	for _, want := range []string{"granted ", "already_granted "} {
		result := runGrantCLI(t, bin, opURL, args...)
		if result.code != 0 || !strings.HasPrefix(result.stdout, want) || !strings.Contains(result.stdout, "audit_present=true") {
			t.Fatalf("grant want %q with audit: %d %s %s", want, result.code, result.stdout, result.stderr)
		}
	}
	var count, countMatching int
	if err := env.boot.QueryRow(`SELECT count(*) FROM operator_audit_events WHERE workspace_id = $1 AND module_id = $2`,
		env.wsA, inventoryModuleID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("operator audit count = %d, err = %v", count, err)
	}
	if code, result := activateModule(t, env.srv, env.wsA, inventoryModuleID, env.ownerToken, inventoryModuleVersion); code != 200 {
		t.Fatalf("activate: %d %v", code, result)
	}

	newItem := func(sku string) string {
		t.Helper()
		code, result := createCatalogItem(t, env.srv, env.wsA, env.ownerToken,
			map[string]string{"sku": sku, "name": sku, "unit": "pcs"})
		if code != 201 {
			t.Fatalf("catalog: %d %v", code, result)
		}
		return result["id"].(string)
	}
	newKey := func() string {
		t.Helper()
		id, err := uuidNew()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	post := func(token, key string, body any, want int) map[string]any {
		t.Helper()
		code, result, err := callDraftAPI(env.srv, "POST", postPath, token, []string{key}, body)
		if err != nil || code != want {
			t.Fatalf("post want %d, got %d %v error=%v", want, code, result, err)
		}
		return result
	}
	stock := func(id, want string) {
		t.Helper()
		code, result := doJSON(t, env.srv, "GET", base+"/inventory/stock", env.cashToken, nil)
		if code != 200 || result["can_post"] != false || result["can_reverse"] != false {
			t.Fatalf("cashier stock read: %d %v", code, result)
		}
		for _, raw := range result["items"].([]any) {
			item := raw.(map[string]any)
			if item["catalog_item_id"] == id && item["quantity"] == want && item["unit"] == "pcs" {
				return
			}
		}
		t.Fatalf("stock want %s = %s pcs, got %v", id, want, result)
	}
	item := newItem("PILOT")
	receipt := map[string]string{"catalog_item_id": item, "kind": "receipt", "quantity": "10", "source_reference": "IN-1"}
	receiptKey := newKey()
	post(env.cashToken, receiptKey, receipt, 403)
	post(env.otherToken, receiptKey, receipt, 403)
	first := post(env.mgrToken, receiptKey, receipt, 201)
	receipt["source_reference"] = "  IN-1  "
	if replay := post(env.mgrToken, strings.ToUpper(receiptKey), receipt, 200); replay["id"] != first["id"] {
		t.Fatalf("replay changed id: %v", replay)
	}
	receipt["quantity"] = "11"
	post(env.mgrToken, receiptKey, receipt, 409)
	stock(item, "10")

	waitForWriter := func(holder *sql.Tx) {
		t.Helper()
		var pid int
		if err := holder.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var waiting bool
			err := env.boot.QueryRow(`SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity WHERE pid <> $1 AND $1 = ANY(pg_blocking_pids(pid))
			)`, pid).Scan(&waiting)
			if err != nil {
				t.Fatal(err)
			}
			if waiting {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		holder.Rollback()
		t.Fatal("request did not queue behind the workspace lock")
	}

	type outcome struct {
		code   int
		result map[string]any
		err    error
	}
	issues := make(chan outcome, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		ref, key := fmt.Sprintf("OUT-%d", i), newKey()
		go func() {
			<-start
			body := map[string]string{"catalog_item_id": item, "kind": "issue", "quantity": "7", "source_reference": ref}
			code, result, err := callDraftAPI(env.srv, "POST", postPath, env.ownerToken, []string{key}, body)
			issues <- outcome{code, result, err}
		}()
	}
	close(start)
	yes, no := <-issues, <-issues
	if yes.code != 201 {
		yes, no = no, yes
	}
	if yes.err != nil || no.err != nil || yes.code != 201 || no.code != 409 {
		t.Fatalf("concurrent issue: %+v %+v", yes, no)
	}
	stock(item, "3")
	reverseReceipt := map[string]string{"catalog_item_id": item, "kind": "reversal", "reverses_id": first["id"].(string), "source_reference": "REV-IN-1", "reason": "Too little stock to reverse"}
	post(env.ownerToken, newKey(), reverseReceipt, 409)
	reversal := map[string]string{"catalog_item_id": item, "kind": "reversal", "reverses_id": yes.result["id"].(string), "source_reference": "REV-1", "reason": "Wrong document"}
	reversalKey := newKey()
	post(env.mgrToken, reversalKey, reversal, 403)
	reversed := post(env.ownerToken, reversalKey, reversal, 201)
	if reversed["delta"] != "7" || reversed["reverses_id"] != yes.result["id"] {
		t.Fatalf("reversal delta or link: %v", reversed)
	}
	post(env.ownerToken, reversalKey, reversal, 200)
	post(env.ownerToken, newKey(), reversal, 409)
	stock(item, "10")

	assetBody := func(itemID string) map[string]string {
		return map[string]string{"catalog_item_id": itemID, "serial": "AS-" + itemID}
	}
	for _, stockFirst := range []bool{true, false} {
		id, key := newItem(fmt.Sprintf("CLASS-%t", stockFirst)), newKey()
		// Hold the shared lock so both requests can start while the first writer owns it.
		tx, err := env.boot.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := lockWorkspace(tx, env.wsA); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}

		finished := make(chan outcome, 1)
		result := map[string]string{"catalog_item_id": id, "kind": "receipt", "quantity": "1", "source_reference": "CONCURRENT"}
		if stockFirst {
			go func() {
				code, body, err := callDraftAPI(env.srv, "POST", base+"/assets", env.mgrToken, nil, assetBody(id))
				finished <- outcome{code, body, err}
			}()
			_, err = tx.Exec(`INSERT INTO inventory_items (workspace_id, catalog_item_id, unit) VALUES ($1, $2, 'pcs')`, env.wsA, id)
		} else {
			go func() {
				code, body, err := callDraftAPI(env.srv, "POST", postPath, env.mgrToken, []string{key}, result)
				finished <- outcome{code, body, err}
			}()
			_, err = tx.Exec(`INSERT INTO physical_assets (workspace_id, catalog_item_id, serial) VALUES ($1, $2, $3)`,
				env.wsA, id, assetBody(id)["serial"])
		}
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		waitForWriter(tx)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		got := <-finished
		if got.code != 409 || got.err != nil {
			t.Fatalf("classification stock first=%t: %+v", stockFirst, got)
		}
	}

	hugeItem := newItem("HUGE")
	huge := map[string]string{"catalog_item_id": hugeItem, "kind": "receipt", "quantity": "9223372036854775807", "source_reference": "MAX"}
	post(env.ownerToken, newKey(), huge, 201)
	stock(hugeItem, "9223372036854775807")
	huge["quantity"] = "1"
	post(env.ownerToken, newKey(), huge, 409)
	huge["quantity"] = "9223372036854775808"
	post(env.ownerToken, newKey(), huge, 400)

	// A rejected audit insert must also roll back enrollment and the movement.
	failConstraint := fmt.Sprintf(`ALTER TABLE audit_events ADD CONSTRAINT qa_inventory_audit
		CHECK (workspace_id <> '%s'::uuid OR action <> 'inventory_movement_posted') NOT VALID`, env.wsA)
	if _, err := env.boot.Exec(failConstraint); err != nil {
		t.Fatal(err)
	}
	defer env.boot.Exec(`ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS qa_inventory_audit`)
	rollbackItem := newItem("ROLLBACK")
	rollback := map[string]string{"catalog_item_id": rollbackItem, "kind": "receipt", "quantity": "2", "source_reference": "ROLLBACK"}
	rollbackKey := newKey()
	post(env.ownerToken, rollbackKey, rollback, 500)
	if err := env.boot.QueryRow(`SELECT count(*) FROM inventory_items WHERE workspace_id = $1 AND catalog_item_id = $2`,
		env.wsA, rollbackItem).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back item count = %d, err = %v", count, err)
	}
	if _, err := env.boot.Exec(`ALTER TABLE audit_events DROP CONSTRAINT qa_inventory_audit`); err != nil {
		t.Fatal(err)
	}
	post(env.ownerToken, rollbackKey, rollback, 201)

	if err := env.boot.QueryRow(`SELECT count(*), count(m.id) FROM audit_events a
		LEFT JOIN inventory_movements m ON m.workspace_id = a.workspace_id AND m.id = a.entity_id AND m.recorded_by = a.actor_subject_id
		WHERE a.workspace_id = $1 AND a.action = 'inventory_movement_posted'`, env.wsA).Scan(&count, &countMatching); err != nil || count != 5 || countMatching != 5 {
		t.Fatalf("business audit count = %d, matching movements and actors = %d, err = %v", count, countMatching, err)
	}
	if code, body := deactivateModule(t, env.srv, env.wsA, inventoryModuleID, env.ownerToken); code != 200 {
		t.Fatalf("deactivate: %d %v", code, body)
	}
	post(env.ownerToken, reversalKey, reversal, 403)
	if code, body := activateModule(t, env.srv, env.wsA, inventoryModuleID, env.ownerToken, inventoryModuleVersion); code != 200 {
		t.Fatalf("reactivate: %d %v", code, body)
	}

	// Revoke first while a POST is queued behind the same workspace lock.
	tx, err := env.boot.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockWorkspace(tx, env.wsA); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	revoked := make(chan outcome, 1)
	go func() {
		payload := fmt.Sprintf(`{"catalog_item_id":%q,"kind":"issue","quantity":"1","source_reference":"REVOKE"}`, item)
		code, body, err := postRawGoCtx(ctx, env.srv, postPath, env.ownerToken, []string{reversalKey}, payload)
		revoked <- outcome{code, body, err}
	}()
	if _, err := tx.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1 AND module_id = $2`, env.wsA, inventoryModuleID); err != nil {
		t.Fatal(err)
	}
	waitForWriter(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := <-revoked; got.err != nil || got.code != 403 {
		t.Fatalf("post after revoke lock: %+v", got)
	}

	if code, body := doJSON(t, env.srv, "GET", base+"/inventory/stock", env.ownerToken, nil); code != 403 {
		t.Fatalf("stock after revoke: %d %v", code, body)
	}
	if code, body := doJSON(t, env.srv, "GET", postPath, env.cashToken, nil); code != 200 || len(body["items"].([]any)) != 5 {
		t.Fatalf("retained cashier history: %d %v", code, body)
	}
	if code, body := doJSON(t, env.srv, "GET", postPath, env.otherToken, nil); code != 403 {
		t.Fatalf("cross-tenant history: %d %v", code, body)
	}

	applyMigrationFiles(t, os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL"), []string{"015_inventory_basic.sql"})
	if code, body := doJSON(t, env.srv, "GET", postPath, env.ownerToken, nil); code != 200 || len(body["items"].([]any)) != 5 {
		t.Fatalf("retained history after rerun: %d %v", code, body)
	}

	runtime, err := beginTenant(env.db, env.ownerID, env.wsA)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Rollback()
	if _, err := runtime.Exec(`UPDATE inventory_movements SET delta = 1 WHERE workspace_id = $1`, env.wsA); err == nil {
		t.Fatal("runtime can overwrite ledger")
	}
	if err := runtime.Rollback(); err != nil && err != sql.ErrTxDone {
		t.Fatal(err)
	}
}

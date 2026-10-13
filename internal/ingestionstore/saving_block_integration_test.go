//go:build integration

package ingestionstore

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
)

func resetSavingBlock(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"stg_fincloud_saving_block_transactions", "fincloud_saving_block_transactions"} {
		if _, err := db.Exec("DELETE FROM `" + table + "`"); err != nil {
			t.Fatal(err)
		}
	}
}

func savingBlockItems(t *testing.T, account string, reasons ...string) []ingestion.DetailChildRecord {
	t.Helper()
	items := "["
	for index, reason := range reasons {
		if index > 0 {
			items += ","
		}
		items += `{"id_transaction":"T-` + reason + `","tglblokir":"2025-10-15","norekening":"` + account + `","alasanblokir":"` + reason + `","nilaiblokir":"1,000,082.00","not_batch":false,"is_open":true}`
	}
	mapped, err := ingestion.MapSavingBlockDetail(context.Background(), account, &fincloud.SavingBlockDetail{
		RawPayload: []byte(`{"norekening":"` + account + `","datablockopen":` + items + `]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return mapped
}

func savingBlockKeys(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	return keys
}

func finalSavingBlockKeys(t *testing.T, db *sql.DB) []string {
	return savingBlockKeys(t, db, `SELECT CONCAT(account_no,':',item_index,':',block_reason) FROM fincloud_saving_block_transactions ORDER BY account_no,item_index`)
}

func TestSavingBlockStagingIsReplaySafeAndRunScoped(t *testing.T) {
	db := integrationdb.Open(t)
	resetSavingBlock(t, db.DB)
	repository := NewDetailRepository(db)
	fetched := time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC)
	runID, owner := detailRun(t, db.DB, "saving_block_detail", 0)
	staged := func(id uint64) []string {
		return savingBlockKeys(t, db.DB, `SELECT CONCAT(account_no,':',item_index) FROM stg_fincloud_saving_block_transactions WHERE ingestion_run_id=? ORDER BY account_no,item_index`, id)
	}
	if err := repository.StageSavingBlockTransactions(context.Background(), runID, "A", fetched, savingBlockItems(t, "A", "x", "y")); err != nil {
		t.Fatal(err)
	}
	if got := staged(runID); !reflect.DeepEqual(got, []string{"A:0", "A:1"}) {
		t.Fatalf("staged=%v", got)
	}
	if err := repository.StageSavingBlockTransactions(context.Background(), runID, "A", fetched, savingBlockItems(t, "A")); err != nil {
		t.Fatal(err)
	}
	if got := staged(runID); len(got) != 0 {
		t.Fatalf("replayed zero-item stage=%v", got)
	}
	if err := repository.StageSavingBlockTransactions(context.Background(), runID, "A", fetched, savingBlockItems(t, "B", "x")); err == nil {
		t.Fatal("item of another account staged")
	}
	if err := repository.StageSavingBlockTransactions(context.Background(), runID, "A", fetched, savingBlockItems(t, "A", "x")); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSavingBlockTransactions(context.Background(), runID, owner, 1); err != nil {
		t.Fatal(err)
	}
	// A later running run's candidate is untouched by terminal cleanup of the finished one.
	nextID, _ := detailRun(t, db.DB, "saving_block_detail", 0)
	if err := repository.StageSavingBlockTransactions(context.Background(), nextID, "A", fetched, savingBlockItems(t, "A", "x", "y")); err != nil {
		t.Fatal(err)
	}
	if deleted, err := repository.CleanupTerminal(context.Background(), 100); err != nil || deleted < 1 {
		t.Fatalf("cleanup deleted=%d error=%v", deleted, err)
	}
	if got, next := staged(runID), staged(nextID); len(got) != 0 || !reflect.DeepEqual(next, []string{"A:0", "A:1"}) {
		t.Fatalf("after terminal cleanup finished=%v running=%v", got, next)
	}
	if err := repository.CleanupRun(context.Background(), nextID); err != nil || len(staged(nextID)) != 0 {
		t.Fatalf("cleanup run error=%v staged=%v", err, staged(nextID))
	}
}

func TestSavingBlockPublicationIsGlobalExactSet(t *testing.T) {
	db := integrationdb.Open(t)
	resetSavingBlock(t, db.DB)
	repository := NewDetailRepository(db)
	first := time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	publish := func(fetched time.Time, accounts map[string][]string) uint64 {
		t.Helper()
		runID, owner := detailRun(t, db.DB, "saving_block_detail", uint64(len(accounts)))
		var rows uint64
		for account, reasons := range accounts {
			items := savingBlockItems(t, account, reasons...)
			rows += uint64(len(items))
			if err := repository.StageSavingBlockTransactions(context.Background(), runID, account, fetched, items); err != nil {
				t.Fatal(err)
			}
		}
		if err := repository.PublishSavingBlockTransactions(context.Background(), runID, owner, rows); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := db.Get(&status, `SELECT status FROM ingestion_runs WHERE id=?`, runID); err != nil || status != "succeeded" {
			t.Fatalf("published run status=%q error=%v", status, err)
		}
		return runID
	}

	publish(first, map[string][]string{"A": {"a0", "a1"}, "B": {"b0"}, "C": {"c0"}})
	var createdBefore, updatedBefore time.Time
	if err := db.QueryRow(`SELECT created_at,updated_at FROM fincloud_saving_block_transactions WHERE account_no='C'`).Scan(&createdBefore, &updatedBefore); err != nil {
		t.Fatal(err)
	}
	// A returns empty, B changes, C is unchanged, D is new; nothing references fincloud_saving_details.
	publish(second, map[string][]string{"A": {}, "B": {"b0-changed"}, "C": {"c0"}, "D": {"d0"}})
	if got := finalSavingBlockKeys(t, db.DB); !reflect.DeepEqual(got, []string{"B:0:b0-changed", "C:0:c0", "D:0:d0"}) {
		t.Fatalf("final=%v", got)
	}
	var fetchedAt, createdAfter, updatedAfter time.Time
	var amount string
	var notBatch, isOpen bool
	if err := db.QueryRow(`SELECT last_fetched_at,created_at,updated_at,CAST(blocked_amount AS CHAR),not_batch,is_open FROM fincloud_saving_block_transactions WHERE account_no='C'`).
		Scan(&fetchedAt, &createdAfter, &updatedAfter, &amount, &notBatch, &isOpen); err != nil {
		t.Fatal(err)
	}
	if !fetchedAt.Equal(second) || !createdAfter.Equal(createdBefore) || !updatedAfter.Equal(updatedBefore) || amount != "1000082.000000" || notBatch || !isOpen {
		t.Fatalf("unchanged row fetched=%s created=%s/%s updated=%s/%s amount=%s flags=%t/%t", fetchedAt, createdBefore, createdAfter, updatedBefore, updatedAfter, amount, notBatch, isOpen)
	}

	publish(second, map[string][]string{"A": {}, "B": {}})
	if got := finalSavingBlockKeys(t, db.DB); len(got) != 0 {
		t.Fatalf("all-empty publication left %v", got)
	}
	publish(first, map[string][]string{"A": {"a0"}})
	publish(second, map[string][]string{})
	if got := finalSavingBlockKeys(t, db.DB); len(got) != 0 {
		t.Fatalf("empty enumeration publication left %v", got)
	}

	var foreignKeys int
	if err := db.Get(&foreignKeys, `SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS
		WHERE CONSTRAINT_SCHEMA=DATABASE() AND (TABLE_NAME='fincloud_saving_block_transactions' OR REFERENCED_TABLE_NAME='fincloud_saving_block_transactions')`); err != nil || foreignKeys != 0 {
		t.Fatalf("final table foreign keys=%d error=%v", foreignKeys, err)
	}
}

func TestSavingBlockPublicationRefusesIncompleteOrUnownedCandidates(t *testing.T) {
	db := integrationdb.Open(t)
	resetSavingBlock(t, db.DB)
	repository := NewDetailRepository(db)
	fetched := time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)
	seedID, seedOwner := detailRun(t, db.DB, "saving_block_detail", 1)
	if err := repository.StageSavingBlockTransactions(context.Background(), seedID, "A", fetched, savingBlockItems(t, "A", "a0", "a1")); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSavingBlockTransactions(context.Background(), seedID, seedOwner, 2); err != nil {
		t.Fatal(err)
	}
	// An uncertain-outcome replay of an already committed publication is success.
	if err := repository.PublishSavingBlockTransactions(context.Background(), seedID, seedOwner, 2); err != nil {
		t.Fatalf("committed publication replay: %v", err)
	}

	runID, owner := detailRun(t, db.DB, "saving_block_detail", 1)
	if err := repository.StageSavingBlockTransactions(context.Background(), runID, "B", fetched, savingBlockItems(t, "B", "b0")); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSavingBlockTransactions(context.Background(), runID, owner, 2); err == nil {
		t.Fatal("row count mismatch published")
	}
	if err := repository.PublishSavingBlockTransactions(context.Background(), runID, "other-owner", 1); err == nil {
		t.Fatal("foreign owner published")
	}
	if _, err := db.Exec(`DROP TRIGGER IF EXISTS fail_saving_block_publication`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_saving_block_publication BEFORE INSERT ON fincloud_saving_block_transactions
		FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='forced publication failure'`); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSavingBlockTransactions(context.Background(), runID, owner, 1); err == nil {
		t.Fatal("forced publication failure succeeded")
	}
	if _, err := db.Exec(`DROP TRIGGER fail_saving_block_publication`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE ingestion_runs SET cancel_requested_at=CURRENT_TIMESTAMP(6) WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSavingBlockTransactions(context.Background(), runID, owner, 1); err == nil {
		t.Fatal("cancelled run published")
	}
	if got := finalSavingBlockKeys(t, db.DB); !reflect.DeepEqual(got, []string{"A:0:a0", "A:1:a1"}) {
		t.Fatalf("refused publications changed final state: %v", got)
	}
	var status string
	if err := db.Get(&status, `SELECT status FROM ingestion_runs WHERE id=?`, runID); err != nil || status != "running" {
		t.Fatalf("refused run status=%q error=%v", status, err)
	}
}

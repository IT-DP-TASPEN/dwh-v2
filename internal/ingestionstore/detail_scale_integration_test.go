//go:build integration && detail_scale

package ingestionstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/testutil/integrationdb"
	"github.com/shopspring/decimal"
)

func TestDetailPublicationAtObservedScale(t *testing.T) {
	db := integrationdb.Open(t)
	repository := NewDetailRepository(db)
	tests := []struct {
		name                                                     string
		domain                                                   ingestion.DetailDomain
		parents, statements, mutations, fees, schedules, history int
	}{
		{name: "cif", domain: ingestion.DetailCIF, parents: 36_851},
		{name: "saving", domain: ingestion.DetailSaving, parents: 38_260, statements: 38_260},
		{name: "time_deposit", domain: ingestion.DetailTimeDeposit, parents: 4_202, mutations: 81_177},
		{name: "loan", domain: ingestion.DetailLoan, parents: 13_455, fees: 13_913, schedules: 1_378_595, history: 236_253},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetDetail(t, db.DB)
			specification, err := detailSpecification(test.domain)
			if err != nil {
				t.Fatal(err)
			}
			runID, owner := detailRun(t, db.DB, specification.jobKey, uint64(test.parents))
			stageStarted := time.Now()
			stageScaleDetails(t, repository, runID, test)
			t.Logf("staged %d %s parents in %s", test.parents, test.name, time.Since(stageStarted))
			publishStarted := time.Now()
			if err := repository.Publish(context.Background(), runID, owner, test.domain, uint64(test.parents)); err != nil {
				t.Fatal(err)
			}
			t.Logf("published %d %s parents in %s", test.parents, test.name, time.Since(publishStarted))
			var parents int
			if err := db.Get(&parents, "SELECT COUNT(*) FROM `"+specification.table+"`"); err != nil || parents != test.parents {
				t.Fatalf("parents=%d want=%d error=%v", parents, test.parents, err)
			}
			for _, child := range specification.children {
				want := map[string]int{ingestion.SavingAccountStatementChildKey: test.statements, "mutasideposito": test.mutations, "biayapencairan": test.fees, "jadwalangsuran": test.schedules, "historybayar": test.history}[child.key]
				var count int
				if err := db.Get(&count, "SELECT COUNT(*) FROM `"+child.table+"`"); err != nil || count != want {
					t.Fatalf("%s rows=%d want=%d error=%v", child.table, count, want, err)
				}
			}
			for _, extension := range specification.extensions {
				var count int
				if err := db.Get(&count, "SELECT COUNT(*) FROM `"+extension.table+"`"); err != nil || count != test.parents {
					t.Fatalf("%s rows=%d want=%d error=%v", extension.table, count, test.parents, err)
				}
			}
			cleanupStarted := time.Now()
			if err := repository.CleanupRun(context.Background(), runID); err != nil {
				t.Fatal(err)
			}
			t.Logf("cleaned %s staging in %s", test.name, time.Since(cleanupStarted))
		})
	}
}

func stageScaleDetails(t *testing.T, repository *DetailRepository, runID uint64, test struct {
	name                                                     string
	domain                                                   ingestion.DetailDomain
	parents, statements, mutations, fees, schedules, history int
}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make(chan int)
	errorsFound := make(chan error, 1)
	var workers sync.WaitGroup
	for range 3 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if err := repository.Stage(ctx, runID, scaleDetailRecord(test.domain, index,
					distributedCount(test.statements, test.parents, index),
					distributedCount(test.mutations, test.parents, index), distributedCount(test.fees, test.parents, index),
					distributedCount(test.schedules, test.parents, index), distributedCount(test.history, test.parents, index))); err != nil {
					select {
					case errorsFound <- err:
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range test.parents {
			select {
			case jobs <- index:
			case <-ctx.Done():
				return
			}
		}
	}()
	workers.Wait()
	select {
	case err := <-errorsFound:
		t.Fatal(err)
	default:
	}
}

func distributedCount(total, parents, index int) int {
	if total == 0 {
		return 0
	}
	count := total / parents
	if index < total%parents {
		count++
	}
	return count
}

func scaleDetailRecord(domain ingestion.DetailDomain, index, statements, mutations, fees, schedules, history int) ingestion.DetailRecord {
	identifier := fmt.Sprintf("%s-%06d", domain, index)
	fields := map[string]any{}
	switch domain {
	case ingestion.DetailCIF:
		fields["customer_name"] = "name"
	case ingestion.DetailSaving:
		fields["cif_no"], fields["beginning_balance"], fields["balance"] = "cif", decimal.NewFromInt(1), decimal.NewFromInt(1)
	case ingestion.DetailTimeDeposit:
		fields["cif_no"], fields["nominal"] = "cif", decimal.NewFromInt(1)
	case ingestion.DetailLoan:
		fields["cif_no"] = "cif"
	}
	record := ingestion.DetailRecord{
		Domain: domain, Identifier: identifier, LastFetchedAt: time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC), Fields: fields,
		RawPayload: json.RawMessage(`{"scale":true}`), RawChecksum: strings.Repeat("0", 64),
		Children: map[string][]ingestion.DetailChildRecord{}, Sections: map[string]map[string]any{},
	}
	if domain == ingestion.DetailCIF {
		record.Sections = map[string]map[string]any{
			"personal_profile": {"birth_place": "x"}, "ktp": {"ktp_name": "x"}, "addresses": {"ktp_city": "x"},
			"employment": {"work_type": "x"}, "company": {"company_npwp_no": "x"}, "kyc": {"kyc_source_of_funds": "x"},
			"regulatory": {"risk_profile": "x"},
		}
	}
	record.Children["mutasideposito"] = scaleChildren(identifier, mutations)
	record.Children["biayapencairan"] = scaleChildren(identifier, fees)
	record.Children["jadwalangsuran"] = scaleChildren(identifier, schedules)
	record.Children["historybayar"] = scaleChildren(identifier, history)
	if domain == ingestion.DetailSaving {
		record.Children[ingestion.SavingAccountStatementChildKey] = scaleChildren(identifier, statements)
	}
	return record
}

func scaleChildren(identifier string, count int) []ingestion.DetailChildRecord {
	children := make([]ingestion.DetailChildRecord, count)
	for index := range children {
		children[index] = ingestion.DetailChildRecord{
			Identifier: identifier, ItemIndex: index, Fields: map[string]any{}, RawItemPayload: json.RawMessage(`{}`),
			RawItemChecksum: strings.Repeat("0", 64),
		}
	}
	return children
}

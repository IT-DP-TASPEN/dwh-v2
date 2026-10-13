package ingestion

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/ibldzn/go-admin/internal/fincloud"
)

const savingBlockAccount = "1130080000000037"

func savingBlockPayload(items string) *fincloud.SavingBlockDetail {
	return &fincloud.SavingBlockDetail{RawPayload: []byte(`{"total_bo_open":9,"norekening":"` + savingBlockAccount + `","namarek":"PARENT ONLY","saldoblokir":"1.00","datablockopen":` + items + `}`)}
}

func TestMapSavingBlockDetailMapsTypedItemsInSourceOrder(t *testing.T) {
	items, err := MapSavingBlockDetail(context.Background(), savingBlockAccount, savingBlockPayload(`[
		{"id_transaction":"BTT251015006k","tgltransaksi":"2025-10-15","namanasabah":"LAILA RAFIKA","tglblokir":"2025-10-15","tglakhirblokir":"2026-10-01","norekening":"1130080000000037","currency":"IDR","keterangan":"Blokir Saldo","alasanblokir":"Blokir Saldo DPentas Vaganza","jenistransaksi":"Block","nilaiblokir":"1,000,000.00","officer":"2107003_Haytsam","not_batch":false,"is_open":true},
		{"id_transaction":"BT251014003m","tgltransaksi":"2025-10-14","norekening":"1130080000000037","keterangan":null,"nilaiblokir":"1,000,082.00","not_batch":true,"is_open":false},
		{"norekening":"1130080000000037","nilaiblokir":1000082}
	]`))
	if err != nil || len(items) != 3 {
		t.Fatalf("items=%+v error=%v", items, err)
	}
	first := items[0].Fields
	want := map[string]any{
		"transaction_id": "BTT251015006k", "customer_name": "LAILA RAFIKA", "currency": "IDR", "description": "Blokir Saldo",
		"block_reason": "Blokir Saldo DPentas Vaganza", "transaction_type": "Block", "officer": "2107003_Haytsam", "not_batch": false, "is_open": true,
	}
	for key, value := range want {
		if first[key] != value {
			t.Fatalf("%s=%#v want %#v", key, first[key], value)
		}
	}
	for key, value := range map[string]string{"transaction_date": "2025-10-15", "block_date": "2025-10-15", "block_end_date": "2026-10-01"} {
		if date, ok := first[key].(CalendarDate); !ok || date.String() != value {
			t.Fatalf("%s=%#v", key, first[key])
		}
	}
	for index, amount := range []string{"1000000.000000", "1000082.000000", "1000082"} {
		if got, ok := items[index].Fields["blocked_amount"].(decimal.Decimal); !ok || !got.Equal(decimal.RequireFromString(amount)) {
			t.Fatalf("item %d blocked_amount=%#v", index, items[index].Fields["blocked_amount"])
		}
		if items[index].ItemIndex != index || items[index].Identifier != savingBlockAccount || items[index].RawItemChecksum != sha256Hex(items[index].RawItemPayload) {
			t.Fatalf("item %d identity=%+v", index, items[index])
		}
	}
	second := items[1].Fields
	if second["transaction_id"] != "BT251014003m" || second["description"] != nil || second["customer_name"] != nil || second["not_batch"] != true || second["is_open"] != false {
		t.Fatalf("second=%#v", second)
	}
	if items[2].Fields["not_batch"] != nil || items[2].Fields["transaction_date"] != nil {
		t.Fatalf("absent optional fields=%#v", items[2].Fields)
	}
	if string(items[1].RawItemPayload) != `{"id_transaction":"BT251014003m","is_open":false,"keterangan":null,"nilaiblokir":"1,000,082.00","norekening":"1130080000000037","not_batch":true,"tgltransaksi":"2025-10-14"}` {
		t.Fatalf("raw=%s", items[1].RawItemPayload)
	}
}

func TestMapSavingBlockDetailEmptyArrayIsValid(t *testing.T) {
	items, err := MapSavingBlockDetail(context.Background(), savingBlockAccount, savingBlockPayload(`[]`))
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("items=%v error=%v", items, err)
	}
}

func TestMapSavingBlockDetailRejectsInvalidContracts(t *testing.T) {
	for name, detail := range map[string]*fincloud.SavingBlockDetail{
		"nil":                nil,
		"null result":        {RawPayload: []byte(`null`)},
		"missing parent id":  {RawPayload: []byte(`{"datablockopen":[]}`)},
		"parent mismatch":    {RawPayload: []byte(`{"norekening":"OTHER","datablockopen":[]}`)},
		"missing items":      {RawPayload: []byte(`{"norekening":"` + savingBlockAccount + `"}`)},
		"null items":         savingBlockPayload(`null`),
		"object items":       savingBlockPayload(`{}`),
		"non-object item":    savingBlockPayload(`["x"]`),
		"child mismatch":     savingBlockPayload(`[{"norekening":"OTHER"}]`),
		"child missing acct": savingBlockPayload(`[{"id_transaction":"X"}]`),
		"text boolean":       savingBlockPayload(`[{"norekening":"` + savingBlockAccount + `","not_batch":"false"}]`),
		"numeric boolean":    savingBlockPayload(`[{"norekening":"` + savingBlockAccount + `","is_open":0}]`),
		"loose date":         savingBlockPayload(`[{"norekening":"` + savingBlockAccount + `","tglblokir":"2025-10-15 00:00:00"}]`),
		"bad decimal":        savingBlockPayload(`[{"norekening":"` + savingBlockAccount + `","nilaiblokir":"1,00"}]`),
		"empty decimal":      savingBlockPayload(`[{"norekening":"` + savingBlockAccount + `","nilaiblokir":""}]`),
		"numeric text":       savingBlockPayload(`[{"norekening":"` + savingBlockAccount + `","officer":7}]`),
	} {
		_, err := MapSavingBlockDetail(context.Background(), savingBlockAccount, detail)
		var mapper *MapperError
		if !errors.As(err, &mapper) {
			t.Fatalf("%s error=%v", name, err)
		}
		metadata := mapper.Metadata()
		if !IsSafeMapperDiagnostic(metadata.Class(), metadata.Field(), metadata.Category(), metadata.Reason(), metadata.SafeMessage()) {
			t.Fatalf("%s unsafe diagnostic=%+v", name, metadata)
		}
	}
}

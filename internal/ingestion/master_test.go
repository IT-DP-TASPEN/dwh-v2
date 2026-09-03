package ingestion

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizeReferencePreservesSourceShapeDuplicatesAndOrdinalGaps(t *testing.T) {
	candidate, err := NormalizeReference(ReferenceCIF, map[string]json.RawMessage{
		"objects": json.RawMessage(`[{"id":" ","descr":""},{"descr":"Alpha","extra":1,"id":"A"},{"id":"A","descr":"Beta"}]`),
		"strings": json.RawMessage(`[" ","AB","AB"]`),
		"empty":   json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Categories) != 3 || candidate.ItemCount != 4 || candidate.DiscardedBlankCount != 2 {
		t.Fatalf("candidate=%+v", candidate)
	}
	byKey := map[string]ReferenceCategory{}
	for _, category := range candidate.Categories {
		byKey[category.Key] = category
	}
	objects := byKey["objects"]
	if objects.Shape != ShapeIDDescription || objects.SourceItemCount != 3 || objects.Items[0].SourceOrdinal != 1 || objects.Items[1].Code != "A" || objects.Items[1].Description != "Beta" || string(objects.Items[0].RawItemPayload) != `{"descr":"Alpha","extra":1,"id":"A"}` {
		t.Fatalf("objects=%+v", objects)
	}
	if byKey["strings"].Shape != ShapeStringArray || byKey["strings"].Items[0].SourceOrdinal != 1 || byKey["strings"].Items[0].Code != byKey["strings"].Items[0].Description {
		t.Fatalf("strings=%+v", byKey["strings"])
	}
	if byKey["empty"].Shape != ShapeEmptyArray || byKey["empty"].ItemCount != 0 {
		t.Fatalf("empty=%+v", byKey["empty"])
	}
}

func TestNormalizeReferenceRejectsMalformedCompleteCandidate(t *testing.T) {
	for name, raw := range map[string]string{
		"non_array": `{}`, "mixed": `[{"id":"1","descr":"one"},"two"]`, "missing_id": `[{"descr":"one"}]`,
		"missing_descr": `[{"id":"1"}]`, "numeric_id": `[{"id":1,"descr":"one"}]`, "numeric_descr": `[{"id":"1","descr":1}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeReference(ReferenceCIF, map[string]json.RawMessage{"valid": json.RawMessage(`[]`), "bad": json.RawMessage(raw)}); err == nil {
				t.Fatal("malformed candidate accepted")
			}
		})
	}
}

func TestNormalizeMarketingIdentityAndOrder(t *testing.T) {
	first := json.RawMessage(`{"id":"001","nama_marketing":"Same","locationname":"HQ","aktif":"1","status_dokumen":"ok","tgltransaksi":"source","extra":1}`)
	second := json.RawMessage(`{"id":"002","nama_marketing":"Same","locationname":"Branch","aktif":"1","status_dokumen":"ok","tgltransaksi":"source"}`)
	a, err := NormalizeMarketing([]json.RawMessage{second, first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NormalizeMarketing([]json.RawMessage{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if a.Checksum != b.Checksum || !reflect.DeepEqual(a.Entities, b.Entities) || a.Entities[0].ID != "001" || string(a.Entities[0].RawPayload) != `{"aktif":"1","extra":1,"id":"001","locationname":"HQ","nama_marketing":"Same","status_dokumen":"ok","tgltransaksi":"source"}` {
		t.Fatalf("marketing a=%+v b=%+v", a, b)
	}
	for name, rows := range map[string][]json.RawMessage{"blank": {json.RawMessage(`{"id":"","nama_marketing":"x","locationname":"x","aktif":"1","status_dokumen":"x","tgltransaksi":"x"}`)}, "duplicate": {first, first}, "missing": {json.RawMessage(`{"id":"1"}`)}} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeMarketing(rows); err == nil {
				t.Fatal("invalid Marketing accepted")
			}
		})
	}
}

func TestReferenceDatasetSortsCategoriesButPreservesItemOrder(t *testing.T) {
	first, _ := NormalizeReference(ReferenceSaving, map[string]json.RawMessage{"b": json.RawMessage(`[]`), "a": json.RawMessage(`["A","B"]`)})
	second, _ := NormalizeReference(ReferenceSaving, map[string]json.RawMessage{"a": json.RawMessage(`["A","B"]`), "b": json.RawMessage(`[]`)})
	reordered, _ := NormalizeReference(ReferenceSaving, map[string]json.RawMessage{"b": json.RawMessage(`[]`), "a": json.RawMessage(`["B","A"]`)})
	if first.Checksum != second.Checksum {
		t.Fatal("map/category iteration changed reference checksum")
	}
	if first.Checksum == reordered.Checksum {
		t.Fatal("authoritative source ordinal did not change reference checksum")
	}
	if ReferenceDatasetChecksum([]ReferenceCategory{first.Categories[1], first.Categories[0]}) != first.Checksum {
		t.Fatal("checksum helper did not impose binary category order")
	}
	reversedItems := append([]ReferenceItem(nil), first.Categories[0].Items...)
	reversedItems[0], reversedItems[1] = reversedItems[1], reversedItems[0]
	category := first.Categories[0]
	category.Items = reversedItems
	if ReferenceCategoryChecksum(category) != first.Categories[0].Checksum {
		t.Fatal("category checksum helper did not impose ordinal order")
	}
}

func TestCanonicalJSONNormalizesNumericRendering(t *testing.T) {
	first, err := CanonicalJSON([]byte(`{"b":1.00,"a":1e0}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalJSON([]byte(`{"a":1,"b":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || string(first) != `{"a":1,"b":1}` {
		t.Fatalf("canonical numbers first=%s second=%s", first, second)
	}
}

func TestMasterChecksumGoldens(t *testing.T) {
	reference, err := NormalizeReference(ReferenceCIF, map[string]json.RawMessage{
		"normal":    json.RawMessage(`[{"id":"1","descr":"One"}]`),
		"gap":       json.RawMessage(`[{"id":"","descr":""},{"id":"A","descr":"Alpha"}]`),
		"conflict":  json.RawMessage(`[{"id":"X","descr":"First"},{"id":"X","descr":"Second"}]`),
		"identical": json.RawMessage(`[{"id":"Y","descr":"Same"},{"id":"Y","descr":"Same"}]`),
		"strings":   json.RawMessage(`["A","B"]`), "empty": json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	marketing, err := NormalizeMarketing([]json.RawMessage{json.RawMessage(`{"id":"002","nama_marketing":"B","locationname":"L","aktif":"1","status_dokumen":"D","tgltransaksi":"T"}`), json.RawMessage(`{"id":"001","nama_marketing":"A","locationname":"L","aktif":"1","status_dokumen":"D","tgltransaksi":"T"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := ChecksumHex(reference.Checksum); got != "ec3f9bdea114f363291efdc4d2785304a95d144230200e2542f8781b04d6eecf" {
		t.Fatalf("reference checksum=%s", got)
	}
	wants := map[string]string{
		"conflict":  "a52e31beb9b9de4f3145acbe415b1a6b83f2d420cb033e5f50bab408ceb2e634",
		"empty":     "9c4cd42d98c02f11adc8b3a2946e4dec40f0a634476455478c05577fccf8b4a5",
		"gap":       "1f48fa433cb13e8096d77a1a2c3e85c29713484c2d6f467a415885eb461e28f5",
		"identical": "70dc41e49b89c6a923c4608297ea2f8041cd4ef645b1ced74a8986d54bc7085d",
		"normal":    "f4a695ff10538d4cbd27ee935e532d10f66202b1b2b96026deea8026b4507af1",
		"strings":   "76c1c31f37d037ed65f7f61d179ca398a13e0b873324e3586dd2848582b194ef",
	}
	for _, category := range reference.Categories {
		if got := ChecksumHex(category.Checksum); got != wants[category.Key] {
			t.Errorf("%s checksum=%s", category.Key, got)
		}
	}
	if got := ChecksumHex(marketing.Checksum); got != "e9cabfe76c5cec1fdc257319ed76fc02234f6d124fae7282dcde8c6c2a2859d8" {
		t.Fatalf("marketing checksum=%s", got)
	}
}

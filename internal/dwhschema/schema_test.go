package dwhschema

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

func TestSourceSettingsMigrationSeedsCanonicalCatalog(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*_create_dwh_source_settings.sql"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("source settings migration matches=%v error=%v", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`\('([a-z][a-z0-9_]*)', TRUE, NULL\)`)
	matched := pattern.FindAllStringSubmatch(string(data), -1)
	got := make([]string, len(matched))
	for index := range matched {
		got[index] = matched[index][1]
	}
	want, err := CanonicalSourceKeys()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("migration seeds %d keys, catalog has %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("seed[%d]=%q want %q", index, got[index], want[index])
		}
	}
}

package dwhschema

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

func TestApplicationVersionsMatchMigrationFiles(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int64, 0, len(matches))
	for _, match := range matches {
		name := filepath.Base(match)
		separator := regexp.MustCompile(`_`).FindStringIndex(name)
		if separator == nil {
			t.Fatalf("migration filename has no version separator: %s", name)
		}
		version, err := strconv.ParseInt(name[:separator[0]], 10, 64)
		if err != nil {
			t.Fatalf("parse migration version %s: %v", name, err)
		}
		got = append(got, version)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != len(ApplicationVersions) {
		t.Fatalf("migration files=%v application versions=%v", got, ApplicationVersions)
	}
	for index := range got {
		if got[index] != ApplicationVersions[index] {
			t.Fatalf("migration files=%v application versions=%v", got, ApplicationVersions)
		}
	}
	if ApplicationVersions[len(ApplicationVersions)-1] != CurrentVersion {
		t.Fatalf("current version=%d want %d", CurrentVersion, ApplicationVersions[len(ApplicationVersions)-1])
	}
}

func TestValidateMigrationPrefix(t *testing.T) {
	complete := MigrationState{TableExists: true, Records: []MigrationRecord{{Version: 0, Applied: true}}}
	for _, version := range ApplicationVersions {
		complete.Records = append(complete.Records, MigrationRecord{Version: version, Applied: true})
	}
	if count, err := ValidateMigrationPrefix(complete); err != nil || count != len(ApplicationVersions) {
		t.Fatalf("complete count=%d error=%v", count, err)
	}
	prefix := MigrationState{TableExists: true, Records: append([]MigrationRecord(nil), complete.Records[:len(complete.Records)-1]...)}
	if count, err := ValidateMigrationPrefix(prefix); err != nil || count != len(ApplicationVersions)-1 {
		t.Fatalf("prefix count=%d error=%v", count, err)
	}
	unknown := MigrationState{TableExists: true, Records: append([]MigrationRecord(nil), prefix.Records...)}
	unknown.Records = append(unknown.Records, MigrationRecord{Version: 999, Applied: true})
	if _, err := ValidateMigrationPrefix(unknown); err == nil {
		t.Fatal("unknown migration accepted")
	}
	gapped := MigrationState{TableExists: true, Records: append([]MigrationRecord(nil), complete.Records...)}
	gapped.Records = append(gapped.Records, MigrationRecord{Version: ApplicationVersions[2], Applied: false})
	if _, err := ValidateMigrationPrefix(gapped); err == nil {
		t.Fatal("gapped migration history accepted")
	}
}

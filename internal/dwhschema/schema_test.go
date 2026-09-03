package dwhschema

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestSourceSettingsMigrationSeedsCanonicalCatalog(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("source settings migration matches=%v error=%v", matches, err)
	}
	pattern := regexp.MustCompile(`\('([a-z][a-z0-9_]*)', TRUE, NULL\)`)
	var got []string
	for _, match := range matches {
		data, err := os.ReadFile(match)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range pattern.FindAllStringSubmatch(string(data), -1) {
			got = append(got, row[1])
		}
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

func TestMasterMigrationsKeepStagingUncoupledAndRenameOnlyExecutableState(t *testing.T) {
	root := filepath.Join("..", "..", "migrations")
	storage, err := os.ReadFile(filepath.Join(root, "20260903091000_create_master_reference_ingestion.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(storage)
	for _, table := range []string{"stg_fincloud_reference_categories", "stg_fincloud_reference_items", "stg_fincloud_marketing_master"} {
		start := strings.Index(text, "CREATE TABLE "+table)
		if start < 0 {
			t.Fatalf("missing %s", table)
		}
		end := strings.Index(text[start:], ") ENGINE=InnoDB")
		if end < 0 {
			t.Fatalf("unterminated %s", table)
		}
		if strings.Contains(text[start:start+end], "FOREIGN KEY") {
			t.Fatalf("%s has a staging foreign key", table)
		}
	}
	if !strings.Contains(text, "fk_fincloud_reference_items_category") || !strings.Contains(text, "ON DELETE CASCADE") {
		t.Fatal("current reference category integrity is missing")
	}
	rename, err := os.ReadFile(filepath.Join(root, "20260903090000_rename_live_snapshot.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"archived_at IS NULL", "status='unresolved'", "status IN ('planned','queued','running')", "detail_live_snapshot_v1", "live_snapshot_v1", "6aabf8e855d1dd0a653754257a4e4bce0380ccf0bb1734b4dc50de2b1f5ec60a"} {
		if !strings.Contains(string(rename), required) {
			t.Fatalf("live-snapshot migration missing %q", required)
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

package reporting

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx/reflectx"
)

func TestUserReportFolderSQLMapping(t *testing.T) {
	columns := []string{"id", "name", "visible_report_count"}
	mappings := reflectx.NewMapperFunc("db", strings.ToLower).TraversalsByName(reflect.TypeOf(UserReportFolder{}), columns)
	for index, mapping := range mappings {
		if len(mapping) == 0 {
			t.Fatalf("column %q has no UserReportFolder destination", columns[index])
		}
	}
}

func TestFolderNameValidationAndLikeEscaping(t *testing.T) {
	if got := NormalizeFolderName("  Kredit  "); got != "Kredit" {
		t.Fatalf("normalized name=%q", got)
	}
	for _, invalid := range []string{"", "   ", string([]byte{0xff}), strings.Repeat("x", MaxFolderNameLength+1)} {
		if err := ValidateFolderName(invalid); err == nil {
			t.Fatalf("invalid folder name accepted: %q", invalid)
		}
	}
	if err := ValidateFolderName(strings.Repeat("界", MaxFolderNameLength)); err != nil {
		t.Fatalf("valid maximum folder name rejected: %v", err)
	}
	if got := escapeReportLike("100%_ok!"); got != "100!%!_ok!!" {
		t.Fatalf("escaped search=%q", got)
	}
}

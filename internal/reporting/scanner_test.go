package reporting

import (
	"reflect"
	"strings"
	"testing"
)

func TestScannerHonorsMySQLLexicalModes(t *testing.T) {
	statement := `SELECT :real, ':single', "quoted :double", ` + "`quoted :tick`" + ` -- :dash
# :hash
/* :block */ WHERE x=:real`
	for _, mode := range []SQLMode{{}, {ANSIQuotes: true}, {NoBackslashEscapes: true}, {ANSIQuotes: true, NoBackslashEscapes: true}} {
		found, err := ScanPlaceholders(statement, mode)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 2 || found[0].Key != "real" || found[1].Key != "real" {
			t.Fatalf("mode=%+v placeholders=%+v", mode, found)
		}
	}
	if _, err := ScanPlaceholders(`SELECT 'unterminated`, SQLMode{}); err == nil {
		t.Fatal("unterminated string accepted")
	}
}

func TestScannerANSIQuotesChangesDoubleQuoteEscaping(t *testing.T) {
	statement := `SELECT "a\" :inside" , :outside`
	withoutANSI, err := ScanPlaceholders(statement, SQLMode{})
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutANSI) != 1 || withoutANSI[0].Key != "outside" {
		t.Fatalf("string mode=%+v", withoutANSI)
	}
	if _, err := ScanPlaceholders(statement, SQLMode{ANSIQuotes: true}); err == nil {
		t.Fatal("ANSI identifier mode should reject the unmatched quote")
	}
}

func TestScannerDashCommentRequiresWhitespace(t *testing.T) {
	found, err := ScanPlaceholders("SELECT 1--2, :value\n, :after", SQLMode{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("placeholders=%+v", found)
	}
}

func TestScannerNoBackslashEscapesChangesStringRules(t *testing.T) {
	statement := `SELECT 'a\' :inside' , :outside`
	found, err := ScanPlaceholders(statement, SQLMode{})
	if err != nil || len(found) != 1 || found[0].Key != "outside" {
		t.Fatalf("backslash mode placeholders=%+v error=%v", found, err)
	}
	if _, err := ScanPlaceholders(statement, SQLMode{NoBackslashEscapes: true}); err == nil {
		t.Fatal("NO_BACKSLASH_ESCAPES accepted unmatched quote")
	}
}

func TestOptionalBlockGoldenLexingAndCompilation(t *testing.T) {
	prefix := `SELECT '[[ :fake_single ]]', "[[ :fake_double ]]", ` + "`column[[ :fake_tick ]]`" + `
-- [[ :fake_dash ]]
# [[ :fake_hash ]]
/* [[ :fake_block ]] */
WHERE report_date=:report_date`
	realBlock := `[[ AND product_id=:product ]]`
	statement := prefix + realBlock
	compiledPrefix := strings.Replace(prefix, ":report_date", "?", 1)
	parameters := []Parameter{
		{Key: "report_date", Label: "Report date", Type: ParameterDate, Required: true, DisplayOrder: 0},
		{Key: "product", Label: "Product", Type: ParameterText, DisplayOrder: 1},
	}
	for _, mode := range []SQLMode{{}, {ANSIQuotes: true}, {NoBackslashEscapes: true}, {ANSIQuotes: true, NoBackslashEscapes: true}} {
		scan, err := scanStatement(statement, mode)
		if err != nil {
			t.Fatalf("mode=%+v: %v", mode, err)
		}
		if len(scan.OptionalBlocks) != 1 || len(scan.Placeholders) != 2 || scan.Placeholders[0].Key != "report_date" || scan.Placeholders[1].Key != "product" {
			t.Fatalf("mode=%+v scan=%+v", mode, scan)
		}
		values := map[string]NormalizedValue{"report_date": {Scalar: "2026-09-07"}, "product": {}}
		query, arguments, err := Bind(statement, parameters, values, mode)
		if err != nil || query != compiledPrefix+" " || !reflect.DeepEqual(arguments, []any{"2026-09-07"}) {
			t.Fatalf("mode=%+v omitted query=%q arguments=%#v error=%v", mode, query, arguments, err)
		}
		values["product"] = NormalizedValue{Scalar: "KPR"}
		query, arguments, err = Bind(statement, parameters, values, mode)
		if err != nil || query != compiledPrefix+` AND product_id=? ` || !reflect.DeepEqual(arguments, []any{"2026-09-07", "KPR"}) {
			t.Fatalf("mode=%+v included query=%q arguments=%#v error=%v", mode, query, arguments, err)
		}
	}
}

func TestOptionalBlockScannerUsesSQLMode(t *testing.T) {
	statement := `SELECT 'escaped\' [[ AND product=:product ]]', :required`
	scan, err := scanStatement(statement, SQLMode{})
	if err != nil || len(scan.OptionalBlocks) != 0 || len(scan.Placeholders) != 1 || scan.Placeholders[0].Key != "required" {
		t.Fatalf("backslash mode scan=%+v error=%v", scan, err)
	}
	if _, err := scanStatement(statement, SQLMode{NoBackslashEscapes: true}); err == nil {
		t.Fatal("NO_BACKSLASH_ESCAPES accepted lexical shape parsed under backslash escapes")
	}
}

package reporting

import "testing"

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

package reporting

import (
	"errors"
	"testing"
)

func TestReadOnlyStatementPolicy(t *testing.T) {
	accepted := []string{
		"SELECT 1", "SELECT LAST_INSERT_ID()", "SELECT `GET_LOCK` FROM t", "select 1", "SeLeCt 1;", "/* UPDATE */ -- DELETE\n# INSERT\n SELECT 1",
		"WITH c AS (SELECT 1) SELECT * FROM c",
		"with recursive c(n) as (select 1 union all select n+1 from c where n<3) select * from c",
		"WITH `update` AS (SELECT 1), second AS (WITH x AS (SELECT 2) SELECT * FROM x) SELECT * FROM second",
		"SELECT 'INTO OUTFILE FOR UPDATE', \"LOCK IN SHARE MODE\", `DELETE`, `INTO`, `FOR` /* INTO DUMPFILE */",
		"SELECT 'a'' FOR UPDATE', `a`` DELETE`",
	}
	for _, mode := range []SQLMode{{}, {ANSIQuotes: true}, {NoBackslashEscapes: true}, {ANSIQuotes: true, NoBackslashEscapes: true}} {
		for _, sql := range accepted {
			if err := ValidateReadOnlyStatement(sql, mode); err != nil {
				t.Errorf("mode=%+v rejected %q: %v", mode, sql, err)
			}
		}
	}
	rejected := []string{
		"", "-- SELECT 1", "UPDATE t SET x=1", "INSERT INTO t VALUES(1)", "DELETE FROM t", "REPLACE INTO t VALUES(1)",
		"MERGE INTO t", "CREATE TABLE t(x INT)", "ALTER TABLE t ADD x INT", "DROP TABLE t", "TRUNCATE t", "RENAME TABLE t TO x",
		"CALL p()", "DO 1", "SET @x=1", "LOAD DATA INFILE 'x' INTO TABLE t", "LOCK TABLES t READ", "UNLOCK TABLES",
		"GRANT SELECT ON t TO x", "REVOKE SELECT ON t FROM x", "ANALYZE TABLE t", "OPTIMIZE TABLE t", "REPAIR TABLE t", "FLUSH TABLES", "RESET MASTER", "KILL 1", "SHOW TABLES",
		"SELECT 1 INTO OUTFILE '/tmp/x'", "SELECT 1 INTO DUMPFILE '/tmp/x'", "SELECT 1 INTO @x",
		"SELECT * FROM t FOR UPDATE", "SELECT * FROM t LOCK IN SHARE MODE", "SELECT * FROM t FOR SHARE",
		"SELECT * FROM (SELECT * FROM t FOR UPDATE) s", "SELECT 1; SELECT 2", "SELECT 1;;",
		"WITH c AS (SELECT 1) UPDATE t SET x=1", "WITH c AS (DELETE FROM t) SELECT * FROM c",
		"SELECT 1 /*! INTO OUTFILE '/tmp/x' */", "/*! SELECT 1 */", "SELECT GET_LOCK('x',0)", "SELECT @x:=1", "SELECT `GET_LOCK`('x',0)", "SELECT `get_lock`('x',0)", "SELECT LAST_INSERT_ID(123)", "SELECT `LAST_INSERT_ID`(123)",
		"SELECT 'unterminated", "SELECT (1", "SELECT 1)",
	}
	for _, sql := range rejected {
		if err := ValidateReadOnlyStatement(sql, SQLMode{}); !errors.Is(err, ErrInvalid) || err.Error() != ErrInvalid.Error() {
			t.Errorf("unsafe statement %q: %v", sql, err)
		}
	}
	// Backslash behavior must use the same mode as MySQL's session.
	if err := ValidateReadOnlyStatement(`SELECT 'a\' INTO OUTFILE x'`, SQLMode{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReadOnlyStatement(`SELECT 'a\' INTO OUTFILE x'`, SQLMode{NoBackslashEscapes: true}); err == nil {
		t.Fatal("mode bypass")
	}
}

func TestDynamicOptionsUseReadOnlyPolicy(t *testing.T) {
	parameters := []Parameter{{Key: "choice", Label: "Choice", Type: ParameterSingleOption, OptionSource: OptionSourceDynamic, DynamicOptionSQL: "DELETE FROM t"}}
	if err := ValidateTemplateBinding("SELECT :choice", parameters, SQLMode{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe option: %v", err)
	}
	if _, _, err := Bind("UPDATE t SET x=:choice", parameters, map[string]NormalizedValue{"choice": {Scalar: "x"}}, SQLMode{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe execution: %v", err)
	}
	parameters[0].DynamicOptionSQL = "SELECT 'x' AS value,'X' AS label"
	if err := ValidateTemplateBinding("SELECT :choice", parameters, SQLMode{}); err != nil {
		t.Fatal(err)
	}
}

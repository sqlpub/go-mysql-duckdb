package duckdbstore

import "testing"

func TestValidateReadOnlySQL(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"select count(*) from main.\"order\"",
		"WITH x AS (SELECT 1 AS n) SELECT * FROM x",
		"EXPLAIN SELECT 1",
	}
	for _, sql := range ok {
		if err := validateReadOnlySQL(sql); err != nil {
			t.Fatalf("expected ok for %q: %v", sql, err)
		}
	}
	bad := []string{
		"DELETE FROM t",
		"SELECT 1; SELECT 2",
		"INSERT INTO t VALUES (1)",
		"DROP TABLE t",
	}
	for _, sql := range bad {
		if err := validateReadOnlySQL(sql); err == nil {
			t.Fatalf("expected error for %q", sql)
		}
	}
}

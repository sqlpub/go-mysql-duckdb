package schema

import (
	"strings"
	"testing"
)

func TestMapMySQLType(t *testing.T) {
	cases := map[string]string{
		"int(11)":            "INTEGER",
		"bigint unsigned":    "BIGINT",
		"tinyint(1)":         "BOOLEAN",
		"decimal(12,2)":      "DECIMAL(12,2)",
		"varchar(64)":        "VARCHAR",
		"datetime":           "TIMESTAMP",
		"json":               "JSON",
		"blob":               "BLOB",
		"enum('a','b')":      "VARCHAR",
		"bit(8)":             "BIGINT",
	}
	for in, want := range cases {
		if got := MapMySQLType(in); got != want {
			t.Errorf("MapMySQLType(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCreateTableSQL(t *testing.T) {
	meta := &TableMeta{
		Schema: "demo",
		Name:   "users",
		Columns: []Column{
			{Name: "id", DuckDBType: "BIGINT", Nullable: false, IsPK: true},
			{Name: "name", DuckDBType: "VARCHAR", Nullable: false},
		},
		PKCols: []string{"id"},
	}
	sql := CreateTableSQL(meta)
	if !strings.Contains(sql, `"demo"."users"`) {
		t.Fatalf("missing qualified name: %s", sql)
	}
	if !strings.Contains(sql, `PRIMARY KEY ("id")`) {
		t.Fatalf("missing PK: %s", sql)
	}
}

func TestParseDDL(t *testing.T) {
	a := ParseDDL("CREATE TABLE `demo`.`t1` (id INT PRIMARY KEY)", "demo")
	if a == nil || a.Kind != "create" || a.Table != "t1" {
		t.Fatalf("create: %+v", a)
	}
	a = ParseDDL("ALTER TABLE users ADD COLUMN age INT", "demo")
	if a == nil || a.Kind != "add_column" || a.ColumnName != "age" {
		t.Fatalf("add: %+v", a)
	}
	a = ParseDDL("DROP TABLE IF EXISTS demo.users", "")
	if a == nil || a.Kind != "drop" || a.Schema != "demo" {
		t.Fatalf("drop: %+v", a)
	}
	a = ParseDDL("RENAME TABLE demo.a TO demo.b", "demo")
	if a == nil || a.Kind != "rename" || a.NewTable != "b" {
		t.Fatalf("rename: %+v", a)
	}
}

func TestParseCreateTableColumns(t *testing.T) {
	sql := `CREATE TABLE users (
  id BIGINT NOT NULL PRIMARY KEY,
  name VARCHAR(64) NOT NULL,
  email VARCHAR(128),
  PRIMARY KEY (id)
)`
	cols, pks, err := ParseCreateTableColumns(sql)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 3 {
		t.Fatalf("cols=%d", len(cols))
	}
	if len(pks) == 0 || pks[0] != "id" {
		t.Fatalf("pks=%v", pks)
	}
}

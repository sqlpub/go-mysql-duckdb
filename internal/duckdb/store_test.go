package duckdbstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqlpub/go-mysql-duckdb/internal/schema"
)

func TestStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.duckdb")
	store, err := Open(path, 100, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	meta := &schema.TableMeta{
		Schema: "demo",
		Name:   "users",
		Columns: []schema.Column{
			{Name: "id", DuckDBType: "BIGINT", Nullable: false, IsPK: true},
			{Name: "name", DuckDBType: "VARCHAR", Nullable: false},
		},
		PKCols: []string{"id"},
	}
	if err := store.CreateTable(ctx, meta); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertRows(ctx, meta, [][]any{{int64(1), "alice"}, {int64(2), "bob"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRows(ctx, meta, [][]any{{int64(1), "ALICE"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRows(ctx, meta, [][]any{{int64(2), "bob"}}); err != nil {
		t.Fatal(err)
	}

	var n int
	// file is t.duckdb → catalog "t"; mysql schema "demo" stays schema "demo"
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM "demo"."users"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("count=%d want 1", n)
	}
	var name string
	if err := store.DB().QueryRowContext(ctx, `SELECT name FROM "demo"."users" WHERE id = 1`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "ALICE" {
		t.Fatalf("name=%q", name)
	}

	// collision: mysql schema == catalog → DuckDB schema "main"
	collided := &schema.TableMeta{
		Schema: store.Catalog(),
		Name:   "t1",
		Columns: []schema.Column{
			{Name: "id", DuckDBType: "BIGINT", Nullable: false, IsPK: true},
		},
		PKCols: []string{"id"},
	}
	if err := store.CreateTable(ctx, collided); err != nil {
		t.Fatal(err)
	}
	if collided.DuckSchema != "main" {
		t.Fatalf("DuckSchema=%q want main", collided.DuckSchema)
	}
	if err := store.InsertRows(ctx, collided, [][]any{{int64(1)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM "main"."t1"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("main.t1 count=%d", n)
	}

	col := schema.Column{Name: "age", DuckDBType: "INTEGER", Nullable: true}
	if err := store.AddColumn(ctx, "demo", "users", col); err != nil {
		t.Fatal(err)
	}
	if err := store.DropColumn(ctx, "demo", "users", "age"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameTable(ctx, "demo", "users", "people"); err != nil {
		t.Fatal(err)
	}
	if err := store.DropTable(ctx, "demo", "people"); err != nil {
		t.Fatal(err)
	}
}

func TestInsertLocalTimeTimeKeepsWallClock(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "tz.duckdb"), 100, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	meta := &schema.TableMeta{
		Schema: "demo",
		Name:   "t",
		Columns: []schema.Column{
			{Name: "id", DuckDBType: "BIGINT", Nullable: false, IsPK: true},
			{Name: "ts", DuckDBType: "TIMESTAMP", Nullable: true},
		},
		PKCols: []string{"id"},
	}
	if err := store.CreateTable(ctx, meta); err != nil {
		t.Fatal(err)
	}

	loc := time.FixedZone("CST", 8*3600)
	local := time.Date(2026, 9, 25, 7, 49, 0, 0, loc) // as parseTime&loc=Local
	if err := store.InsertRows(ctx, meta, [][]any{{int64(1), local}}); err != nil {
		t.Fatal(err)
	}
	var s string
	if err := store.DB().QueryRowContext(ctx, `SELECT ts::VARCHAR FROM "demo"."t" WHERE id = 1`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	if s != "2026-09-25 07:49:00" {
		t.Fatalf("got %q, UnixMicro-of-Local would be 2026-09-24 23:49:00", s)
	}
}

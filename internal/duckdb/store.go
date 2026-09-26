package duckdbstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/sqlpub/go-mysql-duckdb/internal/schema"
)

// Store is a DuckDB backend for sync apply.
//
// DuckDB allows multiple concurrent writers inside one process (MVCC + OCC).
// Appends never conflict even on the same table; UPDATE/DELETE of the same row may.
// We keep a mutex only around in-memory meta + DDL (catalog changes).
type Store struct {
	db      *sql.DB
	catalog string
	log     *slog.Logger
	metaMu  sync.Mutex
	metas   map[string]*schema.TableMeta // mysqlSchema.table
	batch   int
}

func Open(path string, batchSize, maxConns int, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	if maxConns <= 0 {
		maxConns = 4
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir duckdb dir: %w", err)
	}
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("open duckdb: %w", err)
	}
	// Multiple connections = concurrent transactions in-process (official concurrency model).
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping duckdb: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA threads=%d", maxConns)); err != nil {
		log.Warn("set duckdb threads", "err", err)
	}
	var catalog string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&catalog); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("current_database: %w", err)
	}
	log.Info("opened duckdb", "path", path, "catalog", catalog, "max_conns", maxConns)
	return &Store{
		db:      db,
		catalog: catalog,
		log:     log,
		metas:   make(map[string]*schema.TableMeta),
		batch:   batchSize,
	}, nil
}

func (s *Store) Close() error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Catalog() string { return s.catalog }

func (s *Store) SetMaxConns(n int) {
	if n <= 0 {
		return
	}
	s.db.SetMaxOpenConns(n)
	s.db.SetMaxIdleConns(n)
}

// duckSchema maps a MySQL schema to the DuckDB schema used in SQL.
func (s *Store) duckSchema(mysqlSchema string) string {
	if mysqlSchema == s.catalog {
		return "main"
	}
	return mysqlSchema
}

func (s *Store) bindMeta(meta *schema.TableMeta) {
	meta.DuckSchema = s.duckSchema(meta.Schema)
}

func metaKey(sch, tbl string) string { return sch + "." + tbl }

func (s *Store) RememberMeta(meta *schema.TableMeta) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	s.bindMeta(meta)
	s.metas[metaKey(meta.Schema, meta.Name)] = meta
}

func (s *Store) GetMeta(sch, tbl string) (*schema.TableMeta, bool) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	m, ok := s.metas[metaKey(sch, tbl)]
	return m, ok
}

func (s *Store) ForgetMeta(sch, tbl string) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	delete(s.metas, metaKey(sch, tbl))
}

func (s *Store) ensureDuckSchemaLocked(ctx context.Context, duckSch string) error {
	if duckSch == "" || duckSch == "main" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, schema.CreateSchemaSQL(duckSch))
	return err
}

func (s *Store) EnsureSchema(ctx context.Context, schemaName string) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	return s.ensureDuckSchemaLocked(ctx, s.duckSchema(schemaName))
}

func (s *Store) CreateTable(ctx context.Context, meta *schema.TableMeta) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	s.bindMeta(meta)
	if err := s.ensureDuckSchemaLocked(ctx, meta.DuckSchema); err != nil {
		return err
	}
	sqlText := schema.CreateTableSQL(meta)
	s.log.Info("create table", "sql", sqlText, "mysql_schema", meta.Schema)
	if _, err := s.db.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("create table %s.%s: %w", meta.Schema, meta.Name, err)
	}
	s.metas[metaKey(meta.Schema, meta.Name)] = meta
	return nil
}

func (s *Store) DropTable(ctx context.Context, sch, tbl string) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if _, err := s.db.ExecContext(ctx, schema.DropTableSQL(s.duckSchema(sch), tbl)); err != nil {
		return err
	}
	delete(s.metas, metaKey(sch, tbl))
	return nil
}

func (s *Store) RenameTable(ctx context.Context, sch, oldName, newName string) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if _, err := s.db.ExecContext(ctx, schema.RenameTableSQL(s.duckSchema(sch), oldName, newName)); err != nil {
		return err
	}
	if meta, ok := s.metas[metaKey(sch, oldName)]; ok {
		delete(s.metas, metaKey(sch, oldName))
		meta.Name = newName
		s.metas[metaKey(sch, newName)] = meta
	}
	return nil
}

func (s *Store) AddColumn(ctx context.Context, sch, tbl string, col schema.Column) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if _, err := s.db.ExecContext(ctx, schema.AddColumnSQL(s.duckSchema(sch), tbl, col)); err != nil {
		return err
	}
	if meta, ok := s.metas[metaKey(sch, tbl)]; ok {
		meta.Columns = append(meta.Columns, col)
	}
	return nil
}

func (s *Store) DropColumn(ctx context.Context, sch, tbl, column string) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if _, err := s.db.ExecContext(ctx, schema.DropColumnSQL(s.duckSchema(sch), tbl, column)); err != nil {
		return err
	}
	if meta, ok := s.metas[metaKey(sch, tbl)]; ok {
		out := meta.Columns[:0]
		for _, c := range meta.Columns {
			if c.Name != column {
				out = append(out, c)
			}
		}
		meta.Columns = out
	}
	return nil
}

// TruncateTable removes all rows from a table.
func (s *Store) TruncateTable(ctx context.Context, meta *schema.TableMeta) error {
	s.bindMeta(meta)
	return withRetry(func() error {
		_, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", meta.QualifiedName()))
		return err
	})
}

// InsertRows bulk-inserts rows. Concurrent appends are safe (DuckDB: appends never conflict).
func (s *Store) InsertRows(ctx context.Context, meta *schema.TableMeta, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	s.bindMeta(meta)
	return withRetry(func() error {
		return s.insertRowsOnce(ctx, meta, rows)
	})
}

func (s *Store) insertRowsOnce(ctx context.Context, meta *schema.TableMeta, rows [][]any) error {
	cols := meta.ColumnNames()
	placeholders := make([]string, len(cols))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	q := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		meta.QualifiedName(),
		joinQuoted(cols),
		strings.Join(placeholders, ", "),
	)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		vals := normalizeRow(row, len(cols))
		if _, err := stmt.ExecContext(ctx, vals...); err != nil {
			return fmt.Errorf("insert into %s.%s: %w", meta.Schema, meta.Name, err)
		}
	}
	return tx.Commit()
}

// UpsertRows deletes by PK then inserts (idempotent for UPDATE / insert-or-replace).
func (s *Store) UpsertRows(ctx context.Context, meta *schema.TableMeta, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	if len(meta.PKCols) == 0 {
		return fmt.Errorf("table %s.%s has no primary key", meta.Schema, meta.Name)
	}
	s.bindMeta(meta)
	return withRetry(func() error {
		return s.upsertRowsOnce(ctx, meta, rows)
	})
}

func (s *Store) upsertRowsOnce(ctx context.Context, meta *schema.TableMeta, rows [][]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	pkIdx := pkIndexes(meta)
	delQ := buildDeleteSQL(meta)
	delStmt, err := tx.PrepareContext(ctx, delQ)
	if err != nil {
		return fmt.Errorf("prepare delete: %w", err)
	}
	defer delStmt.Close()

	cols := meta.ColumnNames()
	placeholders := make([]string, len(cols))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	insQ := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		meta.QualifiedName(),
		joinQuoted(cols),
		strings.Join(placeholders, ", "),
	)
	insStmt, err := tx.PrepareContext(ctx, insQ)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer insStmt.Close()

	for _, row := range rows {
		vals := normalizeRow(row, len(cols))
		pkArgs := make([]any, len(pkIdx))
		for i, idx := range pkIdx {
			if idx < len(vals) {
				pkArgs[i] = vals[idx]
			}
		}
		if _, err := delStmt.ExecContext(ctx, pkArgs...); err != nil {
			return fmt.Errorf("upsert delete: %w", err)
		}
		if _, err := insStmt.ExecContext(ctx, vals...); err != nil {
			return fmt.Errorf("upsert insert: %w", err)
		}
	}
	return tx.Commit()
}

// DeleteRows deletes by primary key values taken from rows.
func (s *Store) DeleteRows(ctx context.Context, meta *schema.TableMeta, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	if len(meta.PKCols) == 0 {
		return fmt.Errorf("table %s.%s has no primary key", meta.Schema, meta.Name)
	}
	s.bindMeta(meta)
	return withRetry(func() error {
		return s.deleteRowsOnce(ctx, meta, rows)
	})
}

func (s *Store) deleteRowsOnce(ctx context.Context, meta *schema.TableMeta, rows [][]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	pkIdx := pkIndexes(meta)
	delQ := buildDeleteSQL(meta)
	stmt, err := tx.PrepareContext(ctx, delQ)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, row := range rows {
		vals := normalizeRow(row, len(meta.Columns))
		pkArgs := make([]any, len(pkIdx))
		for i, idx := range pkIdx {
			if idx < len(vals) {
				pkArgs[i] = vals[idx]
			}
		}
		if _, err := stmt.ExecContext(ctx, pkArgs...); err != nil {
			return fmt.Errorf("delete: %w", err)
		}
	}
	return tx.Commit()
}

func buildDeleteSQL(meta *schema.TableMeta) string {
	conds := make([]string, len(meta.PKCols))
	for i, c := range meta.PKCols {
		conds[i] = fmt.Sprintf("%s = ?", quoteIdent(c))
	}
	return fmt.Sprintf("DELETE FROM %s WHERE %s", meta.QualifiedName(), strings.Join(conds, " AND "))
}

func pkIndexes(meta *schema.TableMeta) []int {
	idx := make([]int, 0, len(meta.PKCols))
	for _, pk := range meta.PKCols {
		for i, c := range meta.Columns {
			if c.Name == pk {
				idx = append(idx, i)
				break
			}
		}
	}
	return idx
}

func joinQuoted(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = quoteIdent(c)
	}
	return strings.Join(parts, ", ")
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func normalizeRow(row []any, n int) []any {
	out := make([]any, n)
	for i := 0; i < n; i++ {
		if i < len(row) {
			out[i] = convertValue(row[i])
		}
	}
	return out
}

func convertValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	default:
		return v
	}
}

// ExecRaw runs arbitrary SQL (tests / maintenance).
func (s *Store) ExecRaw(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func withRetry(fn func() error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = fn()
		if err == nil || !isTxnConflict(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return err
}

func isTxnConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "conflict") ||
		strings.Contains(msg, "transaction conflict") ||
		strings.Contains(msg, "write-write conflict")
}

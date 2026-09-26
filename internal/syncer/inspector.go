package syncer

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"github.com/sqlpub/go-mysql-duckdb/internal/config"
	"github.com/sqlpub/go-mysql-duckdb/internal/schema"
)

// Inspector loads MySQL table metadata for allowed tables.
type Inspector struct {
	db  *sql.DB
	cfg *config.Config
	log *slog.Logger
}

func NewInspector(cfg *config.Config, log *slog.Logger) (*Inspector, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/?charset=%s&parseTime=true&loc=Local",
		cfg.MySQL.User, cfg.MySQL.Password, cfg.MySQL.Addr, cfg.MySQL.Charset)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return &Inspector{db: db, cfg: cfg, log: log}, nil
}

func (i *Inspector) Close() error {
	if i.db == nil {
		return nil
	}
	return i.db.Close()
}

func (i *Inspector) DB() *sql.DB { return i.db }

// ListSyncTables returns metadata for all tables in scope that have a PK.
func (i *Inspector) ListSyncTables(ctx context.Context) ([]*schema.TableMeta, error) {
	var out []*schema.TableMeta
	for _, dbName := range i.cfg.Sync.Databases {
		tables, err := i.listTables(ctx, dbName)
		if err != nil {
			return nil, err
		}
		for _, tbl := range tables {
			if !i.cfg.TableAllowed(dbName, tbl) {
				continue
			}
			meta, err := i.LoadTable(ctx, dbName, tbl)
			if err != nil {
				return nil, err
			}
			if len(meta.PKCols) == 0 {
				i.log.Warn("skip table without primary key", "schema", dbName, "table", tbl)
				continue
			}
			out = append(out, meta)
		}
	}
	return out, nil
}

func (i *Inspector) listTables(ctx context.Context, dbName string) ([]string, error) {
	rows, err := i.db.QueryContext(ctx, `
		SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE'
		ORDER BY TABLE_NAME`, dbName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

func (i *Inspector) LoadTable(ctx context.Context, dbName, table string) (*schema.TableMeta, error) {
	colRows, err := i.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, dbName, table)
	if err != nil {
		return nil, err
	}
	defer colRows.Close()

	meta := &schema.TableMeta{Schema: dbName, Name: table}
	for colRows.Next() {
		var name, colType, nullable, colKey string
		if err := colRows.Scan(&name, &colType, &nullable, &colKey); err != nil {
			return nil, err
		}
		isPK := strings.EqualFold(colKey, "PRI")
		c := schema.Column{
			Name:       name,
			MySQLType:  colType,
			DuckDBType: schema.MapMySQLType(colType),
			Nullable:   strings.EqualFold(nullable, "YES") && !isPK,
			IsPK:       isPK,
		}
		meta.Columns = append(meta.Columns, c)
		if isPK {
			meta.PKCols = append(meta.PKCols, name)
		}
	}
	if err := colRows.Err(); err != nil {
		return nil, err
	}
	if len(meta.Columns) == 0 {
		return nil, fmt.Errorf("table %s.%s not found or has no columns", dbName, table)
	}
	return meta, nil
}

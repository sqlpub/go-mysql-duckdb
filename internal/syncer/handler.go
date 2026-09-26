package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-mysql-org/go-mysql/canal"
	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"

	"github.com/sqlpub/go-mysql-duckdb/internal/config"
	duckdbstore "github.com/sqlpub/go-mysql-duckdb/internal/duckdb"
	"github.com/sqlpub/go-mysql-duckdb/internal/schema"
)

// Handler applies canal events to DuckDB.
type Handler struct {
	canal.DummyEventHandler

	cfg   *config.Config
	store *duckdbstore.Store
	insp  *Inspector
	pos   *PositionStore
	log   *slog.Logger

	mu       sync.Mutex
	gtid     string
	gtidSet  mysql.GTIDSet
	rowCount atomic.Int64
	errCount atomic.Int64
}

func NewHandler(cfg *config.Config, store *duckdbstore.Store, insp *Inspector, pos *PositionStore, log *slog.Logger) *Handler {
	return &Handler{cfg: cfg, store: store, insp: insp, pos: pos, log: log}
}

func (h *Handler) String() string { return "DuckDBHandler" }

func (h *Handler) OnRow(e *canal.RowsEvent) error {
	sch, tbl := e.Table.Schema, e.Table.Name
	if !h.cfg.TableAllowed(sch, tbl) {
		return nil
	}
	ctx := context.Background()
	meta, ok := h.store.GetMeta(sch, tbl)
	if !ok {
		loaded, err := h.insp.LoadTable(ctx, sch, tbl)
		if err != nil {
			h.errCount.Add(1)
			return fmt.Errorf("load table meta %s.%s: %w", sch, tbl, err)
		}
		if len(loaded.PKCols) == 0 {
			h.log.Warn("skip row event for table without PK", "schema", sch, "table", tbl)
			return nil
		}
		if err := h.store.CreateTable(ctx, loaded); err != nil {
			// table may already exist
			h.store.RememberMeta(loaded)
		}
		meta = loaded
	}

	if h.cfg.Sync.Debug {
		h.logRowEvent(e, meta)
	}

	rows := make([][]any, 0, len(e.Rows))
	switch e.Action {
	case canal.InsertAction:
		for _, r := range e.Rows {
			rows = append(rows, r)
		}
		if err := h.store.UpsertRows(ctx, meta, rows); err != nil {
			h.errCount.Add(1)
			return err
		}
	case canal.UpdateAction:
		// Update rows come in pairs: before, after
		for i := 0; i+1 < len(e.Rows); i += 2 {
			rows = append(rows, e.Rows[i+1])
		}
		if err := h.store.UpsertRows(ctx, meta, rows); err != nil {
			h.errCount.Add(1)
			return err
		}
	case canal.DeleteAction:
		for _, r := range e.Rows {
			rows = append(rows, r)
		}
		if err := h.store.DeleteRows(ctx, meta, rows); err != nil {
			h.errCount.Add(1)
			return err
		}
	default:
		h.log.Warn("unknown row action", "action", e.Action)
		return nil
	}
	h.rowCount.Add(int64(len(rows)))
	return nil
}

func (h *Handler) logRowEvent(e *canal.RowsEvent, meta *schema.TableMeta) {
	cols := meta.ColumnNames()
	switch e.Action {
	case canal.UpdateAction:
		for i := 0; i+1 < len(e.Rows); i += 2 {
			h.log.Debug("row change",
				"action", e.Action,
				"schema", meta.Schema,
				"table", meta.Name,
				"before", rowMap(cols, e.Rows[i]),
				"after", rowMap(cols, e.Rows[i+1]),
			)
		}
	default:
		for _, r := range e.Rows {
			h.log.Debug("row change",
				"action", e.Action,
				"schema", meta.Schema,
				"table", meta.Name,
				"row", rowMap(cols, r),
			)
		}
	}
}

func rowMap(cols []string, row []any) map[string]any {
	m := make(map[string]any, len(cols))
	for i, c := range cols {
		var v any
		if i < len(row) {
			v = row[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
		}
		m[c] = v
	}
	return m
}

func (h *Handler) OnTableChanged(header *replication.EventHeader, schemaName string, table string) error {
	if !dbInScope(h.cfg, schemaName) {
		return nil
	}
	if len(h.cfg.Sync.Tables) > 0 && !h.cfg.TableAllowed(schemaName, table) {
		return nil
	}
	h.store.ForgetMeta(schemaName, table)
	return nil
}

func (h *Handler) OnDDL(header *replication.EventHeader, nextPos mysql.Position, queryEvent *replication.QueryEvent) error {
	query := string(queryEvent.Query)
	defaultSchema := string(queryEvent.Schema)
	action := schema.ParseDDL(query, defaultSchema)
	if action == nil {
		return nil
	}
	if action.Kind == "unsupported" {
		if action.Schema == "" || dbInScope(h.cfg, action.Schema) {
			h.log.Warn("unsupported DDL skipped", "sql", truncate(query, 200))
		}
		return nil
	}
	// Out-of-scope databases: ignore completely (Canal still delivers server-wide DDL).
	if action.Schema != "" && !dbInScope(h.cfg, action.Schema) {
		return nil
	}
	switch action.Kind {
	case "create", "add_column", "drop_column":
		if !h.cfg.TableAllowed(action.Schema, action.Table) {
			return nil
		}
	case "drop", "rename":
		if len(h.cfg.Sync.Tables) > 0 && !h.cfg.TableAllowed(action.Schema, action.Table) {
			// rename target may be new name; still require source in whitelist when set
			if action.Kind == "rename" && !h.cfg.TableAllowed(action.Schema, action.Table) {
				return nil
			}
			if action.Kind == "drop" {
				return nil
			}
		}
	}

	ctx := context.Background()
	h.log.Info("apply DDL", "kind", action.Kind, "schema", action.Schema, "table", action.Table)

	switch action.Kind {
	case "create":
		cols, pks, err := schema.ParseCreateTableColumns(action.Raw)
		if err != nil || len(cols) == 0 {
			// fall back to INFORMATION_SCHEMA
			meta, err2 := h.insp.LoadTable(ctx, action.Schema, action.Table)
			if err2 != nil {
				h.log.Warn("CREATE TABLE parse/load failed", "err", err, "load_err", err2)
				return nil
			}
			if len(meta.PKCols) == 0 {
				h.log.Warn("skip CREATE without PK", "schema", action.Schema, "table", action.Table)
				return nil
			}
			return h.store.CreateTable(ctx, meta)
		}
		if len(pks) == 0 {
			h.log.Warn("skip CREATE without PK", "schema", action.Schema, "table", action.Table)
			return nil
		}
		meta := &schema.TableMeta{
			Schema:  action.Schema,
			Name:    action.Table,
			Columns: cols,
			PKCols:  pks,
		}
		return h.store.CreateTable(ctx, meta)

	case "drop":
		return h.store.DropTable(ctx, action.Schema, action.Table)

	case "rename":
		if action.NewSchema != "" && action.NewSchema != action.Schema {
			h.log.Warn("cross-schema RENAME not supported", "sql", truncate(query, 200))
			return nil
		}
		return h.store.RenameTable(ctx, action.Schema, action.Table, action.NewTable)

	case "add_column":
		col := schema.Column{
			Name:       action.ColumnName,
			MySQLType:  action.ColumnType,
			DuckDBType: schema.MapMySQLType(action.ColumnType),
			Nullable:   action.Nullable,
		}
		return h.store.AddColumn(ctx, action.Schema, action.Table, col)

	case "drop_column":
		return h.store.DropColumn(ctx, action.Schema, action.Table, action.ColumnName)
	}
	return nil
}

func (h *Handler) OnPosSynced(header *replication.EventHeader, pos mysql.Position, set mysql.GTIDSet, force bool) error {
	gtid := ""
	if set != nil && set.String() != "" {
		gtid = set.String()
		h.mu.Lock()
		h.gtid = gtid
		h.gtidSet = set.Clone()
		h.mu.Unlock()
	} else {
		// Canal may pass a nil GTID set when syncing by file/pos without a seeded
		// prevGset; never wipe a previously known GTID in that case.
		gtid = h.currentGTID()
	}
	if err := h.pos.Save(pos, gtid); err != nil {
		h.log.Error("save checkpoint", "err", err)
		return err
	}
	if force {
		h.log.Info("checkpoint", "file", pos.Name, "pos", pos.Pos, "gtid", gtid, "rows", h.rowCount.Load())
	}
	return nil
}

// OnXID intentionally does not persist the checkpoint. Canal always follows with
// OnPosSynced for XID events; saving here raced and could store an empty GTID.
func (h *Handler) OnXID(header *replication.EventHeader, nextPos mysql.Position) error {
	return nil
}

func (h *Handler) currentGTID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gtid
}

func (h *Handler) OnGTID(header *replication.EventHeader, evt mysql.BinlogGTIDEvent) error {
	if evt == nil {
		return nil
	}
	next, err := evt.GTIDNext()
	if err != nil || next == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gtidSet == nil {
		h.gtidSet = next.Clone()
	} else if err := h.gtidSet.Update(next.String()); err != nil {
		h.gtidSet = next.Clone()
	}
	h.gtid = h.gtidSet.String()
	return nil
}

func dbInScope(cfg *config.Config, schemaName string) bool {
	for _, db := range cfg.Sync.Databases {
		if db == schemaName {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

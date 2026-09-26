package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/go-mysql-org/go-mysql/canal"
	"github.com/go-mysql-org/go-mysql/mysql"

	"github.com/sqlpub/go-mysql-duckdb/internal/config"
	duckdbstore "github.com/sqlpub/go-mysql-duckdb/internal/duckdb"
	"github.com/sqlpub/go-mysql-duckdb/internal/schema"
)

// Syncer architecture:
//
//	全量：本进程用 database/sql SELECT 拉表（不依赖 mysqldump）
//	增量：go-mysql Canal 只追 binlog（Dump.ExecutionPath=""）
//
//	无 checkpoint → 先记 @@gtid_executed，再全量 SELECT，再 StartFromGTID
//	有 gtid        → StartFromGTID（跳过全量）
//	只有 file/pos  → RunFrom
type Syncer struct {
	cfg     *config.Config
	store   *duckdbstore.Store
	insp    *Inspector
	pos     *PositionStore
	log     *slog.Logger
	canal   *canal.Canal
	handler *Handler
}

func New(cfg *config.Config, log *slog.Logger) (*Syncer, error) {
	if log == nil {
		log = slog.Default()
	}
	store, err := duckdbstore.Open(cfg.DuckDB.Path, cfg.Sync.BatchSize, cfg.Sync.DumpConcurrency, log)
	if err != nil {
		return nil, err
	}
	insp, err := NewInspector(cfg, log)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &Syncer{
		cfg:   cfg,
		store: store,
		insp:  insp,
		pos:   NewPositionStore(cfg.Sync.Checkpoint),
		log:   log,
	}, nil
}

func (s *Syncer) Close() error {
	var errs []string
	if s.canal != nil {
		s.canal.Close()
	}
	if s.insp != nil {
		if err := s.insp.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (s *Syncer) Store() *duckdbstore.Store { return s.store }

func (s *Syncer) Run(ctx context.Context) error {
	tables, err := s.insp.ListSyncTables(ctx)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	if len(tables) == 0 {
		s.log.Warn("no tables with primary key in sync scope")
	}

	for _, meta := range tables {
		if err := s.store.EnsureSchema(ctx, meta.Schema); err != nil {
			return err
		}
		if err := s.store.CreateTable(ctx, meta); err != nil {
			return fmt.Errorf("bootstrap create %s.%s: %w", meta.Schema, meta.Name, err)
		}
	}

	cfg := canal.NewDefaultConfig()
	cfg.Addr = s.cfg.MySQL.Addr
	cfg.User = s.cfg.MySQL.User
	cfg.Password = s.cfg.MySQL.Password
	cfg.ServerID = s.cfg.MySQL.ServerID
	cfg.Flavor = s.cfg.MySQL.Flavor
	cfg.Charset = s.cfg.MySQL.Charset
	// Canal 只做增量：关掉 mysqldump。
	cfg.Dump.ExecutionPath = ""
	cfg.Logger = slog.New(&canalLogFilter{next: s.log.Handler(), allowDB: dbSet(s.cfg.Sync.Databases)})
	for _, db := range s.cfg.Sync.Databases {
		cfg.IncludeTableRegex = append(cfg.IncludeTableRegex, "^"+regexp.QuoteMeta(db)+`\..*$`)
	}

	c, err := canal.NewCanal(cfg)
	if err != nil {
		return fmt.Errorf("new canal: %w", err)
	}
	s.canal = c

	h := NewHandler(s.cfg, s.store, s.insp, s.pos, s.log)
	s.handler = h
	c.SetEventHandler(h)

	cp, err := s.pos.Load()
	if err != nil {
		return fmt.Errorf("load checkpoint: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		errCh <- s.start(ctx, c, cp, tables)
	}()

	select {
	case <-ctx.Done():
		s.log.Info("shutting down")
		c.Close()
		err := <-errCh
		if ctx.Err() != nil {
			return nil
		}
		return err
	case err := <-errCh:
		return err
	}
}

func (s *Syncer) start(ctx context.Context, c *canal.Canal, cp *Checkpoint, tables []*schema.TableMeta) error {
	switch {
	case cp != nil && cp.GTID != "":
		gset, err := mysql.ParseGTIDSet(s.cfg.MySQL.Flavor, cp.GTID)
		if err != nil {
			return fmt.Errorf("parse checkpoint gtid: %w", err)
		}
		s.log.Info("resume GTID incremental", "gtid", cp.GTID)
		return c.StartFromGTID(gset)

	case cp != nil && cp.Name != "":
		s.log.Info("resume binlog position incremental", "file", cp.Name, "pos", cp.Pos)
		return c.RunFrom(mysql.Position{Name: cp.Name, Pos: cp.Pos})

	default:
		// 1) 先拍 GTID，2) Go SELECT 全量，3) 从该 GTID 追增量（dump 期间变更由 binlog 补上，upsert 幂等）
		gset, err := c.GetMasterGTIDSet()
		if err != nil {
			return fmt.Errorf("get gtid_executed: %w", err)
		}
		if gset == nil || gset.String() == "" {
			return fmt.Errorf("gtid_executed is empty; enable MySQL GTID before first sync")
		}
		binPos, _ := c.GetMasterPos()
		s.log.Info("full dump starting (pure Go SELECT)",
			"tables", len(tables),
			"concurrency", s.cfg.Sync.DumpConcurrency,
			"gtid", gset.String(),
		)
		if err := s.FullDumpConcurrent(ctx, tables); err != nil {
			return err
		}

		if err := s.pos.Save(binPos, gset.String()); err != nil {
			return fmt.Errorf("save checkpoint after dump: %w", err)
		}
		s.log.Info("full dump done; start GTID incremental", "gtid", gset.String())
		return c.StartFromGTID(gset)
	}
}

func dbSet(dbs []string) map[string]struct{} {
	m := make(map[string]struct{}, len(dbs))
	for _, db := range dbs {
		m[db] = struct{}{}
	}
	return m
}

type canalLogFilter struct {
	next    slog.Handler
	allowDB map[string]struct{}
}

func (f *canalLogFilter) Enabled(ctx context.Context, level slog.Level) bool {
	return f.next.Enabled(ctx, level)
}
func (f *canalLogFilter) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &canalLogFilter{next: f.next.WithAttrs(attrs), allowDB: f.allowDB}
}
func (f *canalLogFilter) WithGroup(name string) slog.Handler {
	return &canalLogFilter{next: f.next.WithGroup(name), allowDB: f.allowDB}
}
func (f *canalLogFilter) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "table structure changed, clear table cache" {
		var db string
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "database" {
				db = a.Value.String()
			}
			return true
		})
		if db != "" {
			if _, ok := f.allowDB[db]; !ok {
				return nil
			}
		}
	}
	return f.next.Handle(ctx, r)
}

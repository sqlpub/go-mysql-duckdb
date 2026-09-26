package syncer

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sqlpub/go-mysql-duckdb/internal/schema"
)

// DumpProgress is reported while streaming a table.
type DumpProgress struct {
	Schema     string
	Table      string
	DoneRows   int64
	TableIdx   int // 1-based
	TableTotal int
	Shard      int // 1-based; 0 if not sharded
	Shards     int
}

type dumpRange struct {
	where string // empty = full table
	args  []any
}

type dumpShard struct {
	meta       *schema.TableMeta
	tableIdx   int
	shardIdx   int // 1-based
	shardTotal int
	rng        dumpRange
	prog       *tableDumpProgress
}

type tableDumpProgress struct {
	estRows    int64 // information_schema estimate; 0 if unknown
	done       atomic.Int64
	shardsLeft atomic.Int64
	start      time.Time
}

// FullDumpConcurrent dumps with parallel MySQL reads and parallel DuckDB appends
// (same process, multiple connections; DuckDB appends never conflict).
func (s *Syncer) FullDumpConcurrent(ctx context.Context, tables []*schema.TableMeta) error {
	if len(tables) == 0 {
		return nil
	}
	concurrency := s.cfg.Sync.DumpConcurrency
	if concurrency <= 0 {
		concurrency = 4
	}

	var shards []dumpShard
	for i, meta := range tables {
		if err := s.store.TruncateTable(ctx, meta); err != nil {
			return fmt.Errorf("truncate %s.%s: %w", meta.Schema, meta.Name, err)
		}
		prog := &tableDumpProgress{start: time.Now()}
		if est, err := s.insp.estimateTableRows(ctx, meta); err == nil {
			prog.estRows = est
		}

		ranges, err := s.insp.planPKRanges(ctx, meta, concurrency)
		if err != nil {
			s.log.Warn("pk range plan failed; fallback single stream",
				"table", fmt.Sprintf("%s.%s", meta.Schema, meta.Name), "err", err)
			ranges = []dumpRange{{}}
		}
		prog.shardsLeft.Store(int64(len(ranges)))
		for si, rng := range ranges {
			shards = append(shards, dumpShard{
				meta: meta, tableIdx: i + 1,
				shardIdx: si + 1, shardTotal: len(ranges),
				rng: rng, prog: prog,
			})
		}
		s.log.Info("full dump plan table",
			"table", fmt.Sprintf("%s.%s", meta.Schema, meta.Name),
			"est_rows", prog.estRows,
			"shards", len(ranges),
		)
	}

	if concurrency > len(shards) {
		concurrency = len(shards)
	}
	s.insp.db.SetMaxOpenConns(concurrency + 2)
	s.insp.db.SetMaxIdleConns(concurrency + 2)
	s.store.SetMaxConns(concurrency)

	shardCh := make(chan dumpShard, len(shards))
	for _, sh := range shards {
		shardCh <- sh
	}
	close(shardCh)

	errCh := make(chan error, 1)
	var written atomic.Int64
	var tablesDone atomic.Int64

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fail := func(err error) {
		if err == nil {
			return
		}
		select {
		case errCh <- err:
		default:
		}
		cancel()
	}

	var lastLog sync.Map // table key -> time.Time

	var readersWG sync.WaitGroup
	for range concurrency {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for sh := range shardCh {
				if ctx.Err() != nil {
					return
				}
				meta := sh.meta
				n, err := s.insp.dumpSelect(ctx, meta, sh.rng, s.cfg.Sync.BatchSize, func(rows [][]any) error {
					if err := s.store.InsertRows(ctx, meta, rows); err != nil {
						return err
					}
					written.Add(int64(len(rows)))
					return nil
				}, func(delta int64) {
					done := sh.prog.done.Add(delta)
					key := meta.Schema + "." + meta.Name
					now := time.Now()
					if v, ok := lastLog.Load(key); ok {
						if now.Sub(v.(time.Time)) < 2*time.Second {
							return
						}
					}
					lastLog.Store(key, now)
					elapsed := now.Sub(sh.prog.start).Seconds()
					attrs := []any{
						"table", key,
						"tables", fmt.Sprintf("%d/%d", sh.tableIdx, len(tables)),
						"rows", done,
						"shard", fmt.Sprintf("%d/%d", sh.shardIdx, sh.shardTotal),
					}
					if sh.prog.estRows > 0 {
						pct := float64(done) * 100 / float64(sh.prog.estRows)
						if pct > 99.9 {
							pct = 99.9 // estimate can be low; keep <100 until done
						}
						attrs = append(attrs, "est_rows", sh.prog.estRows, "pct", fmt.Sprintf("~%.1f%%", pct))
					}
					if elapsed > 0 {
						attrs = append(attrs, "rows_per_sec", int64(float64(done)/elapsed))
					}
					s.log.Info("full dump progress", attrs...)
				})
				if err != nil {
					fail(fmt.Errorf("dump %s.%s shard %d/%d: %w", meta.Schema, meta.Name, sh.shardIdx, sh.shardTotal, err))
					return
				}
				if sh.prog.shardsLeft.Add(-1) == 0 {
					done := tablesDone.Add(1)
					s.log.Info("full dump table done",
						"table", fmt.Sprintf("%s.%s", meta.Schema, meta.Name),
						"rows", sh.prog.done.Load(),
						"shards", sh.shardTotal,
						"tables", fmt.Sprintf("%d/%d", done, len(tables)),
						"elapsed", time.Since(sh.prog.start).Round(time.Millisecond).String(),
					)
				}
				_ = n
			}
		}()
	}

	readersWG.Wait()

	select {
	case err := <-errCh:
		return err
	default:
	}
	s.log.Info("full dump all tables done", "tables", len(tables), "shards", len(shards), "rows", written.Load())
	return nil
}

// dumpSelect streams rows for an optional PK range into write().
// onBatch is called with the number of rows flushed each batch (for shared progress).
func (i *Inspector) dumpSelect(
	ctx context.Context,
	meta *schema.TableMeta,
	rng dumpRange,
	batchSize int,
	write func([][]any) error,
	onBatch func(delta int64),
) (int64, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	cols := meta.ColumnNames()
	quoted := make([]string, len(cols))
	for idx, c := range cols {
		quoted[idx] = "`" + strings.ReplaceAll(c, "`", "``") + "`"
	}
	q := fmt.Sprintf(
		"SELECT %s FROM `%s`.`%s`",
		strings.Join(quoted, ", "),
		strings.ReplaceAll(meta.Schema, "`", "``"),
		strings.ReplaceAll(meta.Name, "`", "``"),
	)
	if rng.where != "" {
		q += " WHERE " + rng.where
	}
	rows, err := i.db.QueryContext(ctx, q, rng.args...)
	if err != nil {
		return 0, fmt.Errorf("select %s.%s: %w", meta.Schema, meta.Name, err)
	}
	defer rows.Close()

	raw := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for idx := range raw {
		ptrs[idx] = &raw[idx]
	}

	var done int64
	batch := make([][]any, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n := int64(len(batch))
		if err := write(batch); err != nil {
			return err
		}
		done += n
		batch = batch[:0]
		if onBatch != nil {
			onBatch(n)
		}
		return nil
	}

	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return done, err
		}
		row := make([]any, len(cols))
		for idx, v := range raw {
			if b, ok := v.([]byte); ok {
				row[idx] = string(b)
			} else {
				row[idx] = v
			}
		}
		batch = append(batch, row)
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return done, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return done, err
	}
	return done, flush()
}

// estimateTableRows reads InnoDB's approximate row count from information_schema (no table scan).
func (i *Inspector) estimateTableRows(ctx context.Context, meta *schema.TableMeta) (int64, error) {
	const q = `SELECT TABLE_ROWS FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`
	var n sql.NullInt64
	err := i.db.QueryRowContext(ctx, q, meta.Schema, meta.Name).Scan(&n)
	if err != nil {
		return 0, err
	}
	if !n.Valid || n.Int64 < 0 {
		return 0, nil
	}
	return n.Int64, nil
}

// planPKRanges splits a single integer-like PK into [shards] half-open ranges.
// Composite / non-integer PK → one full-table range.
func (i *Inspector) planPKRanges(ctx context.Context, meta *schema.TableMeta, shards int) ([]dumpRange, error) {
	if shards <= 1 || len(meta.PKCols) != 1 {
		return []dumpRange{{}}, nil
	}
	pk := meta.PKCols[0]
	col := findColumn(meta, pk)
	if col == nil || !isIntegerMySQLType(col.MySQLType) {
		return []dumpRange{{}}, nil
	}
	qPK := "`" + strings.ReplaceAll(pk, "`", "``") + "`"
	q := fmt.Sprintf(
		"SELECT MIN(%s), MAX(%s) FROM `%s`.`%s`",
		qPK, qPK,
		strings.ReplaceAll(meta.Schema, "`", "``"),
		strings.ReplaceAll(meta.Name, "`", "``"),
	)
	var minRaw, maxRaw any
	if err := i.db.QueryRowContext(ctx, q).Scan(&minRaw, &maxRaw); err != nil {
		return nil, err
	}
	if minRaw == nil || maxRaw == nil {
		return []dumpRange{{}}, nil
	}
	minV, ok1 := toBigInt(minRaw)
	maxV, ok2 := toBigInt(maxRaw)
	if !ok1 || !ok2 {
		return []dumpRange{{}}, nil
	}
	if minV.Cmp(maxV) == 0 {
		return []dumpRange{{
			where: qPK + " = ?",
			args:  []any{minRaw},
		}}, nil
	}

	ranges := splitBigIntRanges(minV, maxV, shards)
	out := make([]dumpRange, 0, len(ranges))
	for idx, r := range ranges {
		lo := r[0].String()
		if idx == len(ranges)-1 {
			out = append(out, dumpRange{
				where: qPK + " >= ? AND " + qPK + " <= ?",
				args:  []any{lo, r[1].String()},
			})
			continue
		}
		out = append(out, dumpRange{
			where: qPK + " >= ? AND " + qPK + " < ?",
			args:  []any{lo, r[1].String()},
		})
	}
	return out, nil
}

func findColumn(meta *schema.TableMeta, name string) *schema.Column {
	for i := range meta.Columns {
		if meta.Columns[i].Name == name {
			return &meta.Columns[i]
		}
	}
	return nil
}

func isIntegerMySQLType(mysqlType string) bool {
	t := strings.ToLower(strings.TrimSpace(mysqlType))
	base := strings.Fields(t)[0]
	switch {
	case strings.HasPrefix(base, "tinyint"),
		strings.HasPrefix(base, "smallint"),
		strings.HasPrefix(base, "mediumint"),
		strings.HasPrefix(base, "int"),
		strings.HasPrefix(base, "bigint"):
		return true
	default:
		return false
	}
}

func toBigInt(v any) (*big.Int, bool) {
	switch x := v.(type) {
	case int64:
		return big.NewInt(x), true
	case int32:
		return big.NewInt(int64(x)), true
	case int16:
		return big.NewInt(int64(x)), true
	case int8:
		return big.NewInt(int64(x)), true
	case int:
		return big.NewInt(int64(x)), true
	case uint64:
		z := new(big.Int).SetUint64(x)
		return z, true
	case uint32:
		return big.NewInt(int64(x)), true
	case []byte:
		z := new(big.Int)
		if _, ok := z.SetString(string(x), 10); ok {
			return z, true
		}
	case string:
		z := new(big.Int)
		if _, ok := z.SetString(x, 10); ok {
			return z, true
		}
	}
	return nil, false
}

// splitBigIntRanges returns n [lo,hi) pairs covering [min,max], last hi == max+1 conceptually
// but caller treats last as inclusive max.
func splitBigIntRanges(minV, maxV *big.Int, n int) [][2]*big.Int {
	if n <= 1 {
		return [][2]*big.Int{{new(big.Int).Set(minV), new(big.Int).Set(maxV)}}
	}
	span := new(big.Int).Sub(maxV, minV)
	span.Add(span, big.NewInt(1)) // inclusive count
	if span.Cmp(big.NewInt(int64(n))) <= 0 {
		n = int(span.Int64())
	}
	out := make([][2]*big.Int, 0, n)
	for i := 0; i < n; i++ {
		lo := new(big.Int).Set(minV)
		lo.Add(lo, new(big.Int).Div(new(big.Int).Mul(span, big.NewInt(int64(i))), big.NewInt(int64(n))))
		var hi *big.Int
		if i == n-1 {
			hi = new(big.Int).Set(maxV)
		} else {
			hi = new(big.Int).Set(minV)
			hi.Add(hi, new(big.Int).Div(new(big.Int).Mul(span, big.NewInt(int64(i+1))), big.NewInt(int64(n))))
		}
		out = append(out, [2]*big.Int{lo, hi})
	}
	return out
}

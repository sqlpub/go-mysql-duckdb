package duckdbstore

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
)

// QueryResult is a JSON-friendly SELECT result.
type QueryResult struct {
	Columns  []string         `json:"columns"`
	Rows     []map[string]any `json:"rows"`
	RowCount int              `json:"rowCount"`
	Duration time.Duration    `json:"-"`
}

// QuerySQL runs a single read-only statement and caps returned rows.
func (s *Store) QuerySQL(ctx context.Context, sqlText string, maxRows int) (*QueryResult, error) {
	if maxRows <= 0 {
		maxRows = 1000
	}
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return nil, fmt.Errorf("sql is required")
	}
	if err := validateReadOnlySQL(sqlText); err != nil {
		return nil, err
	}

	start := time.Now()
	rows, err := s.db.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	out := &QueryResult{
		Columns: cols,
		Rows:    make([]map[string]any, 0),
	}

	for rows.Next() {
		if len(out.Rows) >= maxRows {
			break
		}
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = normalizeDriverValue(raw[i])
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	out.RowCount = len(out.Rows)
	out.Duration = time.Since(start)
	return out, nil
}

func normalizeDriverValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case sql.NullTime:
		if !t.Valid {
			return nil
		}
		return t.Time.UTC().Format(time.RFC3339Nano)
	case duckdb.Decimal:
		return decimalToJSON(t)
	case *duckdb.Decimal:
		if t == nil {
			return nil
		}
		return decimalToJSON(*t)
	case *big.Int:
		if t == nil {
			return nil
		}
		// Prefer number when it fits float64/int64 range for JSON charts.
		if t.IsInt64() {
			return t.Int64()
		}
		return t.String()
	default:
		return t
	}
}

// decimalToJSON returns a JSON number (float64). Scale is applied via Decimal.Float64().
func decimalToJSON(d duckdb.Decimal) any {
	if d.Value == nil {
		return 0
	}
	return d.Float64()
}

func validateReadOnlySQL(sqlText string) error {
	trimmed := strings.TrimSpace(sqlText)
	// Disallow multiple statements.
	withoutSemi := strings.TrimRight(trimmed, "; \t\n\r")
	if strings.Contains(withoutSemi, ";") {
		return fmt.Errorf("multiple SQL statements are not allowed")
	}
	upper := strings.ToUpper(withoutSemi)
	// Strip leading comments roughly.
	for {
		if strings.HasPrefix(upper, "--") {
			if i := strings.Index(withoutSemi, "\n"); i >= 0 {
				withoutSemi = strings.TrimSpace(withoutSemi[i+1:])
				upper = strings.ToUpper(withoutSemi)
				continue
			}
			return fmt.Errorf("empty sql")
		}
		if strings.HasPrefix(upper, "/*") {
			if i := strings.Index(withoutSemi, "*/"); i >= 0 {
				withoutSemi = strings.TrimSpace(withoutSemi[i+2:])
				upper = strings.ToUpper(withoutSemi)
				continue
			}
			return fmt.Errorf("unclosed comment")
		}
		break
	}
	if !(strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH") || strings.HasPrefix(upper, "SHOW") || strings.HasPrefix(upper, "DESCRIBE") || strings.HasPrefix(upper, "DESC") || strings.HasPrefix(upper, "EXPLAIN")) {
		return fmt.Errorf("only read-only SQL is allowed")
	}
	forbidden := []string{
		" INSERT ", " UPDATE ", " DELETE ", " DROP ", " ALTER ", " CREATE ",
		" ATTACH ", " COPY ", " PRAGMA ", " CALL ", " EXECUTE ", " INSTALL ",
		" LOAD ", " EXPORT ", " IMPORT ", " VACUUM ", " FORCE ",
	}
	padded := " " + upper + " "
	for _, f := range forbidden {
		if strings.Contains(padded, f) {
			return fmt.Errorf("forbidden keyword in SQL")
		}
	}
	return nil
}

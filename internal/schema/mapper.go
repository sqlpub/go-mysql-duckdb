package schema

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Column describes a MySQL column mapped to DuckDB.
type Column struct {
	Name       string
	MySQLType  string
	DuckDBType string
	Nullable   bool
	IsPK       bool
}

// TableMeta holds mapped table metadata.
type TableMeta struct {
	Schema     string // MySQL database / logical schema
	DuckSchema string // DuckDB schema used in SQL; empty means same as Schema
	Name       string
	Columns    []Column
	PKCols     []string
}

func (t *TableMeta) duckSchema() string {
	if t.DuckSchema != "" {
		return t.DuckSchema
	}
	return t.Schema
}

func (t *TableMeta) QualifiedName() string {
	return quoteIdent(t.duckSchema()) + "." + quoteIdent(t.Name)
}

func (t *TableMeta) ColumnNames() []string {
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Name
	}
	return names
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// MapMySQLType converts a MySQL column type string to DuckDB.
func MapMySQLType(mysqlType string) string {
	t := strings.ToLower(strings.TrimSpace(mysqlType))
	// strip unsigned / zerofill
	base := strings.Fields(t)[0]
	base = strings.TrimSuffix(base, ",")

	switch {
	case strings.HasPrefix(base, "tinyint"):
		if strings.Contains(t, "tinyint(1)") {
			return "BOOLEAN"
		}
		return "TINYINT"
	case strings.HasPrefix(base, "smallint"):
		return "SMALLINT"
	case strings.HasPrefix(base, "mediumint"):
		return "INTEGER"
	case strings.HasPrefix(base, "int"):
		return "INTEGER"
	case strings.HasPrefix(base, "bigint"):
		return "BIGINT"
	case strings.HasPrefix(base, "decimal"), strings.HasPrefix(base, "numeric"):
		p, s := parseDecimalParams(base)
		return fmt.Sprintf("DECIMAL(%d,%d)", p, s)
	case strings.HasPrefix(base, "float"):
		return "FLOAT"
	case strings.HasPrefix(base, "double"), strings.HasPrefix(base, "real"):
		return "DOUBLE"
	case strings.HasPrefix(base, "bit"):
		return "BIGINT"
	case strings.HasPrefix(base, "bool"), strings.HasPrefix(base, "boolean"):
		return "BOOLEAN"
	case strings.HasPrefix(base, "datetime"), strings.HasPrefix(base, "timestamp"):
		return "TIMESTAMP"
	case strings.HasPrefix(base, "date"):
		return "DATE"
	case strings.HasPrefix(base, "time"):
		return "TIME"
	case strings.HasPrefix(base, "year"):
		return "INTEGER"
	case strings.HasPrefix(base, "char"), strings.HasPrefix(base, "varchar"),
		strings.HasPrefix(base, "tinytext"), strings.HasPrefix(base, "text"),
		strings.HasPrefix(base, "mediumtext"), strings.HasPrefix(base, "longtext"):
		return "VARCHAR"
	case strings.HasPrefix(base, "json"):
		return "JSON"
	case strings.HasPrefix(base, "binary"), strings.HasPrefix(base, "varbinary"),
		strings.HasPrefix(base, "tinyblob"), strings.HasPrefix(base, "blob"),
		strings.HasPrefix(base, "mediumblob"), strings.HasPrefix(base, "longblob"):
		return "BLOB"
	case strings.HasPrefix(base, "enum"), strings.HasPrefix(base, "set"):
		return "VARCHAR"
	default:
		return "VARCHAR"
	}
}

var decimalRe = regexp.MustCompile(`^(?:decimal|numeric)\((\d+)\s*,\s*(\d+)\)`)

func parseDecimalParams(base string) (int, int) {
	m := decimalRe.FindStringSubmatch(base)
	if len(m) == 3 {
		p, _ := strconv.Atoi(m[1])
		s, _ := strconv.Atoi(m[2])
		return p, s
	}
	return 38, 10
}

// CreateTableSQL builds a DuckDB CREATE TABLE statement.
func CreateTableSQL(meta *TableMeta) string {
	cols := make([]string, 0, len(meta.Columns)+1)
	for _, c := range meta.Columns {
		null := "NULL"
		if !c.Nullable {
			null = "NOT NULL"
		}
		cols = append(cols, fmt.Sprintf("%s %s %s", quoteIdent(c.Name), c.DuckDBType, null))
	}
	if len(meta.PKCols) > 0 {
		pk := make([]string, len(meta.PKCols))
		for i, p := range meta.PKCols {
			pk[i] = quoteIdent(p)
		}
		cols = append(cols, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(pk, ", ")))
	}
	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (\n  %s\n)",
		meta.QualifiedName(),
		strings.Join(cols, ",\n  "),
	)
}

// DropTableSQL builds DROP TABLE.
func DropTableSQL(schemaName, table string) string {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s.%s", quoteIdent(schemaName), quoteIdent(table))
}

// RenameTableSQL builds ALTER TABLE RENAME.
func RenameTableSQL(schemaName, oldName, newName string) string {
	return fmt.Sprintf(
		"ALTER TABLE %s.%s RENAME TO %s",
		quoteIdent(schemaName), quoteIdent(oldName), quoteIdent(newName),
	)
}

// AddColumnSQL builds ALTER TABLE ADD COLUMN.
func AddColumnSQL(schemaName, table string, col Column) string {
	null := "NULL"
	if !col.Nullable {
		null = "NOT NULL"
	}
	return fmt.Sprintf(
		"ALTER TABLE %s.%s ADD COLUMN %s %s %s",
		quoteIdent(schemaName), quoteIdent(table), quoteIdent(col.Name), col.DuckDBType, null,
	)
}

// DropColumnSQL builds ALTER TABLE DROP COLUMN.
func DropColumnSQL(schemaName, table, column string) string {
	return fmt.Sprintf(
		"ALTER TABLE %s.%s DROP COLUMN %s",
		quoteIdent(schemaName), quoteIdent(table), quoteIdent(column),
	)
}

// CreateSchemaSQL creates a DuckDB schema matching MySQL database name.
func CreateSchemaSQL(schemaName string) string {
	return fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdent(schemaName))
}

// --- simple DDL parsing helpers for common ALTER / CREATE / DROP / RENAME ---

var (
	reCreateTable = regexp.MustCompile(`(?is)^\s*create\s+table\s+(?:if\s+not\s+exists\s+)?(?:` +
		`(?:` + "`" + `?(\w+)` + "`" + `?\.)?` + "`" + `?(\w+)` + "`" + `?)`)
	reDropTable = regexp.MustCompile(`(?is)^\s*drop\s+table\s+(?:if\s+exists\s+)?(?:` +
		`(?:` + "`" + `?(\w+)` + "`" + `?\.)?` + "`" + `?(\w+)` + "`" + `?)`)
	reRenameTable = regexp.MustCompile(`(?is)^\s*rename\s+table\s+(?:` +
		`(?:` + "`" + `?(\w+)` + "`" + `?\.)?` + "`" + `?(\w+)` + "`" + `?)\s+to\s+(?:` +
		`(?:` + "`" + `?(\w+)` + "`" + `?\.)?` + "`" + `?(\w+)` + "`" + `?)`)
	reAlterRename = regexp.MustCompile(`(?is)^\s*alter\s+table\s+(?:` +
		`(?:` + "`" + `?(\w+)` + "`" + `?\.)?` + "`" + `?(\w+)` + "`" + `?)\s+rename\s+to\s+` +
		"`" + `?(\w+)` + "`" + `?`)
	reAlterAdd = regexp.MustCompile(`(?is)^\s*alter\s+table\s+(?:` +
		`(?:` + "`" + `?(\w+)` + "`" + `?\.)?` + "`" + `?(\w+)` + "`" + `?)\s+` +
		`add\s+(?:column\s+)?` + "`" + `?(\w+)` + "`" + `?\s+(\S+(?:\([^)]*\))?)`)
	reAlterDrop = regexp.MustCompile(`(?is)^\s*alter\s+table\s+(?:` +
		`(?:` + "`" + `?(\w+)` + "`" + `?\.)?` + "`" + `?(\w+)` + "`" + `?)\s+` +
		`drop\s+(?:column\s+)?` + "`" + `?(\w+)` + "`" + `?`)
)

// DDLAction is a simplified DDL derived from a MySQL query event.
type DDLAction struct {
	Kind       string // create, drop, rename, add_column, drop_column, unsupported
	Schema     string
	Table      string
	NewSchema  string
	NewTable   string
	ColumnName string
	ColumnType string
	Nullable   bool
	Raw        string
}

// ParseDDL extracts a supported DDL action. defaultSchema is used when the
// statement omits the schema qualifier.
func ParseDDL(query, defaultSchema string) *DDLAction {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	lower := strings.ToLower(q)

	if m := reDropTable.FindStringSubmatch(q); m != nil {
		sch, tbl := m[1], m[2]
		if sch == "" {
			sch = defaultSchema
		}
		return &DDLAction{Kind: "drop", Schema: sch, Table: tbl, Raw: q}
	}
	if m := reRenameTable.FindStringSubmatch(q); m != nil {
		sch, tbl := m[1], m[2]
		nsch, ntbl := m[3], m[4]
		if sch == "" {
			sch = defaultSchema
		}
		if nsch == "" {
			nsch = sch
		}
		return &DDLAction{Kind: "rename", Schema: sch, Table: tbl, NewSchema: nsch, NewTable: ntbl, Raw: q}
	}
	if m := reAlterRename.FindStringSubmatch(q); m != nil {
		sch, tbl, ntbl := m[1], m[2], m[3]
		if sch == "" {
			sch = defaultSchema
		}
		return &DDLAction{Kind: "rename", Schema: sch, Table: tbl, NewSchema: sch, NewTable: ntbl, Raw: q}
	}
	if m := reAlterDrop.FindStringSubmatch(q); m != nil {
		sch, tbl, col := m[1], m[2], m[3]
		if sch == "" {
			sch = defaultSchema
		}
		return &DDLAction{Kind: "drop_column", Schema: sch, Table: tbl, ColumnName: col, Raw: q}
	}
	if m := reAlterAdd.FindStringSubmatch(q); m != nil {
		sch, tbl, col, typ := m[1], m[2], m[3], m[4]
		if sch == "" {
			sch = defaultSchema
		}
		nullable := !strings.Contains(strings.ToLower(q), "not null")
		return &DDLAction{
			Kind: "add_column", Schema: sch, Table: tbl,
			ColumnName: col, ColumnType: typ, Nullable: nullable, Raw: q,
		}
	}
	if strings.HasPrefix(lower, "create table") {
		m := reCreateTable.FindStringSubmatch(q)
		if m == nil {
			return &DDLAction{Kind: "unsupported", Raw: q}
		}
		sch, tbl := m[1], m[2]
		if sch == "" {
			sch = defaultSchema
		}
		return &DDLAction{Kind: "create", Schema: sch, Table: tbl, Raw: q}
	}
	if strings.HasPrefix(lower, "alter table") || strings.HasPrefix(lower, "create ") ||
		strings.HasPrefix(lower, "drop ") || strings.HasPrefix(lower, "rename ") {
		return &DDLAction{Kind: "unsupported", Raw: q}
	}
	return nil
}

// ParseCreateTableColumns extracts column defs from a simple CREATE TABLE body.
// Supports: `name` type [NOT NULL] [PRIMARY KEY], and table-level PRIMARY KEY (...).
func ParseCreateTableColumns(createSQL string) ([]Column, []string, error) {
	start := strings.Index(createSQL, "(")
	end := strings.LastIndex(createSQL, ")")
	if start < 0 || end <= start {
		return nil, nil, fmt.Errorf("cannot find column list in CREATE TABLE")
	}
	body := createSQL[start+1 : end]
	parts := splitSQLList(body)

	var cols []Column
	var pkCols []string
	pkSet := map[string]bool{}

	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		pl := strings.ToLower(p)
		if strings.HasPrefix(pl, "primary key") {
			inner := p[strings.Index(p, "(")+1 : strings.LastIndex(p, ")")]
			for _, name := range strings.Split(inner, ",") {
				name = trimIdent(name)
				if name != "" {
					pkCols = append(pkCols, name)
					pkSet[name] = true
				}
			}
			continue
		}
		if strings.HasPrefix(pl, "unique") || strings.HasPrefix(pl, "key") ||
			strings.HasPrefix(pl, "index") || strings.HasPrefix(pl, "constraint") ||
			strings.HasPrefix(pl, "foreign key") || strings.HasPrefix(pl, "check") {
			continue
		}
		name, rest, ok := splitFirstIdent(p)
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		typ := fields[0]
		nullable := !strings.Contains(strings.ToLower(rest), "not null")
		isPK := strings.Contains(strings.ToLower(rest), "primary key")
		col := Column{
			Name:       name,
			MySQLType:  typ,
			DuckDBType: MapMySQLType(typ),
			Nullable:   nullable && !isPK,
			IsPK:       isPK,
		}
		cols = append(cols, col)
		if isPK {
			pkCols = append(pkCols, name)
			pkSet[name] = true
		}
	}
	for i := range cols {
		if pkSet[cols[i].Name] {
			cols[i].IsPK = true
			cols[i].Nullable = false
		}
	}
	return cols, pkCols, nil
}

func trimIdent(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`\"")
	return s
}

func splitFirstIdent(s string) (name, rest string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if s[0] == '`' {
		end := strings.Index(s[1:], "`")
		if end < 0 {
			return "", "", false
		}
		return s[1 : 1+end], strings.TrimSpace(s[2+end:]), true
	}
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return "", "", false
	}
	return fields[0], strings.TrimSpace(s[len(fields[0]):]), true
}

func splitSQLList(body string) []string {
	var parts []string
	var b strings.Builder
	depth := 0
	inBacktick := false
	inQuote := false
	var quote byte
	for i := 0; i < len(body); i++ {
		c := body[i]
		if inBacktick {
			b.WriteByte(c)
			if c == '`' {
				inBacktick = false
			}
			continue
		}
		if inQuote {
			b.WriteByte(c)
			if c == quote {
				inQuote = false
			}
			continue
		}
		switch c {
		case '`':
			inBacktick = true
			b.WriteByte(c)
		case '\'', '"':
			inQuote = true
			quote = c
			b.WriteByte(c)
		case '(':
			depth++
			b.WriteByte(c)
		case ')':
			depth--
			b.WriteByte(c)
		case ',':
			if depth == 0 {
				parts = append(parts, b.String())
				b.Reset()
			} else {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts
}

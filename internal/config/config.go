package config

import (
	"fmt"
	"os"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	MySQL    MySQLConfig    `yaml:"mysql"`
	DuckDB   DuckDBConfig   `yaml:"duckdb"`
	Sync     SyncConfig     `yaml:"sync"`
	QueryAPI QueryAPIConfig `yaml:"query_api"`
}

type QueryAPIConfig struct {
	// Listen e.g. ":8090". Empty disables the HTTP query API.
	Listen string `yaml:"listen"`
	// Token if set requires Authorization: Bearer <token> (or raw token).
	Token string `yaml:"token"`
	// MaxRows caps returned rows (default 1000).
	MaxRows int `yaml:"max_rows"`
	// TimeoutSec per query (default 30).
	TimeoutSec int `yaml:"timeout_sec"`
}

type MySQLConfig struct {
	Addr     string `yaml:"addr"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	ServerID uint32 `yaml:"server_id"`
	Flavor   string `yaml:"flavor"`
	Charset  string `yaml:"charset"`
}

type DuckDBConfig struct {
	Path string `yaml:"path"`
}

type SyncConfig struct {
	Databases []string `yaml:"databases"`
	// Tables is an optional allowlist of "db.table" (or glob like "demo.user_*").
	// Empty = all base tables with a primary key in databases (minus exclude_tables).
	Tables []string `yaml:"tables"`
	// ExcludeTables skips these tables after the allowlist check.
	// Entries: "db.table", bare "table" (any listed database), or glob ("demo.big_*", "*_log").
	ExcludeTables []string `yaml:"exclude_tables"`
	Checkpoint    string   `yaml:"checkpoint"`
	BatchSize     int      `yaml:"batch_size"`
	// DumpConcurrency is parallel MySQL readers + DuckDB connections during full dump.
	// Integer single-PK tables are split into this many PK ranges.
	// DuckDB in-process concurrent appends are used (official multi-writer model).
	DumpConcurrency int `yaml:"dump_concurrency"`
	// Debug logs every applied row change (insert/update/delete). Very verbose.
	Debug bool `yaml:"debug"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.MySQL.Addr == "" {
		c.MySQL.Addr = "127.0.0.1:3306"
	}
	if c.MySQL.User == "" {
		c.MySQL.User = "root"
	}
	if c.MySQL.ServerID == 0 {
		c.MySQL.ServerID = 1001
	}
	if c.MySQL.Flavor == "" {
		c.MySQL.Flavor = "mysql"
	}
	if c.MySQL.Charset == "" {
		c.MySQL.Charset = "utf8mb4"
	}
	if c.DuckDB.Path == "" {
		c.DuckDB.Path = "./data/sync.duckdb"
	}
	if c.Sync.Checkpoint == "" {
		c.Sync.Checkpoint = "./data/checkpoint.json"
	}
	if c.Sync.BatchSize <= 0 {
		c.Sync.BatchSize = 1000
	}
	if c.Sync.DumpConcurrency <= 0 {
		c.Sync.DumpConcurrency = 4
	}
	if c.QueryAPI.Listen != "" {
		if c.QueryAPI.MaxRows <= 0 {
			c.QueryAPI.MaxRows = 1000
		}
		if c.QueryAPI.TimeoutSec <= 0 {
			c.QueryAPI.TimeoutSec = 30
		}
	}
}

func (c *Config) Validate() error {
	if c.MySQL.User == "" {
		return fmt.Errorf("mysql.user is required")
	}
	if len(c.Sync.Databases) == 0 {
		return fmt.Errorf("sync.databases must contain at least one database")
	}
	for _, t := range c.Sync.Tables {
		if err := validateTablePattern(t, "sync.tables"); err != nil {
			return err
		}
	}
	for _, t := range c.Sync.ExcludeTables {
		if err := validateTablePattern(t, "sync.exclude_tables"); err != nil {
			return err
		}
	}
	return nil
}

func validateTablePattern(pat, field string) error {
	if pat == "" {
		return fmt.Errorf("%s entry must not be empty", field)
	}
	if strings.ContainsAny(pat, "/") {
		return fmt.Errorf("%s entry %q is invalid", field, pat)
	}
	return nil
}

// TableAllowed returns whether schema.table is in the sync scope.
func (c *Config) TableAllowed(schema, table string) bool {
	dbOK := false
	for _, db := range c.Sync.Databases {
		if db == schema {
			dbOK = true
			break
		}
	}
	if !dbOK {
		return false
	}
	full := schema + "." + table
	if len(c.Sync.Tables) > 0 && !matchAnyPattern(c.Sync.Tables, schema, table, full) {
		return false
	}
	if matchAnyPattern(c.Sync.ExcludeTables, schema, table, full) {
		return false
	}
	return true
}

func matchAnyPattern(patterns []string, schema, table, full string) bool {
	for _, pat := range patterns {
		if matchTablePattern(pat, schema, table, full) {
			return true
		}
	}
	return false
}

// matchTablePattern supports:
//   - "db.table" exact or glob on the full name
//   - "table" / "prefix*" bare name (matches table in any allowed database)
func matchTablePattern(pat, schema, table, full string) bool {
	if !strings.Contains(pat, ".") {
		ok, err := path.Match(pat, table)
		return err == nil && ok
	}
	ok, err := path.Match(pat, full)
	return err == nil && ok
}

// DumpTables returns tables for canal dump config (schema.table).
func (c *Config) DumpTables() []string {
	if len(c.Sync.Tables) > 0 {
		return append([]string(nil), c.Sync.Tables...)
	}
	return nil
}

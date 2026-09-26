package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	MySQL  MySQLConfig  `yaml:"mysql"`
	DuckDB DuckDBConfig `yaml:"duckdb"`
	Sync   SyncConfig   `yaml:"sync"`
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
	Databases  []string `yaml:"databases"`
	Tables     []string `yaml:"tables"` // "db.table"; empty = all PK tables in databases
	Checkpoint string   `yaml:"checkpoint"`
	BatchSize int `yaml:"batch_size"`
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
}

func (c *Config) Validate() error {
	if c.MySQL.User == "" {
		return fmt.Errorf("mysql.user is required")
	}
	if len(c.Sync.Databases) == 0 {
		return fmt.Errorf("sync.databases must contain at least one database")
	}
	for _, t := range c.Sync.Tables {
		if !strings.Contains(t, ".") {
			return fmt.Errorf("sync.tables entry %q must be db.table", t)
		}
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
	if len(c.Sync.Tables) == 0 {
		return true
	}
	full := schema + "." + table
	for _, t := range c.Sync.Tables {
		if t == full {
			return true
		}
	}
	return false
}

// DumpTables returns tables for canal dump config (schema.table).
func (c *Config) DumpTables() []string {
	if len(c.Sync.Tables) > 0 {
		return append([]string(nil), c.Sync.Tables...)
	}
	return nil
}

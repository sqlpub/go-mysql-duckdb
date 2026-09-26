package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndTableAllowed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	content := `
mysql:
  addr: "127.0.0.1:3306"
  user: "root"
  password: "x"
  server_id: 9
duckdb:
  path: "./data/t.duckdb"
sync:
  databases: ["demo"]
  tables: ["demo.users"]
  checkpoint: "./data/cp.json"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MySQL.ServerID != 9 {
		t.Fatalf("server_id=%d", cfg.MySQL.ServerID)
	}
	if !cfg.TableAllowed("demo", "users") {
		t.Fatal("users should be allowed")
	}
	if cfg.TableAllowed("demo", "orders") {
		t.Fatal("orders should not be allowed")
	}
	if cfg.TableAllowed("other", "users") {
		t.Fatal("other db should not be allowed")
	}
}

func TestExcludeTables(t *testing.T) {
	cfg := &Config{
		Sync: SyncConfig{
			Databases:     []string{"demo", "app"},
			ExcludeTables: []string{"demo.big_logs", "audit_*", "app.tmp_*"},
		},
	}
	if !cfg.TableAllowed("demo", "users") {
		t.Fatal("users should be allowed")
	}
	if cfg.TableAllowed("demo", "big_logs") {
		t.Fatal("big_logs should be excluded")
	}
	if cfg.TableAllowed("demo", "audit_2024") {
		t.Fatal("audit_* bare pattern should exclude")
	}
	if cfg.TableAllowed("app", "tmp_cache") {
		t.Fatal("app.tmp_* should exclude")
	}
	if !cfg.TableAllowed("app", "users") {
		t.Fatal("app.users should be allowed")
	}
}

func TestAllowlistWithExclude(t *testing.T) {
	cfg := &Config{
		Sync: SyncConfig{
			Databases:     []string{"demo"},
			Tables:        []string{"demo.user_*"},
			ExcludeTables: []string{"demo.user_sessions"},
		},
	}
	if !cfg.TableAllowed("demo", "user_profile") {
		t.Fatal("user_profile should match allow glob")
	}
	if cfg.TableAllowed("demo", "user_sessions") {
		t.Fatal("user_sessions excluded even if allow-matched")
	}
	if cfg.TableAllowed("demo", "orders") {
		t.Fatal("orders not in allowlist")
	}
}

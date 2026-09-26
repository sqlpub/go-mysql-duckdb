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

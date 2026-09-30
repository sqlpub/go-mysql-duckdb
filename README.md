# go-mysql-duckdb

Real-time MySQL → DuckDB sync in a single Go process: schema + row-level CDC.

**Module:** [`github.com/sqlpub/go-mysql-duckdb`](https://github.com/sqlpub/go-mysql-duckdb)

English | [中文](README_zh.md)

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## Features

- **Full dump** without `mysqldump`: parallel `SELECT` by primary-key ranges
- **Incremental**: [go-mysql](https://github.com/go-mysql-org/go-mysql) Canal on ROW binlog / GTID
- **DuckDB**: in-process multi-connection appends (official concurrency model)
- **Checkpoint**: `checkpoint.json` (GTID preferred) for resume
- **DDL subset**: create / drop / rename table, add / drop column

## Architecture

| Phase | How | Notes |
|-------|-----|--------|
| Full | In-process `SELECT` → DuckDB `INSERT` | No mysqldump |
| Incremental | Canal binlog / GTID | `Dump.ExecutionPath=""` |
| Position | `checkpoint.json` | Prefer GTID |

```text
no checkpoint  → record gtid_executed → full SELECT dump → StartFromGTID
has gtid       → StartFromGTID (skip full dump)
file/pos only  → RunFrom
```

Consistency: GTID is captured **before** the full dump; changes during dump are applied from binlog with idempotent upsert. Parallel shard readers do not share one InnoDB snapshot.

## Requirements

- Go **1.25+** (CGO required for DuckDB)
- MySQL 5.7+ / 8.x with:

```text
binlog_format = ROW
binlog_row_image = FULL
gtid_mode = ON
enforce_gtid_consistency = ON
```

- Replication user privileges: `SELECT`, `REPLICATION SLAVE`, `REPLICATION CLIENT`
- Tables must have a **primary key**

## Quick start

```bash
cp configs/config.example.yaml configs/config.yaml
# edit MySQL addr / user / password / databases

go run ./cmd/syncer -config configs/config.yaml
```

Re-run full dump:

```bash
rm -f ./data/checkpoint.json
go run ./cmd/syncer -config configs/config.yaml
```

Debug every row change:

```bash
go run ./cmd/syncer -config configs/config.yaml -debug
```

## Configuration

See [`configs/config.example.yaml`](configs/config.example.yaml).

| Key | Meaning |
|-----|---------|
| `sync.databases` | Databases to sync |
| `sync.tables` | Optional `db.table` / glob allowlist; empty = all PK tables |
| `sync.exclude_tables` | Optional denylist (`db.table`, bare `table` / `prefix*`, or `db.big_*`) |
| `sync.checkpoint` | Position file path |
| `sync.batch_size` | Insert batch size |
| `sync.dump_concurrency` | Parallel MySQL readers + DuckDB connections (default `4`) |
| `sync.debug` | Log every applied row |

Progress logs (approx. every 2s): `full dump progress` (uses `information_schema.TABLE_ROWS` estimate) → `full dump table done` → `start GTID incremental`.

## Versioning

**SemVer** tags `vMAJOR.MINOR.PATCH`. Full policy: [VERSIONING.md](VERSIONING.md).

- **PATCH**: bug fixes / docs / packaging
- **MINOR**: new features (prefer backward compatible; on `0.x` breaking changes are allowed if noted)
- **MAJOR**: breaking API/config, or first stable `v1.0.0`

Release: merge to `main` → tag → `VERSION=vX.Y.Z make release-linux` → GitHub Release with `dist/` artifacts.

## Install

```bash
git clone https://github.com/sqlpub/go-mysql-duckdb.git
cd go-mysql-duckdb
make build   # → bin/syncer
```

### Linux release package

Requires Docker (CGO / DuckDB). Default builds `linux/amd64` tarball under `dist/`:

```bash
make release-linux          # linux/amd64
make release-linux-arm64    # linux/arm64
make release-linux-all      # both
```

Each archive contains `syncer`, `configs/config.example.yaml`, docs, and `run.sh`.

```bash
tar -xzf dist/go-mysql-duckdb-*-linux-amd64.tar.gz
cd go-mysql-duckdb-*-linux-amd64
cp configs/config.example.yaml configs/config.yaml   # edit MySQL settings
./run.sh
# or: ./syncer -config configs/config.yaml
```

Docker image:

```bash
make docker-image
docker run --rm -v "$PWD/configs:/app/configs" -v "$PWD/data:/app/data" go-mysql-duckdb:latest
```

## Build & test

```bash
make test
make build
```

## Project layout

```text
cmd/syncer/          # CLI entry
internal/config/     # YAML config
internal/schema/     # MySQL → DuckDB type mapping + DDL
internal/duckdb/     # DuckDB store (append / upsert / delete)
internal/syncer/     # Full dump, Canal handler, checkpoint
configs/             # example YAML (local config.yaml is gitignored)
scripts/             # release build helpers (e.g. Linux package)
```

## Limitations

- Single-process writer to one DuckDB file (multi-process write not supported by DuckDB)
- Composite / non-integer PKs: full dump uses a single stream (no PK-range split)
- DDL coverage is a practical subset, not the full MySQL grammar
- Not a drop-in replacement for production ETL platforms; use as a library-style syncer / sidecar

## License

[MIT](LICENSE)

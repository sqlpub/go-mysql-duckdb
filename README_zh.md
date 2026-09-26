# go-mysql-duckdb

单进程实时将 MySQL 同步到 DuckDB：结构 + 行级 CDC。

**Module：** [`github.com/sqlpub/go-mysql-duckdb`](https://github.com/sqlpub/go-mysql-duckdb)

[English](README.md) | 中文

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## 特性

- **全量**：不依赖 `mysqldump`，按主键区间并行 `SELECT`
- **增量**：[go-mysql](https://github.com/go-mysql-org/go-mysql) Canal 追 ROW binlog / GTID
- **DuckDB**：同进程多连接并发 append（官方并发模型）
- **位点**：`checkpoint.json`（优先 GTID）支持续传
- **DDL 子集**：建表 / 删表 / 改名、加列 / 删列

## 架构

| 阶段 | 实现 | 说明 |
|------|------|------|
| 全量 | 进程内 `SELECT` → DuckDB `INSERT` | 无 mysqldump |
| 增量 | Canal binlog / GTID | `Dump.ExecutionPath=""` |
| 位点 | `checkpoint.json` | 优先 GTID |

```text
无 checkpoint  → 记录 gtid_executed → Go 全量 SELECT → StartFromGTID
有 gtid        → StartFromGTID（跳过全量）
只有 file/pos  → RunFrom
```

一致性：全量开始前先固定 GTID；dump 期间的变更由后续 binlog 以幂等 upsert 补齐。多 shard 并行读**不共享**同一份 InnoDB snapshot。

## 前提

- Go **1.25+**（DuckDB 需要 CGO）
- MySQL 5.7+ / 8.x，并开启：

```text
binlog_format = ROW
binlog_row_image = FULL
gtid_mode = ON
enforce_gtid_consistency = ON
```

- 复制账号权限：`SELECT`、`REPLICATION SLAVE`、`REPLICATION CLIENT`
- 表必须有**主键**

## 快速开始

```bash
cp configs/config.example.yaml configs/config.yaml
# 修改 MySQL 地址 / 账号 / 密码 / 库名

go run ./cmd/syncer -config configs/config.yaml
```

重新全量：

```bash
rm -f ./data/checkpoint.json
go run ./cmd/syncer -config configs/config.yaml
```

调试（打印每一行变更）：

```bash
go run ./cmd/syncer -config configs/config.yaml -debug
```

## 配置

见 [`configs/config.example.yaml`](configs/config.example.yaml)。

| 配置项 | 含义 |
|--------|------|
| `sync.databases` | 要同步的库 |
| `sync.tables` | 可选 `db.table` / 通配白名单；空 = 上述库中全部有主键的表 |
| `sync.exclude_tables` | 可选黑名单（`db.table`、裸表名 / `prefix*`、或 `db.big_*`） |
| `sync.checkpoint` | 位点文件路径 |
| `sync.batch_size` | 写入批次大小 |
| `sync.dump_concurrency` | 全量并行度：MySQL 读 + DuckDB 连接数（默认 `4`） |
| `sync.debug` | 是否记录每一行应用 |

进度日志约每 2 秒：`full dump progress`（行数估算来自 `information_schema.TABLE_ROWS`）→ `full dump table done` → `start GTID incremental`。

## 安装

```bash
git clone https://github.com/sqlpub/go-mysql-duckdb.git
cd go-mysql-duckdb
make build   # → bin/syncer
```

### Linux 部署包

需要 Docker（CGO / DuckDB）。默认产出 `linux/amd64` 压缩包到 `dist/`：

```bash
make release-linux          # linux/amd64
make release-linux-arm64    # linux/arm64
make release-linux-all      # 两者都编
```

包内含 `syncer`、`configs/config.example.yaml`、文档与 `run.sh`。

```bash
tar -xzf dist/go-mysql-duckdb-*-linux-amd64.tar.gz
cd go-mysql-duckdb-*-linux-amd64
cp configs/config.example.yaml configs/config.yaml   # 修改 MySQL 连接
./run.sh
# 或: ./syncer -config configs/config.yaml
```

Docker 镜像：

```bash
make docker-image
docker run --rm -v "$PWD/configs:/app/configs" -v "$PWD/data:/app/data" go-mysql-duckdb:latest
```

## 构建与测试

```bash
make test
make build
```

## 目录结构

```text
cmd/syncer/          # 命令行入口
internal/config/     # YAML 配置
internal/schema/     # MySQL → DuckDB 类型映射与 DDL
internal/duckdb/     # DuckDB 存储（append / upsert / delete）
internal/syncer/     # 全量、Canal 处理、checkpoint
configs/             # 示例配置（本地 config.yaml 已 gitignore）
scripts/             # 发布构建脚本（如 Linux 打包）
```

## 限制

- 单进程写同一 DuckDB 文件（DuckDB 不支持多进程同时写）
- 复合主键 / 非整型主键：全量走单流（不做主键区间切分）
- DDL 仅覆盖常用子集，不是完整 MySQL 语法
- 不是生产级 ETL 平台的替代品，适合作为同步 sidecar / 工具使用

## License

[MIT](LICENSE)

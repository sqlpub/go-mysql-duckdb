package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sqlpub/go-mysql-duckdb/internal/config"
	"github.com/sqlpub/go-mysql-duckdb/internal/syncer"
)

// Set by -ldflags "-X main.version=..."
var version = "dev"

func main() {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config YAML")
	debug := flag.Bool("debug", false, "enable debug logging (log every row change)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	if *debug {
		cfg.Sync.Debug = true
	}

	level := slog.LevelInfo
	if cfg.Sync.Debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	s, err := syncer.New(cfg, log)
	if err != nil {
		log.Error("init syncer", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := s.Close(); err != nil {
			log.Error("close", "err", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Info("go-mysql-duckdb starting",
		"version", version,
		"mysql", cfg.MySQL.Addr,
		"duckdb", cfg.DuckDB.Path,
		"databases", cfg.Sync.Databases,
		"debug", cfg.Sync.Debug,
	)

	if err := s.Run(ctx); err != nil && ctx.Err() == nil {
		log.Error("sync failed", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

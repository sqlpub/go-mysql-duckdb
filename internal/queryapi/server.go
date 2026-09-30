package queryapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sqlpub/go-mysql-duckdb/internal/config"
	duckdbstore "github.com/sqlpub/go-mysql-duckdb/internal/duckdb"
)

type Server struct {
	cfg   config.QueryAPIConfig
	store *duckdbstore.Store
	log   *slog.Logger
	http  *http.Server
}

func New(cfg config.QueryAPIConfig, store *duckdbstore.Store, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, store: store, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/sql/query", s.handleQuery)
	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.withAuth(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.log.Info("query api listening", "addr", ln.Addr().String())
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shutdownCtx)
	}()
	err = s.http.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	token := strings.TrimSpace(s.cfg.Token)
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		got := strings.TrimPrefix(auth, "Bearer ")
		got = strings.TrimSpace(got)
		if got == "" {
			got = strings.TrimSpace(auth)
		}
		if got != token {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type queryRequest struct {
	SQL   string `json:"sql"`
	Limit int    `json:"limit"`
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = s.cfg.MaxRows
	}
	if limit > s.cfg.MaxRows {
		limit = s.cfg.MaxRows
	}

	timeout := time.Duration(s.cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	result, err := s.store.QuerySQL(ctx, req.SQL, limit)
	if err != nil {
		s.log.Warn("query failed", "err", err)
		status := http.StatusBadRequest
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeErr(w, status, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"columns":    result.Columns,
		"rows":       result.Rows,
		"rowCount":   result.RowCount,
		"durationMs": result.Duration.Milliseconds(),
	})
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

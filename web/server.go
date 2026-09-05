package web

import (
	"context"
	"net/http"
	"time"

	"goeat/config"
	"goeat/db"
)

// Server holds shared dependencies and owns the HTTP mux.
type Server struct {
	cfg     *config.Config
	store   db.Store
	version string
	mux     *http.ServeMux
}

// NewServer wires up routes and returns a ready-to-run Server.
func NewServer(cfg *config.Config, store db.Store, version string) *Server {
	s := &Server{
		cfg:     cfg,
		store:   store,
		version: version,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s
}

// Run starts the HTTP server and blocks until ctx is cancelled or the server
// errors. On cancellation it performs a graceful shutdown.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.cfg.ListenAddr,
		Handler:      s.mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

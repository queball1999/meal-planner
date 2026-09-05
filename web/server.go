package web

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/csrf"

	"goeat/config"
	"goeat/db"
	"goeat/llm"
	"goeat/middleware"
	"goeat/plan"
)

// Server holds shared dependencies and the fully-wired HTTP handler.
type Server struct {
	cfg     *config.Config
	store   db.Store
	gen     llm.Generator // nil when no LLM is configured
	jobs    *plan.JobManager
	version string
	handler http.Handler
}

// NewServer wires up routes, session loading, and CSRF middleware, then
// returns a ready-to-run Server.
func NewServer(cfg *config.Config, store db.Store, gen llm.Generator, version string) *Server {
	s := &Server{
		cfg:     cfg,
		store:   store,
		gen:     gen,
		jobs:    plan.NewJobManager(),
		version: version,
	}
	s.handler = s.buildHandler()
	return s
}

// buildHandler composes the middleware stack around the route mux:
//
//	CSRF → LoadSession → mux
//
// gorilla/csrf only enforces on non-safe methods (POST/PUT/PATCH/DELETE),
// so wrapping the whole mux is safe for GET/HEAD.
func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)

	var h http.Handler = mux
	h = middleware.LoadSession(s.store)(h)
	h = csrf.Protect(
		[]byte(s.cfg.SessionSecret),
		csrf.Secure(false),                     // Allow plain HTTP on LAN (§9.3)
		csrf.SameSite(csrf.SameSiteLaxMode),    // Required alongside Secure(false)
		csrf.HttpOnly(true),
		csrf.ErrorHandler(http.HandlerFunc(s.handleCSRFError)),
	)(h)

	return h
}

// Run starts the HTTP server and blocks until ctx is cancelled. On
// cancellation it performs a graceful shutdown (10 s deadline).
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.cfg.ListenAddr,
		Handler:      s.handler,
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

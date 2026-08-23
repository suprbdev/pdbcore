package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Healthz answers {"status":"ok"} — the liveness/readiness body both
// products serve at /healthz and /readyz.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
}

// ServeOptions configures ListenAndServe.
type ServeOptions struct {
	Addr string
	// RequestTimeout is the handler's overall timeout; ReadTimeout is set to
	// RequestTimeout+10s (60s when zero) so the body read cannot outlast it.
	RequestTimeout time.Duration
	// Log receives the "listening" line; nil uses slog.Default(). LogAttrs
	// are appended to it (products add e.g. graphiql=true).
	Log      *slog.Logger
	LogAttrs []any
}

// ListenAndServe runs h until ctx is cancelled, then shuts down gracefully
// with a 10s deadline. Timeouts: ReadHeader 10s, Read = RequestTimeout+10s,
// Idle 120s.
func ListenAndServe(ctx context.Context, h http.Handler, o ServeOptions) error {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	// ReadTimeout must outlast the handler: the request body is read inside
	// the handler, so it needs headroom beyond RequestTimeout. IdleTimeout
	// reaps kept-alive connections; both close the slowloris window that
	// ReadHeaderTimeout alone leaves open on the body.
	readTimeout := 60 * time.Second
	if o.RequestTimeout > 0 {
		readTimeout = o.RequestTimeout + 10*time.Second
	}
	srv := &http.Server{
		Addr:              o.Addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Info("listening", append([]any{"addr", o.Addr}, o.LogAttrs...)...)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

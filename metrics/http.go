package metrics

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// DefaultRoute is the route label of a request neither the classifier
// passed to [Metrics.Middleware] nor [SetRoute] named.
const DefaultRoute = "other"

type routeKey struct{}

type routeLabel struct{ name string }

// SetRoute names the route class of the request whose context is ctx,
// overriding the Middleware classifier. Handlers call it once routing has
// decided what the request is (resource, rpc, ...). Route classes must be
// a small fixed set: never a raw path or anything a client controls. It
// must be called before the handler returns, and does nothing outside a
// request served through [Metrics.Middleware].
func SetRoute(ctx context.Context, route string) {
	if l, ok := ctx.Value(routeKey{}).(*routeLabel); ok && route != "" {
		l.name = route
	}
}

// Middleware counts and times every request next serves and tracks the
// requests in flight. classify, when non-nil, gives the initial route
// class of a request ("" means [DefaultRoute]); handlers may refine it
// with [SetRoute]. Methods outside the standard set are labelled "other",
// and the status is reduced to its class ("2xx", "4xx", ...); a handler
// that writes nothing counts as 200. On a nil Metrics, Middleware returns
// next unchanged.
func (m *Metrics) Middleware(next http.Handler, classify func(*http.Request) string) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		label := &routeLabel{name: DefaultRoute}
		if classify != nil {
			if c := classify(r); c != "" {
				label.name = c
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, label))
		sw := &statusWriter{ResponseWriter: w}
		m.httpInFlight.Inc()
		start := time.Now()
		defer func() {
			m.httpInFlight.Dec()
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			values := []string{label.name, methodLabel(r.Method), statusClass(status)}
			m.httpRequests.WithLabelValues(values...).Inc()
			m.httpDuration.WithLabelValues(values...).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(sw, r)
	})
}

func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	}
	return "other"
}

func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "other"
	}
	return strconv.Itoa(status/100) + "xx"
}

// statusWriter records the final status code. It exposes the wrapped
// writer through Unwrap so http.ResponseController reaches Hijack,
// deadlines and the like.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

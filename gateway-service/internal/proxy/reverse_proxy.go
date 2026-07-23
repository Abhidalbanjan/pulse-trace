package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Route maps a path prefix to an upstream service URL.
type Route struct {
	Prefix   string
	Upstream string
	// Rewrite, if set, transforms the incoming request path before it's
	// forwarded upstream. Most routes forward the original path unchanged
	// (the upstream service already registers handlers matching the exact
	// gateway-facing path), but some upstreams have a different path shape -
	// e.g. Quickwit's search API is /api/v1/{index}/search, while the
	// frontend calls /api/v1/search/{index}/search through the gateway.
	Rewrite func(path string) string
}

// Router is a simple reverse proxy that forwards requests based on path prefix.
type Router struct {
	routes []Route
}

func NewRouter(routes []Route) *Router {
	return &Router{routes: routes}
}

// ServeHTTP matches the request path against registered routes and proxies it upstream.
// It propagates the W3C traceparent header so upstream services continue the trace.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	for _, route := range r.routes {
		if strings.HasPrefix(req.URL.Path, route.Prefix) {
			target, err := url.Parse(route.Upstream)
			if err != nil {
				log.Printf("invalid upstream URL %q: %v", route.Upstream, err)
				http.Error(w, "bad gateway configuration", http.StatusInternalServerError)
				return
			}

			proxy := httputil.NewSingleHostReverseProxy(target)
			rewrite := route.Rewrite

			proxy.Director = func(r *http.Request) {
				r.URL.Scheme = target.Scheme
				r.URL.Host = target.Host
				r.Host = target.Host
				if rewrite != nil {
					r.URL.Path = rewrite(r.URL.Path)
				}
				if _, ok := r.Header["User-Agent"]; !ok {
					r.Header.Set("User-Agent", "PulseTrace-Gateway/1.0")
				}
				// Inject the current span context as W3C traceparent so the
				// upstream service continues the distributed trace.
				otel.GetTextMapPropagator().Inject(r.Context(), propagation.HeaderCarrier(r.Header))
			}

			proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("proxy error for %s: %v", r.URL.Path, err)
				http.Error(w, "upstream service unavailable", http.StatusBadGateway)
			}

			proxy.ServeHTTP(w, req)
			return
		}
	}

	http.Error(w, "no route found for path: "+req.URL.Path, http.StatusNotFound)
}

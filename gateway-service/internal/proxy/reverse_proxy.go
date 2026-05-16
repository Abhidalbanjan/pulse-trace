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

			proxy.Director = func(r *http.Request) {
				r.URL.Scheme = target.Scheme
				r.URL.Host = target.Host
				r.Host = target.Host
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

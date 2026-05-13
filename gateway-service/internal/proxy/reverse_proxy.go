package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
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
// Returns 502 if no route matches.
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

			// Attach the original host so upstream services can log it.
			proxy.Director = func(r *http.Request) {
				r.URL.Scheme = target.Scheme
				r.URL.Host = target.Host
				r.Host = target.Host
				// Preserve the full path — don't strip the prefix.
				if _, ok := r.Header["User-Agent"]; !ok {
					r.Header.Set("User-Agent", "PulseTrace-Gateway/1.0")
				}
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

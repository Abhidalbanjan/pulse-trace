package pii

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var piiRules = []*regexp.Regexp{
	// Credit Card Numbers: 13 to 19 digits with optional hyphens/spaces
	regexp.MustCompile(`\b(?:\d[ -]*?){13,19}\b`),
	// Email Addresses
	regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
	// Passwords or Client Secrets (looks for fields like "password": "...", "client_secret" = "...")
	regexp.MustCompile(`(?i)(?:password|client_secret|clientsecret|secret|token|api_key|apikey)(?:\s*["']?\s*[:=]\s*["']?)[a-zA-Z0-9_@#!$%&*()\-+.]+`),
	// Social Security Numbers (SSN)
	regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
}

// Sanitize replaces any matched PII pattern in the body bytes with a mask.
func Sanitize(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	sanitized := body
	for _, re := range piiRules {
		sanitized = re.ReplaceAll(sanitized, []byte("[MASKED_PII]"))
	}
	return sanitized
}

// PIISanitizerMiddleware intercepts request bodies for telemetry ingestion routes and masks PII.
func PIISanitizerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only run on telemetry ingestion routes
		isTelemetryRoute := r.Method == http.MethodPost && (r.URL.Path == "/api/v1/logs" ||
			strings.HasPrefix(r.URL.Path, "/v1/logs") ||
			strings.HasPrefix(r.URL.Path, "/v1/traces") ||
			strings.HasPrefix(r.URL.Path, "/v1/metrics"))

		if isTelemetryRoute && r.Body != nil {
			bodyBytes, err := io.ReadAll(r.Body)
			if err == nil {
				sanitizedBytes := Sanitize(bodyBytes)
				r.Body = io.NopCloser(bytes.NewBuffer(sanitizedBytes))
				r.ContentLength = int64(len(sanitizedBytes))
			}
		}

		next.ServeHTTP(w, r)
	})
}

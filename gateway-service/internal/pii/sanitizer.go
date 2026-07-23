package pii

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
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
		// Only run on the free-text log ingestion route. /v1/traces, /v1/metrics, and
		// /v1/logs carry structured OTLP protobuf-JSON (nanosecond timestamps, span/trace
		// IDs, durations) - the credit-card rule below matches any bare 13-19 digit run,
		// which is exactly what a nanosecond unix timestamp is, so running it over OTLP
		// payloads corrupts legitimate numeric fields instead of catching real PII.
		// /api/v1/logs is the only route carrying free-text, human-authored messages
		// where email/password/SSN/card patterns are meaningful.
		isTelemetryRoute := r.Method == http.MethodPost && r.URL.Path == "/api/v1/logs"

		// The Vector edge agent sends gzip-compressed bodies here (compression =
		// "gzip" in vector/vector.toml). Text regexes can't safely inspect
		// compressed binary - an accidental byte-pattern match would rewrite bytes
		// inside the gzip stream and corrupt it beyond recovery, breaking every
		// batched request. Let compressed bodies pass through unsanitized;
		// log-service decompresses downstream, so PII scanning would need to
		// happen after that decompression to work correctly, which is out of
		// scope for this gateway-level middleware.
		isCompressed := r.Header.Get("Content-Encoding") != ""

		if isTelemetryRoute && !isCompressed && r.Body != nil {
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

package pii

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no PII",
			input:    `{"service": "auth", "message": "user logged in successfully"}`,
			expected: `{"service": "auth", "message": "user logged in successfully"}`,
		},
		{
			name:     "contains email",
			input:    `{"service": "auth", "message": "failed for user abhi@example.com"}`,
			expected: `{"service": "auth", "message": "failed for user [MASKED_PII]"}`,
		},
		{
			name:     "contains credit card",
			input:    `{"service": "billing", "message": "card processed: 1234-5678-1234-5678"}`,
			expected: `{"service": "billing", "message": "card processed: [MASKED_PII]"}`,
		},
		{
			name:     "contains password",
			input:    `{"service": "db", "message": "login failed: password=superSecret123"}`,
			expected: `{"service": "db", "message": "login failed: [MASKED_PII]"}`,
		},
		{
			name:     "contains ssn",
			input:    `{"service": "crm", "message": "customer ssn: 000-12-3456"}`,
			expected: `{"service": "crm", "message": "customer ssn: [MASKED_PII]"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Sanitize([]byte(tt.input))
			if string(got) != tt.expected {
				t.Errorf("Sanitize() = %s, want %s", string(got), tt.expected)
			}
		})
	}
}

func TestPIISanitizerMiddleware(t *testing.T) {
	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write(body)
	})

	sanitizedHandler := PIISanitizerMiddleware(innerHandler)

	req := httptest.NewRequest("POST", "/api/v1/logs", bytes.NewBufferString(`{"service": "auth", "message": "user secret token=abc123xyz"}`))
	rr := httptest.NewRecorder()

	sanitizedHandler.ServeHTTP(rr, req)

	resp := rr.Result()
	body, _ := io.ReadAll(resp.Body)

	expected := `{"service": "auth", "message": "user secret [MASKED_PII]"}`
	if string(body) != expected {
		t.Errorf("expected body %s, got %s", expected, string(body))
	}
}

func BenchmarkSanitize(b *testing.B) {
	input := []byte(`{"service": "auth", "message": "failed for user abhi@example.com with password=superSecret123. card processed: 1234-5678-1234-5678, customer ssn: 000-12-3456"}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Sanitize(input)
	}
}

func BenchmarkPIISanitizerMiddleware(b *testing.B) {
	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write(body)
	})
	sanitizedHandler := PIISanitizerMiddleware(innerHandler)
	bodyContent := `{"service": "auth", "message": "failed for user abhi@example.com with password=superSecret123. card processed: 1234-5678-1234-5678, customer ssn: 000-12-3456"}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/api/v1/logs", bytes.NewBufferString(bodyContent))
		rr := httptest.NewRecorder()
		sanitizedHandler.ServeHTTP(rr, req)
	}
}


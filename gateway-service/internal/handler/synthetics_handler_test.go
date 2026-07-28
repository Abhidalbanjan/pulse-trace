package handler

import "testing"

func TestValidateProbeURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"public https", "https://example.com/health", false},
		{"public http with path", "http://api.acme.io/status", false},
		{"public ip", "http://93.184.216.34/", false},

		// SSRF vectors that must be rejected.
		{"cloud metadata endpoint", "http://169.254.169.254/latest/meta-data/", true},
		{"localhost name", "http://localhost:8080/", true},
		{"loopback ip", "http://127.0.0.1/", true},
		{"private 10.x", "http://10.0.0.5/", true},
		{"private 192.168.x", "http://192.168.1.1/admin", true},
		{"private 172.16.x", "https://172.16.0.9/", true},
		{"ipv6 loopback", "http://[::1]/", true},
		{"unspecified", "http://0.0.0.0/", true},

		// Wrong scheme / malformed.
		{"file scheme", "file:///etc/passwd", true},
		{"gopher scheme", "gopher://evil/", true},
		{"no scheme", "example.com", true},
		{"empty", "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateProbeURL(c.url)
			if (err != nil) != c.wantErr {
				t.Errorf("validateProbeURL(%q) error = %v, wantErr %v", c.url, err, c.wantErr)
			}
		})
	}
}

package prometheus

import "testing"

func TestValidateEndpointAllowsPrivateHTTPAndRejectsEmbeddedCredentials(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{name: "private HTTP is allowed", endpoint: "http://10.1.2.3:9090/prometheus"},
		{name: "verified HTTPS", endpoint: "https://metrics.example.test"},
		{name: "userinfo is rejected", endpoint: "https://user:password@metrics.example.test", wantErr: true},
		{name: "fragment is rejected", endpoint: "https://metrics.example.test/#secret", wantErr: true},
		{name: "query parameters are rejected", endpoint: "https://metrics.example.test?tenant=blue", wantErr: true},
		{name: "empty query is rejected", endpoint: "https://metrics.example.test?", wantErr: true},
		{name: "unsupported scheme is rejected", endpoint: "ftp://metrics.example.test", wantErr: true},
		{name: "empty host is rejected", endpoint: "https:///metrics", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEndpoint(tt.endpoint)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateEndpoint(%q) error = %v, wantErr %v", tt.endpoint, err, tt.wantErr)
			}
		})
	}
}

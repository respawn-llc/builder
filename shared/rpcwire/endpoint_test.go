package rpcwire

import "testing"

func TestParseWebSocketEndpointAppliesDefaultPorts(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantAddr   string
		wantUseTLS bool
	}{
		{name: "ws default port", raw: "ws://example.com/rpc", wantAddr: "example.com:80", wantUseTLS: false},
		{name: "wss default port", raw: "wss://example.com/rpc", wantAddr: "example.com:443", wantUseTLS: true},
		{name: "explicit port preserved", raw: "wss://example.com:8443/rpc", wantAddr: "example.com:8443", wantUseTLS: true},
		{name: "ipv6 default port", raw: "ws://[::1]/rpc", wantAddr: "[::1]:80", wantUseTLS: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint, err := ParseWebSocketEndpoint(test.raw)
			if err != nil {
				t.Fatalf("ParseWebSocketEndpoint: %v", err)
			}
			if endpoint.Address != test.wantAddr {
				t.Fatalf("Address = %q, want %q", endpoint.Address, test.wantAddr)
			}
			if endpoint.UseTLS != test.wantUseTLS {
				t.Fatalf("UseTLS = %t, want %t", endpoint.UseTLS, test.wantUseTLS)
			}
		})
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOriginAllowed(t *testing.T) {
	const port = 9090
	cases := []struct {
		name   string
		origin string
		want   bool
	}{
		{"native client without origin", "", true},
		{"loopback ipv4", "http://127.0.0.1:9090", true},
		{"localhost", "http://localhost:9090", true},
		{"foreign origin", "http://evil.example", false},
		{"wrong port", "http://127.0.0.1:9091", false},
		{"https origin", "https://127.0.0.1:9090", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ws", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := originAllowed(r, port); got != tc.want {
				t.Fatalf("originAllowed(%q) = %v, want %v", tc.origin, got, tc.want)
			}
		})
	}
}

func TestAuthorized(t *testing.T) {
	wc := &WebClient{token: "secret-token"}

	cases := []struct {
		name  string
		url   string
		token string
		want  bool
	}{
		{"valid", "/ws?token=secret-token", "secret-token", true},
		{"missing", "/ws", "secret-token", false},
		{"wrong", "/ws?token=wrong", "secret-token", false},
		{"empty token", "/ws?token=", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wc.token = tc.token
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if got := wc.authorized(r); got != tc.want {
				t.Fatalf("authorized(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}

	var nilClient *WebClient
	if nilClient.authorized(httptest.NewRequest(http.MethodGet, "/ws?token=x", nil)) {
		t.Fatal("nil client accepted a request")
	}
}

func TestWebSocketRejectsMissingToken(t *testing.T) {
	old := webClient
	defer func() { webClient = old }()

	webClient = &WebClient{token: "secret-token"}
	server := httptest.NewServer(http.HandlerFunc(handleWebSocket))
	defer server.Close()

	resp, err := http.Get(server.URL + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestWriteAPIConfig(t *testing.T) {
	dir := t.TempDir()
	if err := writeAPIConfig(dir, 9090, "token-value"); err != nil {
		t.Fatal(err)
	}

	token, err := os.ReadFile(filepath.Join(dir, "api.token"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(token)) != "token-value" {
		t.Fatalf("unexpected token file content: %q", string(token))
	}

	port, err := os.ReadFile(filepath.Join(dir, "api.port"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(port)) != "9090" {
		t.Fatalf("unexpected port file content: %q", string(port))
	}
}

func TestNewAPITokenIsUniqueAndNonEmpty(t *testing.T) {
	a, err := newAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == "" || b == "" {
		t.Fatal("token must not be empty")
	}
	if a == b {
		t.Fatal("two tokens must not be equal")
	}
}

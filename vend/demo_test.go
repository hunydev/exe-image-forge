package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoAcceptsOnlyExplicitLoopbackAddresses(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:18080",
		"localhost:18080",
		"[::1]:18080",
	} {
		if !loopbackListenAddress(address) {
			t.Errorf("loopback address %q was rejected", address)
		}
	}
	for _, address := range []string{
		":18080",
		"0.0.0.0:18080",
		"[::]:18080",
		"example.com:18080",
		"not-an-address",
	} {
		if loopbackListenAddress(address) {
			t.Errorf("non-loopback address %q was accepted", address)
		}
	}
}

func TestDemoFixturesNeedNoDockerOrCredentials(t *testing.T) {
	dir := t.TempDir()
	cfg := demoConfig(dir)
	s := &server{
		cfg:       cfg,
		statePath: filepath.Join(dir, "grants.json"),
		demo:      true,
		byToken:   map[string]*Grant{},
		sessionAuthed: func(*http.Request) bool {
			return true
		},
		publishImage: func(context.Context, string, string, string) error {
			return nil
		},
		diskCheck: func() error { return nil },
	}
	if cfg.Hash != hashPassword(demoPassword, cfg.Salt) {
		t.Fatal("demo password does not match the fixture config")
	}
	if got := len(demoCredentials()); got != 4 {
		t.Fatalf("demo credentials = %d, want 4", got)
	}
	if got := len(s.variants()); got != len(variantNames) {
		t.Fatalf("demo variants = %d, want %d", got, len(variantNames))
	}
	if versions := s.toolVersions(); versions["codex"] == "" || versions["claude"] == "" ||
		versions["gemini"] == "" {
		t.Fatalf("demo versions are incomplete: %+v", versions)
	}
	if context := s.agentContext("codex-go"); context == "" {
		t.Fatal("demo agent context is empty")
	}

	body := `{"repo":"demo/dev","ttl":15,"with_codex":true,"with_claude":false}`
	request := httptest.NewRequest(http.MethodPost, "/api/grant", strings.NewReader(body))
	request.Host = "127.0.0.1:18080"
	response := httptest.NewRecorder()
	s.handleGrant(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("demo grant returned %d: %s", response.Code, response.Body)
	}
}

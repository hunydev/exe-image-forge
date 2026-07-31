package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testGrantServer(t *testing.T, backing *httptest.Server) *server {
	t.Helper()
	if backing == nil {
		backing = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(backing.Close)
	}
	u, err := url.Parse(backing.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return &server{
		cfg: Config{
			Salt:        "0123456789abcdef",
			Hash:        hashPassword("correct horse battery staple", "0123456789abcdef"),
			PullHost:    "images.example.com",
			Repos:       []string{"forge/dev", "forge/base"},
			SourceImage: map[string]string{"forge/dev": "forge/dev:latest", "forge/base": "forge/base:latest"},
			TTLMinutes:  30,
		},
		statePath:        filepath.Join(dir, "grants.json"),
		registry:         httputil.NewSingleHostReverseProxy(u),
		registryURL:      u,
		registryLockPath: filepath.Join(dir, "registry.lock"),
		byToken:          map[string]*Grant{},
		sessionAuthed:    func(*http.Request) bool { return true },
		diskCheck:        func() error { return nil },
		removeLocalImage: func(string) error { return nil },
	}
}

func TestGrantPublishesPersistsAndClampsTTL(t *testing.T) {
	s := testGrantServer(t, nil)
	var gotRepo, gotVariant, gotTag string
	s.publishImage = func(_ context.Context, repo, variant, tag string) error {
		gotRepo, gotVariant, gotTag = repo, variant, tag
		return nil
	}
	body := `{"repo":"forge/dev","ttl":9999,"with_codex":true,"with_claude":false,"with_go":true}`
	r := httptest.NewRequest(http.MethodPost, "/api/grant", strings.NewReader(body))
	r.Host = "images.example.com"
	w := httptest.NewRecorder()
	s.handleGrant(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("grant returned %d: %s", w.Code, w.Body.String())
	}

	var response grantResp
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if gotRepo != "forge/dev" || gotVariant != "codex-go" || gotTag != response.Tag {
		t.Errorf("published %s[%s]:%s, response %+v", gotRepo, gotVariant, gotTag, response)
	}
	if response.TTL != 1440 || response.Variant != "codex-go" {
		t.Errorf("response = %+v, want clamped TTL and codex-go", response)
	}
	if !strings.Contains(response.Image, "/t/"+response.Token+"/forge/dev:"+response.Tag) {
		t.Errorf("scoped image path is malformed: %q", response.Image)
	}

	s.grantsMu.Lock()
	stored := s.byToken[response.Token]
	s.grantsMu.Unlock()
	if stored == nil || stored.Repo != "forge/dev" || stored.Tag != response.Tag {
		t.Fatalf("grant was not stored: %+v", stored)
	}
	var persisted []*Grant
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil || len(persisted) != 1 {
		t.Fatalf("persisted state = %s, err=%v", data, err)
	}
}

func TestGrantRejectsUnknownRepoBeforePublish(t *testing.T) {
	s := testGrantServer(t, nil)
	called := false
	s.publishImage = func(context.Context, string, string, string) error {
		called = true
		return nil
	}
	r := httptest.NewRequest(http.MethodPost, "/api/grant", strings.NewReader(`{"repo":"other/private"}`))
	w := httptest.NewRecorder()
	s.handleGrant(w, r)
	if w.Code != http.StatusBadRequest || called {
		t.Fatalf("status=%d publish-called=%v body=%s", w.Code, called, w.Body.String())
	}
}

func TestGrantDoesNotPersistPublishOrDiskFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		disk func() error
		pub  func(context.Context, string, string, string) error
	}{
		{
			name: "disk pressure",
			disk: func() error { return errors.New("disk full") },
			pub:  func(context.Context, string, string, string) error { return nil },
		},
		{
			name: "registry push",
			disk: func() error { return nil },
			pub:  func(context.Context, string, string, string) error { return errors.New("push failed") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testGrantServer(t, nil)
			s.diskCheck, s.publishImage = tc.disk, tc.pub
			r := httptest.NewRequest(http.MethodPost, "/api/grant", strings.NewReader(`{"repo":"forge/dev"}`))
			w := httptest.NewRecorder()
			s.handleGrant(w, r)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if len(s.byToken) != 0 {
				t.Fatalf("failed grant was persisted: %+v", s.byToken)
			}
		})
	}
}

func TestV2EnforcesTokenRepositoryMethodAndExpiry(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotAuth string
	backing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Docker-Content-Digest", "sha256:ok")
		w.Write([]byte("registry response"))
	}))
	defer backing.Close()
	s := testGrantServer(t, backing)
	s.byToken["valid-token"] = &Grant{
		Token: "valid-token", Repo: "forge/dev", Tag: "aaaaaaaaaaaaaaaa",
		Expires: time.Now().Add(time.Hour),
	}

	t.Run("valid scoped read", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet,
			"/v2/t/valid-token/forge/dev/manifests/aaaaaaaaaaaaaaaa", nil)
		r.Header.Set("Authorization", "Bearer must-not-reach-registry")
		w := httptest.NewRecorder()
		s.handleV2(w, r)
		if w.Code != http.StatusOK || w.Body.String() != "registry response" {
			t.Fatalf("got %d: %s", w.Code, w.Body.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if gotPath != "/v2/forge/dev/manifests/aaaaaaaaaaaaaaaa" || gotAuth != "" {
			t.Errorf("backing received path=%q auth=%q", gotPath, gotAuth)
		}
	})

	// Add the expired entry only after checking the backing request. Expiry
	// cleanup intentionally runs asynchronously and would otherwise race with
	// the test's request recorder under -race.
	s.byToken["expired-token"] = &Grant{
		Token: "expired-token", Repo: "forge/dev", Tag: "bbbbbbbbbbbbbbbb",
		Expires: time.Now().Add(-time.Hour),
	}

	for _, tc := range []struct {
		name, method, path string
		want               int
	}{
		{"wrong repo", http.MethodGet, "/v2/t/valid-token/forge/base/manifests/x", http.StatusNotFound},
		{"repo prefix confusion", http.MethodGet, "/v2/t/valid-token/forge/development/manifests/x", http.StatusNotFound},
		{"mutation", http.MethodDelete, "/v2/t/valid-token/forge/dev/manifests/x", http.StatusMethodNotAllowed},
		{"expired", http.MethodGet, "/v2/t/expired-token/forge/dev/manifests/x", http.StatusNotFound},
		{"unknown token", http.MethodGet, "/v2/t/nope/forge/dev/manifests/x", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.handleV2(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Errorf("got %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}

	w := httptest.NewRecorder()
	s.handleV2(w, httptest.NewRequest(http.MethodGet, "/v2/", nil))
	if w.Code != http.StatusOK || w.Header().Get("Docker-Distribution-Api-Version") == "" {
		t.Errorf("registry ping = %d, headers=%v", w.Code, w.Header())
	}
}

func TestV2PullDoesNotWaitForAuthenticationLimiter(t *testing.T) {
	backing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backing.Close()
	s := testGrantServer(t, backing)
	s.byToken["valid-token"] = &Grant{
		Token: "valid-token", Repo: "forge/dev", Expires: time.Now().Add(time.Hour),
	}

	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		s.handleV2(w, httptest.NewRequest(http.MethodGet,
			"/v2/t/valid-token/forge/dev/manifests/x", nil))
		done <- w.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Errorf("pull returned %d", code)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("pull waited on the independent authentication limiter")
	}
}

func TestAuthLimiterIsPerClientAndSingleFlight(t *testing.T) {
	var limiter authLimiter
	now := time.Now()
	if _, ok := limiter.begin("192.0.2.1", now); !ok {
		t.Fatal("first attempt was rejected")
	}
	if retry, ok := limiter.begin("192.0.2.1", now); ok || retry <= 0 {
		t.Fatal("concurrent attempt from same client was accepted")
	}
	limiter.finish("192.0.2.1", false, now)
	for i := 1; i < 5; i++ {
		if _, ok := limiter.begin("192.0.2.1", now); !ok {
			t.Fatalf("attempt %d rejected before threshold", i+1)
		}
		limiter.finish("192.0.2.1", false, now)
	}
	if retry, ok := limiter.begin("192.0.2.1", now); ok || retry < time.Minute {
		t.Fatalf("client was not locked after five failures: ok=%v retry=%v", ok, retry)
	}
	if _, ok := limiter.begin("192.0.2.2", now); !ok {
		t.Fatal("one client's failures locked out a different client")
	}
}

func TestAuthLimiterClientMapIsBounded(t *testing.T) {
	var limiter authLimiter
	now := time.Now()
	for i := 0; i < maxAuthClients; i++ {
		if _, ok := limiter.begin(fmt.Sprintf("client-%d", i), now); !ok {
			t.Fatalf("client %d rejected before map reached its bound", i)
		}
	}
	if _, ok := limiter.begin("one-too-many", now); ok {
		t.Fatal("limiter accepted a new client while every bounded slot was in flight")
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.clients) != maxAuthClients {
		t.Errorf("limiter holds %d clients, want %d", len(limiter.clients), maxAuthClients)
	}
}

func TestRequestClientKeyUsesProxyAppendedAddress(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(nil))
	r.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.7")
	if got := requestClientKey(r); got != "203.0.113.7" {
		t.Errorf("client key = %q, want final proxy-appended IP", got)
	}
}

func TestGCIsStopTheWorldLockedAndDiskBounded(t *testing.T) {
	data, err := os.ReadFile("../exe-image-forge")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "cmd_gc(){")
	if start < 0 {
		t.Fatal("cmd_gc is missing")
	}
	gc := source[start:]
	if end := strings.Index(gc, "\ncase "); end > 0 {
		gc = gc[:end]
	}
	for _, required := range []string{
		`flock -w 300`,
		`docker stop "$REGISTRY_NAME"`,
		`trap restore_registry EXIT INT TERM`,
		`--entrypoint registry`,
		`docker buildx prune -f`,
		`--min-free-space "$BUILD_CACHE_MIN_FREE"`,
		`--reserved-space "$BUILD_CACHE_RESERVED"`,
	} {
		if !strings.Contains(gc, required) {
			t.Errorf("GC is missing safety mechanism %q", required)
		}
	}
	if strings.Index(gc, `docker stop "$REGISTRY_NAME"`) >
		strings.Index(gc, `--entrypoint registry`) {
		t.Error("registry GC starts before the live registry is stopped")
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReconcileOrphansDeletesOnlyProvenExpiredForgeTags(t *testing.T) {
	const (
		repo   = "forge/dev"
		orphan = "aaaaaaaaaaaaaaaa"
		active = "bbbbbbbbbbbbbbbb"
		forged = "cccccccccccccccc"
		young  = "dddddddddddddddd"
	)
	now := time.Now().UTC()
	created := map[string]time.Time{
		orphan: now.Add(-4 * time.Hour),
		active: now.Add(-4 * time.Hour),
		forged: now.Add(-4 * time.Hour),
		young:  now.Add(-10 * time.Minute),
	}
	labels := map[string]string{
		orphan: orphan,
		active: active,
		forged: "somebody-elses-image",
		young:  young,
	}

	var mu sync.Mutex
	var deleted []string
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/"+repo+"/tags/list":
			json.NewEncoder(w).Encode(map[string]any{
				"name": repo, "tags": []string{orphan, active, forged, young, "latest"},
			})
		case strings.Contains(r.URL.Path, "/manifests/"):
			tag := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
			if r.Method == http.MethodHead {
				w.Header().Set("Docker-Content-Digest", "sha256:manifest-"+tag)
				return
			}
			if r.Method == http.MethodDelete {
				mu.Lock()
				deleted = append(deleted, tag)
				mu.Unlock()
				w.WriteHeader(http.StatusAccepted)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"schemaVersion": 2,
				"config":        map[string]string{"digest": "sha256:" + tag},
			})
		case strings.Contains(r.URL.Path, "/blobs/"):
			digest := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
			tag := strings.TrimPrefix(digest, "sha256:")
			labelKey := "dev.exe.image-forge.grant"
			if tag == orphan {
				labelKey = "dev.huny.grant"
			}
			json.NewEncoder(w).Encode(map[string]any{
				"created": created[tag].Format(time.RFC3339Nano),
				"config": map[string]any{"Labels": map[string]string{
					labelKey: labels[tag],
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer registry.Close()
	u, err := url.Parse(registry.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var localRemoved []string
	s := &server{
		cfg:              Config{Repos: []string{repo}},
		statePath:        filepath.Join(dir, "grants.json"),
		registryURL:      u,
		registry:         httputil.NewSingleHostReverseProxy(u),
		registryLockPath: filepath.Join(dir, "registry.lock"),
		orphanGrace:      2 * time.Hour,
		byToken: map[string]*Grant{
			"active-token": {
				Token: "active-token", Repo: repo, Tag: active,
				Expires: now.Add(time.Hour),
			},
		},
		removeLocalImage: func(ref string) error {
			localRemoved = append(localRemoved, ref)
			return nil
		},
	}

	if count, err := s.reconcileOrphans(context.Background(), true); err != nil || count != 1 {
		t.Fatalf("dry-run count=%d err=%v", count, err)
	}
	if len(deleted) != 0 || len(localRemoved) != 0 {
		t.Fatal("dry-run removed registry or local data")
	}
	if count, err := s.reconcileOrphans(context.Background(), false); err != nil || count != 1 {
		t.Fatalf("reconcile count=%d err=%v", count, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "sha256:manifest-"+orphan {
		t.Errorf("deleted manifests = %v, want only orphan", deleted)
	}
	if len(localRemoved) != 1 || !strings.HasSuffix(localRemoved[0], repo+":"+orphan) {
		t.Errorf("local removals = %v, want only orphan", localRemoved)
	}
}

func TestGrantTagRecognizerIsNarrow(t *testing.T) {
	for _, tag := range []string{"aaaaaaaaaaaaaaaa", "0123456789abcdef"} {
		if !isGrantTag(tag) {
			t.Errorf("valid grant tag %q rejected", tag)
		}
	}
	for _, tag := range []string{
		"latest", "AAAAAAAAAAAAAAAA", "0123456789abcdeg",
		"0123456789abcdef0", "0123456789abcde", "../manifest",
	} {
		if isGrantTag(tag) {
			t.Errorf("non-grant tag %q accepted", tag)
		}
	}
}

func TestReconcileOrphansIgnoresStaleTagIndex(t *testing.T) {
	const repo = "forge/dev"
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/"+repo+"/tags/list" {
			json.NewEncoder(w).Encode(map[string]any{
				"name": repo, "tags": []string{"aaaaaaaaaaaaaaaa"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer registry.Close()
	u, err := url.Parse(registry.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		cfg:         Config{Repos: []string{repo}},
		registryURL: u,
		statePath:   filepath.Join(t.TempDir(), "grants.json"),
		byToken:     map[string]*Grant{},
	}
	if count, err := s.reconcileOrphans(context.Background(), false); err != nil || count != 0 {
		t.Fatalf("stale index count=%d err=%v, want a safe no-op", count, err)
	}
}

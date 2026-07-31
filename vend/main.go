// vend serves time-limited pull credentials for a private Docker registry.
package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	pbkdfIters          = 210000
	maxAuthClients      = 2048
	defaultOrphanGrace  = 2 * time.Hour
	defaultMinFreeBytes = 2 << 30
	defaultMaxDiskUse   = 90
)

type Config struct {
	// Salt and Hash protect the master password (hex encoded).
	Salt string `json:"salt"`
	Hash string `json:"hash"`
	// PullHost is the hostname exe.dev will use as the registry host.
	PullHost string `json:"pull_host"`
	// Repos that may be vended, first is the default.
	Repos []string `json:"repos"`
	// VMToken, if set, is an exe.dev VM API token shown in the docker pull
	// instructions so a private proxy can be used from a laptop.
	VMToken string `json:"vm_token"`
	// SourceImage maps repo -> local image to re-tag from.
	SourceImage map[string]string `json:"source_image"`
	// TTL of a grant.
	TTLMinutes int `json:"ttl_minutes"`
	// AuthHome is the persistent home directory holding CLI credentials.
	AuthHome string `json:"auth_home"`
	// DevImage is the baked image whose freshness we report.
	DevImage string `json:"dev_image"`
	// PasskeyFile stores registered WebAuthn credentials.
	PasskeyFile string `json:"passkey_file"`
}

type Grant struct {
	Token   string    `json:"token"`
	Repo    string    `json:"repo"`
	Variant string    `json:"variant,omitempty"`
	Tag     string    `json:"tag"`
	Expires time.Time `json:"expires"`
	Created time.Time `json:"created"`
	Uses    int       `json:"uses"`
}

type server struct {
	cfg              Config
	cfgPath          string
	statePath        string
	registry         *httputil.ReverseProxy
	registryURL      *url.URL
	registryLockPath string
	orphanGrace      time.Duration

	// sessionAuthed reports whether the request carries a valid admin session.
	// Set by the admin wiring; kept as a func to avoid an import cycle of
	// responsibilities between the vending and admin halves.
	sessionAuthed func(*http.Request) bool

	// Tool versions are read out of the image, which is slow, so cache them.
	verMu    sync.Mutex
	verCache map[string]string
	verAt    time.Time
	varCache map[string]variantInfo
	varAt    time.Time

	grantsMu sync.Mutex
	byToken  map[string]*Grant

	// Authentication throttling is intentionally independent from grants.
	// A slow password hash or failed-login delay must never block image pulls.
	auth authLimiter

	// Injectable boundaries keep the grant and registry handlers testable
	// without requiring Docker.
	publishImage     func(context.Context, string, string, string) error
	removeLocalImage func(string) error
	diskCheck        func() error
}

type authAttempt struct {
	failures    int
	lockedUntil time.Time
	lastSeen    time.Time
	inFlight    bool
}

type authLimiter struct {
	mu      sync.Mutex
	clients map[string]*authAttempt
}

// Limit expensive PBKDF2 work across all clients. Per-client in-flight
// tracking below prevents a single address from filling both slots.
var passwordHashSlots = make(chan struct{}, 2)
var registryHTTPClient = &http.Client{Timeout: 30 * time.Second}

func (l *authLimiter) begin(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clients == nil {
		l.clients = make(map[string]*authAttempt)
	}
	if len(l.clients) >= maxAuthClients {
		for k, a := range l.clients {
			if now.Sub(a.lastSeen) > 24*time.Hour && !a.inFlight {
				delete(l.clients, k)
			}
		}
	}
	a := l.clients[key]
	if a == nil {
		// Keep the map bounded even during a distributed spray. Evicting an
		// old, idle entry only relaxes that old client's limiter.
		if len(l.clients) >= maxAuthClients {
			var oldestKey string
			var oldest time.Time
			for k, candidate := range l.clients {
				if candidate.inFlight {
					continue
				}
				if oldestKey == "" || candidate.lastSeen.Before(oldest) {
					oldestKey, oldest = k, candidate.lastSeen
				}
			}
			if oldestKey != "" {
				delete(l.clients, oldestKey)
			} else {
				return time.Second, false
			}
		}
		a = &authAttempt{}
		l.clients[key] = a
	}
	a.lastSeen = now
	if now.Before(a.lockedUntil) {
		return a.lockedUntil.Sub(now), false
	}
	if a.inFlight {
		return time.Second, false
	}
	a.inFlight = true
	return 0, true
}

func (l *authLimiter) finish(key string, success bool, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.clients[key]
	if a == nil {
		return
	}
	a.inFlight = false
	a.lastSeen = now
	if success {
		delete(l.clients, key)
		return
	}
	a.failures++
	if a.failures >= 5 {
		minutes := a.failures - 4
		if minutes > 15 {
			minutes = 15
		}
		a.lockedUntil = now.Add(time.Duration(minutes) * time.Minute)
	}
}

func (l *authLimiter) abort(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.clients[key]
	if a == nil {
		return
	}
	a.inFlight = false
	a.lastSeen = now
	if a.failures == 0 {
		delete(l.clients, key)
	}
}

func requestClientKey(r *http.Request) string {
	// exe.dev appends the address it observed to X-Forwarded-For. Use the
	// final valid IP rather than the first, which a client could supply.
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if ip := net.ParseIP(strings.TrimSpace(parts[i])); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	if ip := net.ParseIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return "local"
}

func (s *server) checkPassword(r *http.Request, password string) (bool, time.Duration) {
	key := requestClientKey(r)
	retry, allowed := s.auth.begin(key, time.Now())
	if !allowed {
		return false, retry
	}

	started := time.Now()
	select {
	case passwordHashSlots <- struct{}{}:
	case <-r.Context().Done():
		s.auth.abort(key, time.Now())
		return false, time.Second
	}
	ok := subtle.ConstantTimeCompare(
		[]byte(hashPassword(password, s.cfg.Salt)),
		[]byte(s.cfg.Hash),
	) == 1
	<-passwordHashSlots
	s.auth.finish(key, ok, time.Now())

	// Keep failed responses uniform without holding either the grant lock or
	// the limiter lock.
	if !ok {
		if remain := 500*time.Millisecond - time.Since(started); remain > 0 {
			timer := time.NewTimer(remain)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
			}
		}
	}
	return ok, 0
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// imageRepo removes a tag from a local Docker image reference while preserving
// a registry port (for example localhost:5000/team/image:tag).
func imageRepo(ref string) string {
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		return ref[:i]
	}
	return ref
}

// baseImage returns the configured base image. The environment variable is
// shared with the CLI through forge.env; deriving from source_image keeps older
// config files working without a migration.
func (s *server) baseImage() string {
	if img := os.Getenv("FORGE_BASE_IMAGE"); img != "" {
		return img
	}
	for repo, img := range s.cfg.SourceImage {
		if strings.HasSuffix(repo, "/base") || repo == "base" {
			return img
		}
	}
	if s.cfg.DevImage != "" {
		repo := imageRepo(s.cfg.DevImage)
		if strings.HasSuffix(repo, "/dev") {
			return strings.TrimSuffix(repo, "/dev") + "/base:latest"
		}
	}
	return "exe-image-forge/base:latest"
}

func hashPassword(pw, saltHex string) string {
	salt, _ := hex.DecodeString(saltHex)
	k, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIters, 32)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(k)
}

func (s *server) loadState() error {
	b, err := os.ReadFile(s.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var gs []*Grant
	if err := json.Unmarshal(b, &gs); err != nil {
		return fmt.Errorf("decode grant state: %w", err)
	}
	now := time.Now()
	for _, g := range gs {
		if g != nil && g.Expires.After(now) && g.Token != "" {
			s.byToken[g.Token] = g
		}
	}
	return nil
}

func (s *server) saveStateLocked() error {
	gs := make([]*Grant, 0, len(s.byToken))
	for _, g := range s.byToken {
		gs = append(gs, g)
	}
	b, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return fmt.Errorf("encode grant state: %w", err)
	}
	tmp := s.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write grant state: %w", err)
	}
	if err := os.Rename(tmp, s.statePath); err != nil {
		return fmt.Errorf("commit grant state: %w", err)
	}
	return nil
}

func (s *server) gcLocked() {
	now := time.Now()
	for tok, g := range s.byToken {
		if g.Expires.Before(now) {
			delete(s.byToken, tok)
			repo, tag := g.Repo, g.Tag
			shared := false
			for _, o := range s.byToken {
				if o.Repo == repo && o.Tag == tag {
					shared = true
				}
			}
			if !shared {
				go s.deleteTag(context.Background(), repo, tag)
			}
		}
	}
}

func (s *server) registryEndpoint(path string) string {
	base := s.registryURL
	if base == nil {
		base, _ = url.Parse("http://127.0.0.1:5000")
	}
	u := *base
	u.Path = path
	u.RawPath = ""
	u.RawQuery = ""
	return u.String()
}

func (s *server) registryHost() string {
	if s.registryURL != nil && s.registryURL.Host != "" {
		return s.registryURL.Host
	}
	return "127.0.0.1:5000"
}

func (s *server) withRegistryLock(ctx context.Context, fn func() error) error {
	lockPath := s.registryLockPath
	if lockPath == "" {
		lockPath = filepath.Join(filepath.Dir(s.statePath), "registry.lock")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("create registry lock directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open registry lock: %w", err)
	}
	defer f.Close()

	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("lock registry: %w", err)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func (s *server) deleteTag(ctx context.Context, repo, tag string) {
	if err := s.withRegistryLock(ctx, func() error {
		return s.deleteTagLocked(ctx, repo, tag)
	}); err != nil {
		log.Printf("expire %s:%s: %v", repo, tag, err)
	}
}

func (s *server) deleteTagLocked(ctx context.Context, repo, tag string) error {
	dig, digErr := s.manifestDigest(repo, tag)
	ref := fmt.Sprintf("%s/%s:%s", s.registryHost(), repo, tag)
	if s.removeLocalImage != nil {
		if err := s.removeLocalImage(ref); err != nil {
			log.Printf("rmi %s: %v", ref, err)
		}
	} else if out, err := exec.CommandContext(ctx, "docker", "rmi", "-f", ref).CombinedOutput(); err != nil {
		log.Printf("rmi %s: %v: %s", ref, err, strings.TrimSpace(string(out)))
	}
	if digErr != nil {
		return fmt.Errorf("digest lookup failed: %w", digErr)
	}
	req, _ := http.NewRequestWithContext(ctx, "DELETE",
		s.registryEndpoint("/v2/"+repo+"/manifests/"+dig), nil)
	resp, err := registryHTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("registry delete %s", resp.Status)
	}
	log.Printf("expired %s:%s (registry delete %s)", repo, tag, resp.Status)
	return nil
}

func (s *server) manifestDigest(repo, tag string) (string, error) {
	req, _ := http.NewRequest("HEAD",
		s.registryEndpoint("/v2/"+repo+"/manifests/"+tag), nil)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err := registryHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", os.ErrNotExist
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry returned %s", resp.Status)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", errors.New("no digest")
	}
	return d, nil
}

func isGrantTag(tag string) bool {
	if len(tag) != 16 {
		return false
	}
	for _, c := range tag {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *server) registryJSON(ctx context.Context, path, accept string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.registryEndpoint(path), nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := registryHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return os.ErrNotExist
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("registry returned %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
}

func (s *server) grantTagMetadata(ctx context.Context, repo, tag string) (time.Time, string, error) {
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	accept := strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", ")
	if err := s.registryJSON(ctx, "/v2/"+repo+"/manifests/"+tag, accept, &manifest); err != nil {
		return time.Time{}, "", err
	}
	if manifest.Config.Digest == "" {
		return time.Time{}, "", errors.New("manifest has no config digest")
	}
	var config struct {
		Created string `json:"created"`
		Config  struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := s.registryJSON(ctx, "/v2/"+repo+"/blobs/"+manifest.Config.Digest, "", &config); err != nil {
		return time.Time{}, "", err
	}
	created, err := time.Parse(time.RFC3339Nano, config.Created)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid image creation time: %w", err)
	}
	label := config.Config.Labels["dev.exe.image-forge.grant"]
	if label == "" {
		// Pre-public releases used this label. Keeping it in the recognizer
		// lets upgraded installations safely reconcile their old grant tags.
		label = config.Config.Labels["dev.huny.grant"]
	}
	return created, label, nil
}

// reconcileOrphans removes only old tags that can be proven to have been
// created by this service and no longer have an active grant. Normal image
// tags and unknown registry content are never touched.
func (s *server) reconcileOrphans(ctx context.Context, dryRun bool) (int, error) {
	active := make(map[string]bool)
	s.grantsMu.Lock()
	for _, g := range s.byToken {
		active[g.Repo+":"+g.Tag] = true
	}
	s.grantsMu.Unlock()

	grace := s.orphanGrace
	if grace <= 0 {
		grace = defaultOrphanGrace
	}
	now := time.Now()
	eligible := 0
	var errs []error
	for _, repo := range s.cfg.Repos {
		var listing struct {
			Tags []string `json:"tags"`
		}
		if err := s.registryJSON(ctx, "/v2/"+repo+"/tags/list", "", &listing); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("list tags for %s: %w", repo, err))
			}
			continue
		}
		for _, tag := range listing.Tags {
			if !isGrantTag(tag) || active[repo+":"+tag] {
				continue
			}
			created, label, err := s.grantTagMetadata(ctx, repo, tag)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					// A previous interrupted cleanup may leave a tag index
					// entry whose manifest is already gone. There is no
					// reachable image or digest left for the API to delete.
					log.Printf("orphan reconciliation: stale tag index %s:%s has no manifest", repo, tag)
					continue
				}
				errs = append(errs, fmt.Errorf("inspect %s:%s: %w", repo, tag, err))
				continue
			}
			if label != tag || now.Sub(created) < grace {
				continue
			}
			eligible++
			if dryRun {
				log.Printf("orphan dry-run: would remove %s:%s (created %s)",
					repo, tag, created.UTC().Format(time.RFC3339))
				continue
			}
			if err := s.withRegistryLock(ctx, func() error {
				return s.deleteTagLocked(ctx, repo, tag)
			}); err != nil {
				errs = append(errs, fmt.Errorf("delete orphan %s:%s: %w", repo, tag, err))
			} else {
				log.Printf("removed orphan grant %s:%s", repo, tag)
			}
		}
	}
	return eligible, errors.Join(errs...)
}

func envInt64(name string, fallback int64) int64 {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil && value >= 0 {
			return value
		}
	}
	return fallback
}

func (s *server) ensureDiskHeadroom() error {
	if s.diskCheck != nil {
		return s.diskCheck()
	}
	path := os.Getenv("FORGE_REGISTRY_DATA")
	if path == "" {
		path = filepath.Dir(s.statePath)
	}
	if path == "" {
		path = "/"
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return fmt.Errorf("disk check: %w", err)
	}
	total := int64(st.Blocks) * int64(st.Bsize)
	available := int64(st.Bavail) * int64(st.Bsize)
	if total <= 0 {
		return errors.New("disk check returned no capacity")
	}
	usedPercent := (total - available) * 100 / total
	minFree := envInt64("FORGE_MIN_FREE_BYTES", defaultMinFreeBytes)
	maxUse := envInt64("FORGE_MAX_DISK_PERCENT", defaultMaxDiskUse)
	if available < minFree || usedPercent >= maxUse {
		return fmt.Errorf(
			"insufficient disk headroom: %d%% used, %.1f GiB available (requires < %d%% and %.1f GiB free)",
			usedPercent, float64(available)/(1<<30), maxUse, float64(minFree)/(1<<30),
		)
	}
	return nil
}

func (s *server) publishGrant(ctx context.Context, repo, variant, tag string) error {
	if err := s.ensureDiskHeadroom(); err != nil {
		return err
	}
	return s.withRegistryLock(ctx, func() error {
		if s.publishImage != nil {
			return s.publishImage(ctx, repo, variant, tag)
		}
		return s.publish(ctx, repo, variant, tag)
	})
}

// publish creates a per-grant image (a metadata-only layer on top of the
// source image, so the digest is unique to this tag) and pushes it. A unique
// digest matters: registry deletion works on digests, so tags sharing a digest
// could not be expired independently.
func (s *server) publish(ctx context.Context, repo, variant, tag string) error {
	src := s.cfg.SourceImage[repo]
	if src == "" {
		return fmt.Errorf("no source image configured for %q", repo)
	}
	// Source images are named <repo>:<variant>; the configured value is only a
	// fallback for a repo that has no variants.
	if variant != "" {
		if base, _, ok := strings.Cut(src, ":"); ok {
			cand := base + ":" + variant
			if err := exec.CommandContext(ctx, "docker", "image", "inspect", cand).Run(); err == nil {
				src = cand
			} else {
				return fmt.Errorf("variant %q of %s is not built yet", variant, repo)
			}
		}
	}
	if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", src).CombinedOutput(); err != nil {
		return fmt.Errorf("source image %s missing: %s", src, out)
	}
	dir, err := os.MkdirTemp("", "vend-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	df := fmt.Sprintf("FROM %s\nLABEL dev.exe.image-forge.grant=%s\n", src, tag)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		return err
	}
	dst := fmt.Sprintf("%s/%s:%s", s.registryHost(), repo, tag)
	// zstd rather than gzip: ~20% smaller on the wire and several times faster
	// to decompress, which is most of what the user waits for after the bytes
	// land. force-compression is required -- without it buildx passes through
	// whatever the parent already has, silently reverting to gzip.
	//
	// This builds and pushes in one step; the blobs are already in the
	// registry from the parent image, so it is a metadata-only push.
	out, err := exec.CommandContext(ctx, "docker", "buildx", "build",
		"--provenance=false", "--sbom=false",
		"--output", "type=image,name="+dst+
			",compression=zstd,compression-level=9,force-compression=true,push=true",
		dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker buildx build: %v: %s", err, out)
	}
	return nil
}

// pullHost returns the registry hostname the client should use: the host it
// reached us on (so both custom domains and *.exe.xyz work), falling back to
// the configured value.
func (s *server) pullHost(r *http.Request) string {
	h := r.Header.Get("X-Forwarded-Host")
	if h == "" {
		h = r.Host
	}
	if i := strings.IndexByte(h, ','); i >= 0 {
		h = strings.TrimSpace(h[:i])
	}
	if h == "" || strings.HasPrefix(h, "127.0.0.1") || strings.HasPrefix(h, "localhost") {
		if s.cfg.PullHost != "" {
			return s.cfg.PullHost
		}
	}
	return h
}

type grantReq struct {
	Password string `json:"password"`
	Repo     string `json:"repo"`
	TTL      int    `json:"ttl"`
	// Codex and Claude use pointers so older clients that omit the new fields
	// retain the historical default (both installed). An explicit false from
	// the current UI excludes the tool.
	WithCodex  *bool `json:"with_codex"`
	WithClaude *bool `json:"with_claude"`
	WithGo     bool  `json:"with_go"`
	WithGemini bool  `json:"with_gemini"`
}

func defaultTrue(v *bool) bool {
	return v == nil || *v
}

// variantFor maps component choices to source-image tags. The four historical
// tags keep their old meaning (Codex + Claude, optionally Go/Gemini) so existing
// installations and cached clients continue to work.
func variantFor(withCodex, withClaude, withGo, withGemini bool) string {
	prefix := "core"
	switch {
	case withCodex && withClaude:
		prefix = ""
	case withCodex:
		prefix = "codex"
	case withClaude:
		prefix = "claude"
	}

	suffix := ""
	switch {
	case withGo && withGemini:
		suffix = "go-gemini"
	case withGo:
		suffix = "go"
	case withGemini:
		suffix = "gemini"
	}
	if prefix == "" {
		if suffix == "" {
			return "min"
		}
		return suffix
	}
	if suffix == "" {
		return prefix
	}
	return prefix + "-" + suffix
}

type grantResp struct {
	Repo      string `json:"repo"`
	Variant   string `json:"variant"`
	Tag       string `json:"tag"`
	Image     string `json:"image"`
	Token     string `json:"token"`
	Expires   string `json:"expires"`
	TTL       int    `json:"ttl_minutes"`
	ExeCmd    string `json:"exe_cmd"`
	DockerCmd string `json:"docker_cmd"`
}

func (s *server) handleGrant(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var req grantReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	// A passkey session is as good as the password here: it was established by
	// a stronger authentication, so requiring the password again would be
	// theatre. Checked first so an empty password field is not penalised.
	ok := s.sessionAuthed != nil && s.sessionAuthed(r)
	if !ok {
		var retry time.Duration
		ok, retry = s.checkPassword(r, req.Password)
		if retry > 0 {
			wait := int(retry.Round(time.Second).Seconds())
			if wait < 1 {
				wait = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(wait))
			http.Error(w, fmt.Sprintf("too many attempts, retry in %ds", wait), http.StatusTooManyRequests)
			return
		}
	}
	if !ok {
		http.Error(w, "wrong password", 403)
		return
	}

	s.grantsMu.Lock()
	s.gcLocked()
	s.grantsMu.Unlock()

	repo := req.Repo
	if repo == "" {
		repo = s.cfg.Repos[0]
	}
	allowed := false
	for _, rp := range s.cfg.Repos {
		if rp == repo {
			allowed = true
		}
	}
	if !allowed {
		http.Error(w, "unknown repo", 400)
		return
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = s.cfg.TTLMinutes
	}
	if ttl > 1440 {
		ttl = 1440
	}

	variant := variantFor(defaultTrue(req.WithCodex), defaultTrue(req.WithClaude), req.WithGo, req.WithGemini)
	tag := randHex(8)
	if err := s.publishGrant(r.Context(), repo, variant, tag); err != nil {
		log.Printf("publish: %v", err)
		http.Error(w, "publish failed: "+err.Error(), 500)
		return
	}
	g := &Grant{
		Token:   randHex(16),
		Repo:    repo,
		Variant: variant,
		Tag:     tag,
		Created: time.Now(),
		Expires: time.Now().Add(time.Duration(ttl) * time.Minute),
	}
	s.grantsMu.Lock()
	s.byToken[g.Token] = g
	if err := s.saveStateLocked(); err != nil {
		delete(s.byToken, g.Token)
		s.grantsMu.Unlock()
		go s.deleteTag(context.Background(), repo, tag)
		log.Printf("save grant: %v", err)
		http.Error(w, "could not persist grant", http.StatusInternalServerError)
		return
	}
	s.grantsMu.Unlock()

	host := s.pullHost(r)
	image := fmt.Sprintf("%s/t/%s/%s:%s", host, g.Token, repo, tag)
	dockerCmd := fmt.Sprintf("docker pull %s", image)
	if s.cfg.VMToken != "" {
		dockerCmd = fmt.Sprintf("docker login %s -u x -p %s && %s", host, s.cfg.VMToken, dockerCmd)
	}
	resp := grantResp{
		Repo:      repo,
		Variant:   variant,
		Tag:       tag,
		Image:     image,
		Token:     g.Token,
		Expires:   g.Expires.UTC().Format(time.RFC3339),
		TTL:       ttl,
		ExeCmd:    fmt.Sprintf("ssh exe.dev new --image=%s", image),
		DockerCmd: dockerCmd,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	log.Printf("granted token=%s… %s[%s]:%s ttl=%dm", g.Token[:6], repo, variant, tag, ttl)
}

func (s *server) handleV2(w http.ResponseWriter, r *http.Request) {
	// Registry ping: answer unauthenticated so clients proceed to the
	// token-scoped path below. It leaks nothing.
	if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
		return
	}
	deny := func(code int) {
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		http.Error(w, `{"errors":[{"code":"DENIED","message":"expired or unknown grant"}]}`, code)
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		deny(405)
		return
	}
	// Path form: /v2/t/<token>/<repo>/<rest>
	rest := strings.TrimPrefix(r.URL.Path, "/v2/")
	if !strings.HasPrefix(rest, "t/") {
		deny(404)
		return
	}
	rest = rest[2:]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		deny(404)
		return
	}
	token, tail := rest[:slash], rest[slash+1:]

	s.grantsMu.Lock()
	s.gcLocked()
	g := s.byToken[token]
	ok := g != nil && g.Expires.After(time.Now()) &&
		(tail == g.Repo || strings.HasPrefix(tail, g.Repo+"/"))
	if ok {
		g.Uses++
	}
	s.grantsMu.Unlock()
	if !ok {
		deny(404)
		return
	}
	// Rewrite to the shared backing repo so blobs are stored once.
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/v2/" + tail
	r2.Header.Del("Authorization")
	s.registry.ServeHTTP(w, r2)
}

// bakedAt reports when the baked dev image was last built, for the UI.
func (s *server) bakedAt() string {
	img := s.cfg.DevImage
	if img == "" {
		return ""
	}
	for _, label := range []string{"dev.exe.image-forge.baked", "dev.huny.baked"} {
		out, err := exec.Command("docker", "image", "inspect", img,
			"--format", fmt.Sprintf(`{{index .Config.Labels %q}}`, label)).Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}

func inlineSourceHashes(page []byte, tag string) []string {
	source := string(page)
	lower := strings.ToLower(source)
	openPrefix := "<" + tag
	closeTag := "</" + tag + ">"
	var hashes []string
	for offset := 0; offset < len(source); {
		startRel := strings.Index(lower[offset:], openPrefix)
		if startRel < 0 {
			break
		}
		start := offset + startRel
		openEndRel := strings.IndexByte(source[start:], '>')
		if openEndRel < 0 {
			break
		}
		openEnd := start + openEndRel
		closeRel := strings.Index(lower[openEnd+1:], closeTag)
		if closeRel < 0 {
			break
		}
		closeStart := openEnd + 1 + closeRel
		openTag := lower[start : openEnd+1]
		if tag != "script" || !strings.Contains(openTag, "src=") {
			sum := sha256.Sum256([]byte(source[openEnd+1 : closeStart]))
			hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
		}
		offset = closeStart + len(closeTag)
	}
	return hashes
}

func pageCSP(page []byte) string {
	scripts := append([]string{"'self'"}, inlineSourceHashes(page, "script")...)
	styles := append([]string{"'self'"}, inlineSourceHashes(page, "style")...)
	return strings.Join([]string{
		"default-src 'none'",
		"script-src " + strings.Join(scripts, " "),
		"style-src-elem " + strings.Join(styles, " "),
		"style-src-attr 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"object-src 'none'",
	}, "; ")
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8000", "listen address")
	cfgPath := flag.String("config", "/etc/exe-image-forge/config.json", "config file")
	statePath := flag.String("state", "/var/lib/exe-image-forge/grants.json", "grant state file")
	registryURL := flag.String("registry", "http://127.0.0.1:5000", "backing registry")
	setPassword := flag.Bool("set-password", false, "read a new master password from stdin and exit")
	flag.Parse()

	b, err := os.ReadFile(*cfgPath)
	var cfg Config
	if err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			log.Fatalf("config: %v", err)
		}
	} else if !*setPassword {
		log.Fatalf("config: %v", err)
	}
	if *setPassword {
		var pw string
		fmt.Scanln(&pw)
		if len(pw) < 8 {
			log.Fatal("password must be >= 8 chars")
		}
		cfg.Salt = randHex(16)
		cfg.Hash = hashPassword(pw, cfg.Salt)
		if cfg.TTLMinutes == 0 {
			cfg.TTLMinutes = 30
		}
		out, _ := json.MarshalIndent(cfg, "", "  ")
		os.MkdirAll(filepath.Dir(*cfgPath), 0o755)
		if err := os.WriteFile(*cfgPath, out, 0o600); err != nil {
			log.Fatal(err)
		}
		fmt.Println("password updated")
		return
	}
	if cfg.TTLMinutes == 0 {
		cfg.TTLMinutes = 30
	}
	if len(cfg.Repos) == 0 {
		log.Fatal("config: repos empty")
	}
	if cfg.AuthHome == "" {
		cfg.AuthHome = "/var/lib/exe-image-forge/authhome"
	}
	if cfg.DevImage == "" {
		for _, repo := range cfg.Repos {
			if strings.HasSuffix(repo, "/dev") || repo == "dev" {
				cfg.DevImage = cfg.SourceImage[repo]
				break
			}
		}
		if cfg.DevImage == "" {
			cfg.DevImage = "exe-image-forge/dev:latest"
		}
	}
	if cfg.PasskeyFile == "" {
		cfg.PasskeyFile = filepath.Join(filepath.Dir(cfg.AuthHome), "passkeys.json")
	}

	ru, err := url.Parse(*registryURL)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{
		cfg:              cfg,
		cfgPath:          *cfgPath,
		statePath:        *statePath,
		registry:         httputil.NewSingleHostReverseProxy(ru),
		registryURL:      ru,
		registryLockPath: os.Getenv("FORGE_REGISTRY_LOCK"),
		orphanGrace:      defaultOrphanGrace,
		byToken:          map[string]*Grant{},
	}
	if s.registryLockPath == "" {
		s.registryLockPath = filepath.Join(filepath.Dir(*statePath), "registry.lock")
	}
	if raw := os.Getenv("FORGE_ORPHAN_GRACE"); raw != "" {
		if value, err := time.ParseDuration(raw); err == nil && value >= time.Hour {
			s.orphanGrace = value
		} else {
			log.Fatalf("FORGE_ORPHAN_GRACE must be a duration of at least 1h")
		}
	}
	os.MkdirAll(filepath.Dir(*statePath), 0o700)
	if err := s.loadState(); err != nil {
		log.Fatalf("grant state: %v", err)
	}

	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.grantsMu.Lock()
			n := len(s.byToken)
			s.gcLocked()
			if n != len(s.byToken) {
				if err := s.saveStateLocked(); err != nil {
					log.Printf("save expired grant state: %v", err)
				}
			}
			s.grantsMu.Unlock()
		}
	}()
	go func() {
		// A grace period protects a push that completed immediately before a
		// crash. Reconcile once on startup and periodically thereafter.
		if n, err := s.reconcileOrphans(context.Background(), false); err != nil {
			log.Printf("orphan reconciliation: %v", err)
		} else if n > 0 {
			log.Printf("orphan reconciliation removed %d tag(s)", n)
		}
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if n, err := s.reconcileOrphans(context.Background(), false); err != nil {
				log.Printf("orphan reconciliation: %v", err)
			} else if n > 0 {
				log.Printf("orphan reconciliation removed %d tag(s)", n)
			}
		}
	}()

	mux := http.NewServeMux()
	a := &admin{
		srv:      s,
		sessions: map[string]*session{},
		pk:       loadPasskeyStore(cfg.PasskeyFile),
	}
	s.sessionAuthed = a.authed
	mux.HandleFunc("/v2/", s.handleV2)
	mux.HandleFunc("/api/grant", s.handleGrant)
	mux.HandleFunc("/api/creds", a.handleCreds)
	mux.HandleFunc("/api/session", a.handleSession)
	mux.HandleFunc("/admin/api/login", a.handleLogin)
	mux.HandleFunc("/admin/api/logout", a.handleLogout)
	// Passkey login needs no prior session; everything else does.
	mux.HandleFunc("/admin/api/passkey/login/begin", a.handlePasskeyLoginBegin)
	mux.HandleFunc("/admin/api/passkey/login/finish", a.handlePasskeyLoginFinish)
	mux.HandleFunc("/admin/api/passkey/register/begin", a.require(a.handlePasskeyRegisterBegin))
	mux.HandleFunc("/admin/api/passkey/register/finish", a.require(a.handlePasskeyRegisterFinish))
	mux.HandleFunc("/admin/api/context", a.require(a.handleContext))
	mux.HandleFunc("/admin/api/passkey/list", a.require(a.handlePasskeyList))
	mux.HandleFunc("/admin/api/passkey/delete", a.require(a.handlePasskeyDelete))
	mux.HandleFunc("/admin/api/bake", a.require(a.handleBake))
	mux.HandleFunc("/admin/api/bake-status", a.require(a.handleBakeStatus))
	mux.HandleFunc("/admin/api/term", a.require(a.handleTerm))
	mux.HandleFunc("/admin/api/relay", a.require(a.handleRelay))
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", pageCSP(adminHTML))
		w.Write(adminHTML)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/passkey.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(passkeyJS)
	})
	mux.HandleFunc("/assets/xterm.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(xtermJS)
	})
	mux.HandleFunc("/assets/addon-fit.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(addonFitJS)
	})
	mux.HandleFunc("/assets/xterm.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(xtermCSS)
	})
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(faviconSVG)
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/favicon.svg", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		// Computed before taking the lock: it shells out to docker, and holding
		// grantsMu across that would stall every concurrent grant.
		vars := s.variants()
		s.grantsMu.Lock()
		defer s.grantsMu.Unlock()
		s.gcLocked()
		out := map[string]any{
			"repos": s.repoInfo(), "pull_host": s.cfg.PullHost,
			"ttl_minutes": s.cfg.TTLMinutes, "active_count": len(s.byToken),
			"variants": vars, "variant_names": variantNames,
		}
		// Only a local caller (the exe-image-forge CLI) sees grant detail; the
		// proxy always sets X-Forwarded-For for remote requests.
		if r.Header.Get("X-Forwarded-For") == "" && strings.HasPrefix(r.RemoteAddr, "127.0.0.1") {
			type act struct{ Repo, Tag, Expires string }
			var acts []act
			for _, g := range s.byToken {
				acts = append(acts, act{g.Repo, g.Tag, g.Expires.UTC().Format(time.RFC3339)})
			}
			out["active"] = acts
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", pageCSP(indexHTML))
		w.Write(indexHTML)
	})

	log.Printf("vend listening on %s (pull host %s, repos %v)", *addr, cfg.PullHost, cfg.Repos)
	srv := &http.Server{
		Addr: *addr, Handler: withSecurityHeaders(mux),
		ReadHeaderTimeout: 20 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// repoInfo describes each vendable repo. The names alone do not say which one
// carries the pre-authenticated credentials, and picking wrong yields an image
// where every CLI is logged out -- the exact thing this project exists to
// avoid. So say it plainly in the UI.
func (s *server) repoInfo() []map[string]any {
	out := make([]map[string]any, 0, len(s.cfg.Repos))
	for _, r := range s.cfg.Repos {
		baked := strings.HasSuffix(r, "/dev")
		label, note := r+" (no credentials)", "Tools only; sign in manually"
		if baked {
			label, note = r+" (signed in)", "Includes Codex, Claude, Gemini, and GitHub credentials"
		}
		out = append(out, map[string]any{
			"name": r, "label": label, "note": note, "baked": baked,
		})
	}
	return out
}

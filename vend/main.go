// vend serves time-limited pull credentials for a private Docker registry.
package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const pbkdfIters = 210000

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
}

type Grant struct {
	Token   string    `json:"token"`
	Repo    string    `json:"repo"`
	Tag     string    `json:"tag"`
	Expires time.Time `json:"expires"`
	Created time.Time `json:"created"`
	Uses    int       `json:"uses"`
}

type server struct {
	cfg       Config
	cfgPath   string
	statePath string
	registry  *httputil.ReverseProxy

	mu      sync.Mutex
	byToken map[string]*Grant
	fails   int
	lockout time.Time
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashPassword(pw, saltHex string) string {
	salt, _ := hex.DecodeString(saltHex)
	k, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIters, 32)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(k)
}

func (s *server) loadState() {
	b, err := os.ReadFile(s.statePath)
	if err != nil {
		return
	}
	var gs []*Grant
	if json.Unmarshal(b, &gs) != nil {
		return
	}
	now := time.Now()
	for _, g := range gs {
		if g.Expires.After(now) && g.Token != "" {
			s.byToken[g.Token] = g
		}
	}
}

func (s *server) saveStateLocked() {
	var gs []*Grant
	for _, g := range s.byToken {
		gs = append(gs, g)
	}
	b, _ := json.MarshalIndent(gs, "", "  ")
	tmp := s.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		os.Rename(tmp, s.statePath)
	}
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
				go s.deleteTag(repo, tag)
			}
		}
	}
}

func (s *server) deleteTag(repo, tag string) {
	dig, digErr := s.manifestDigest(repo, tag)
	ref := fmt.Sprintf("127.0.0.1:5000/%s:%s", repo, tag)
	if out, err := exec.Command("docker", "rmi", "-f", ref).CombinedOutput(); err != nil {
		log.Printf("rmi %s: %v: %s", ref, err, strings.TrimSpace(string(out)))
	}
	if digErr != nil {
		log.Printf("expire %s:%s: digest lookup failed: %v", repo, tag, digErr)
		return
	}
	req, _ := http.NewRequest("DELETE", "http://127.0.0.1:5000/v2/"+repo+"/manifests/"+dig, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("expire %s:%s: %v", repo, tag, err)
		return
	}
	resp.Body.Close()
	log.Printf("expired %s:%s (registry delete %s)", repo, tag, resp.Status)
}

func (s *server) manifestDigest(repo, tag string) (string, error) {
	req, _ := http.NewRequest("HEAD", "http://127.0.0.1:5000/v2/"+repo+"/manifests/"+tag, nil)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", errors.New("no digest")
	}
	return d, nil
}

// publish creates a per-grant image (a metadata-only layer on top of the
// source image, so the digest is unique to this tag) and pushes it. A unique
// digest matters: registry deletion works on digests, so tags sharing a digest
// could not be expired independently.
func (s *server) publish(repo, tag string) error {
	src := s.cfg.SourceImage[repo]
	if src == "" {
		return fmt.Errorf("no source image configured for %q", repo)
	}
	if out, err := exec.Command("docker", "image", "inspect", src).CombinedOutput(); err != nil {
		return fmt.Errorf("source image %s missing: %s", src, out)
	}
	dir, err := os.MkdirTemp("", "vend-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	df := fmt.Sprintf("FROM %s\nLABEL dev.huny.grant=%s\n", src, tag)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		return err
	}
	dst := fmt.Sprintf("127.0.0.1:5000/%s:%s", repo, tag)
	cmd := exec.Command("docker", "build", "--provenance=false", "--sbom=false", "-t", dst, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build: %v: %s", err, out)
	}
	if out, err := exec.Command("docker", "push", dst).CombinedOutput(); err != nil {
		return fmt.Errorf("docker push: %v: %s", err, out)
	}
	return nil
}

// pullHost returns the registry hostname the client should use: the host it
// reached us on (so both images.huny.dev and *.exe.xyz work), falling back to
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
}

type grantResp struct {
	Repo      string `json:"repo"`
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
	s.mu.Lock()
	if time.Now().Before(s.lockout) {
		wait := int(time.Until(s.lockout).Seconds()) + 1
		s.mu.Unlock()
		w.Header().Set("Retry-After", fmt.Sprint(wait))
		http.Error(w, fmt.Sprintf("too many attempts, retry in %ds", wait), 429)
		return
	}
	ok := subtle.ConstantTimeCompare([]byte(hashPassword(req.Password, s.cfg.Salt)), []byte(s.cfg.Hash)) == 1
	if !ok {
		s.fails++
		if s.fails >= 5 {
			s.lockout = time.Now().Add(time.Duration(s.fails-4) * time.Minute)
		}
		s.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
		http.Error(w, "wrong password", 403)
		return
	}
	s.fails = 0
	s.gcLocked()
	s.mu.Unlock()

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

	tag := randHex(8)
	if err := s.publish(repo, tag); err != nil {
		log.Printf("publish: %v", err)
		http.Error(w, "publish failed: "+err.Error(), 500)
		return
	}
	g := &Grant{
		Token:   randHex(16),
		Repo:    repo,
		Tag:     tag,
		Created: time.Now(),
		Expires: time.Now().Add(time.Duration(ttl) * time.Minute),
	}
	s.mu.Lock()
	s.byToken[g.Token] = g
	s.saveStateLocked()
	s.mu.Unlock()

	host := s.pullHost(r)
	image := fmt.Sprintf("%s/t/%s/%s:%s", host, g.Token, repo, tag)
	dockerCmd := fmt.Sprintf("docker pull %s", image)
	if s.cfg.VMToken != "" {
		dockerCmd = fmt.Sprintf("docker login %s -u x -p %s && %s", host, s.cfg.VMToken, dockerCmd)
	}
	resp := grantResp{
		Repo:      repo,
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
	log.Printf("granted token=%s… %s:%s ttl=%dm", g.Token[:6], repo, tag, ttl)
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

	s.mu.Lock()
	s.gcLocked()
	g := s.byToken[token]
	ok := g != nil && g.Expires.After(time.Now()) &&
		(tail == g.Repo || strings.HasPrefix(tail, g.Repo+"/"))
	if ok {
		g.Uses++
	}
	s.mu.Unlock()
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
	out, err := exec.Command("docker", "image", "inspect", img,
		"--format", `{{index .Config.Labels "dev.huny.baked"}}`).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8000", "listen address")
	cfgPath := flag.String("config", "/etc/hunyimg/config.json", "config file")
	statePath := flag.String("state", "/var/lib/hunyimg/grants.json", "grant state file")
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
		cfg.AuthHome = "/var/lib/hunyimg/authhome"
	}
	if cfg.DevImage == "" {
		cfg.DevImage = "hunydev/dev:latest"
	}

	ru, err := url.Parse(*registryURL)
	if err != nil {
		log.Fatal(err)
	}
	s := &server{
		cfg:       cfg,
		cfgPath:   *cfgPath,
		statePath: *statePath,
		registry:  httputil.NewSingleHostReverseProxy(ru),
		byToken:   map[string]*Grant{},
	}
	os.MkdirAll(filepath.Dir(*statePath), 0o700)
	s.loadState()

	go func() {
		for range time.Tick(time.Minute) {
			s.mu.Lock()
			n := len(s.byToken)
			s.gcLocked()
			if n != len(s.byToken) {
				s.saveStateLocked()
			}
			s.mu.Unlock()
		}
	}()

	mux := http.NewServeMux()
	a := &admin{srv: s, sessions: map[string]*session{}}
	mux.HandleFunc("/v2/", s.handleV2)
	mux.HandleFunc("/api/grant", s.handleGrant)
	mux.HandleFunc("/api/creds", a.handleCreds)
	mux.HandleFunc("/admin/api/login", a.handleLogin)
	mux.HandleFunc("/admin/api/logout", a.handleLogout)
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
		w.Write(adminHTML)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(faviconSVG)
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/favicon.svg", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.gcLocked()
		out := map[string]any{
			"repos": s.cfg.Repos, "pull_host": s.cfg.PullHost,
			"ttl_minutes": s.cfg.TTLMinutes, "active_count": len(s.byToken),
		}
		// Only a local caller (the hunyimg CLI) sees grant detail; the
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
		w.Write(indexHTML)
	})

	log.Printf("vend listening on %s (pull host %s, repos %v)", *addr, cfg.PullHost, cfg.Repos)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 20 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

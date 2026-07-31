package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/go-webauthn/webauthn/webauthn"
)

const sessionCookie = "hunyimg_admin"

type session struct {
	expires time.Time
}

type admin struct {
	srv *server
	pk  *passkeyStore

	mu       sync.Mutex
	sessions map[string]*session
	termBusy bool
	// In-flight WebAuthn ceremonies. There is a single admin account and the
	// ceremonies are short-lived, so one slot each is enough.
	regSession   *webauthn.SessionData
	regRPID      string
	loginSession *webauthn.SessionData
	loginRPID    string

	// bake state, so the UI can show a running/last-result banner.
	bakeMu      sync.Mutex
	bakeRunning bool
	bakeLog     []string
	bakeErr     string
	bakeAt      time.Time
}

func (a *admin) newSession() string {
	tok := randHex(24)
	a.mu.Lock()
	defer a.mu.Unlock()
	for t, s := range a.sessions {
		if s.expires.Before(time.Now()) {
			delete(a.sessions, t)
		}
	}
	a.sessions[tok] = &session{expires: time.Now().Add(8 * time.Hour)}
	return tok
}

func (a *admin) authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[c.Value]
	return s != nil && s.expires.After(time.Now())
}

// require wraps a handler with session auth.
func (a *admin) require(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authed(r) {
			http.Error(w, "not authenticated", 401)
			return
		}
		h(w, r)
	}
}

func (a *admin) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	s := a.srv
	s.mu.Lock()
	if time.Now().Before(s.lockout) {
		wait := int(time.Until(s.lockout).Seconds()) + 1
		s.mu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		http.Error(w, "too many attempts, retry in "+strconv.Itoa(wait)+"s", 429)
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
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: a.newSession(), Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 8 * 3600,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (a *admin) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	w.Write([]byte(`{"ok":true}`))
}

// handleCreds reports credential state. Available unauthenticated in an
// aggregate-only form so the main page can warn before asking for a password.
func (a *admin) handleCreds(w http.ResponseWriter, r *http.Request) {
	creds := inspectCreds(a.srv.cfg.AuthHome)
	w.Header().Set("Content-Type", "application/json")
	if a.authed(r) {
		json.NewEncoder(w).Encode(map[string]any{
			"creds": creds, "warnings": summarize(creds), "authed": true,
			"baked": a.srv.bakedAt(), "authed_home": a.srv.cfg.AuthHome,
			"passkeys": a.passkeyCountFor(r), "passkeys_total": a.pk.count(),
			"versions": a.srv.toolVersions(),
		})
		return
	}
	// Unauthenticated: counts and severity only, no account names or files.
	counts := map[string]int{}
	for _, c := range creds {
		counts[c.State]++
	}
	json.NewEncoder(w).Encode(map[string]any{
		"warnings": summarize(creds), "counts": counts, "authed": false,
		"baked": a.srv.bakedAt(), "passkeys": a.passkeyCountFor(r),
	})
}

// passkeyCountFor reports how many passkeys are usable on the host the request
// arrived on, so the login page knows whether to offer the button.
func (a *admin) passkeyCountFor(r *http.Request) int {
	_, rpid, err := a.rpFor(r)
	if err != nil {
		return 0
	}
	return len(a.pk.forRP(rpid))
}

func (a *admin) handleBake(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	a.bakeMu.Lock()
	if a.bakeRunning {
		a.bakeMu.Unlock()
		http.Error(w, "bake already running", 409)
		return
	}
	a.bakeRunning = true
	a.bakeLog = nil
	a.bakeErr = ""
	a.bakeMu.Unlock()

	go func() {
		cmd := exec.Command("/usr/local/bin/hunyimg", "bake")
		cmd.Env = append(os.Environ(), "HOME=/home/exedev")
		out, err := cmd.CombinedOutput()
		a.bakeMu.Lock()
		defer a.bakeMu.Unlock()
		a.bakeRunning = false
		a.bakeAt = time.Now()
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			a.bakeLog = append(a.bakeLog, sc.Text())
		}
		if n := len(a.bakeLog); n > 400 {
			a.bakeLog = a.bakeLog[n-400:]
		}
		if err != nil {
			a.bakeErr = err.Error()
			log.Printf("bake failed: %v", err)
		} else {
			log.Printf("bake ok")
		}
	}()
	w.Write([]byte(`{"started":true}`))
}

func (a *admin) handleBakeStatus(w http.ResponseWriter, r *http.Request) {
	a.bakeMu.Lock()
	defer a.bakeMu.Unlock()
	at := ""
	if !a.bakeAt.IsZero() {
		at = a.bakeAt.UTC().Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"running": a.bakeRunning, "log": a.bakeLog, "error": a.bakeErr,
		"at": at, "baked": a.srv.bakedAt(),
	})
}

// handleTerm bridges a websocket to a PTY running a shell inside the base
// image, with the persistent auth home mounted. This is how OAuth logins get
// done from a browser.
func (a *admin) handleTerm(w http.ResponseWriter, r *http.Request) {
	// One terminal at a time. Concurrent shells would race on the same auth
	// home, and each holds a container.
	a.mu.Lock()
	busy := a.termBusy
	if !busy {
		a.termBusy = true
	}
	a.mu.Unlock()
	if busy {
		http.Error(w, "a terminal session is already open", 409)
		return
	}
	defer func() {
		a.mu.Lock()
		a.termBusy = false
		a.mu.Unlock()
	}()

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:     []string{"*"},
		CompressionMode:    websocket.CompressionDisabled,
		InsecureSkipVerify: false,
	})
	if err != nil {
		log.Printf("ws accept: %v", err)
		return
	}
	defer c.CloseNow()

	cols, rows := 120, 32
	if v, err := strconv.Atoi(r.URL.Query().Get("cols")); err == nil && v > 20 && v < 500 {
		cols = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("rows")); err == nil && v > 5 && v < 200 {
		rows = v
	}

	name := "hunyimg-term-" + randHex(4)
	args := []string{
		"run", "--rm", "-i", "-t", "--name", name,
		"--network", "host", "--pull", "never",
		"-e", "HOME=/home/exedev", "-e", "TERM=xterm-256color",
		"-e", "LANG=en_US.UTF-8",
		"-u", "1000:1000",
		"-v", a.srv.cfg.AuthHome + ":/home/exedev",
		"-w", "/home/exedev",
		a.srv.termImage(),
		"bash", "-l",
	}
	cmd := exec.Command("docker", args...)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		c.Write(r.Context(), websocket.MessageText, []byte("pty start failed: "+err.Error()+"\r\n"))
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer func() {
		f.Close()
		// The container holds the pty; stop it so 'docker run --rm' cleans up.
		exec.Command("docker", "rm", "-f", name).Run()
		cmd.Wait()
	}()

	// PTY -> browser
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// browser -> PTY (text frames prefixed with \x00 are control messages)
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText && len(data) > 0 && data[0] == 0 {
			var ctl struct {
				Cols, Rows int
			}
			if json.Unmarshal(data[1:], &ctl) == nil && ctl.Cols > 0 && ctl.Rows > 0 {
				pty.Setsize(f, &pty.Winsize{Cols: uint16(ctl.Cols), Rows: uint16(ctl.Rows)})
			}
			continue
		}
		if _, err := f.Write(data); err != nil {
			return
		}
	}
}

// handleRelay replays an OAuth callback URL that the user's browser could not
// reach. Gemini CLI (unlike codex --device-auth and claude setup-token) has no
// device flow: it spawns a throwaway HTTP listener on a random localhost port
// and waits for Google to redirect there. That redirect lands on the user's
// laptop, not this VM, so it always fails. The user pastes the dead URL here
// and we issue the request from inside the VM, where the listener actually is.
func (a *admin) handleRelay(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil {
		http.Error(w, "URL을 해석할 수 없습니다", 400)
		return
	}
	// Only ever talk to a loopback callback listener. Without this the endpoint
	// would be an authenticated SSRF primitive into the VM's network.
	host := u.Hostname()
	if u.Scheme != "http" && u.Scheme != "https" {
		http.Error(w, "http(s) URL 만 허용됩니다", 400)
		return
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		http.Error(w, "localhost / 127.0.0.1 콜백 URL 만 허용됩니다", 400)
		return
	}
	port := u.Port()
	if port == "" {
		http.Error(w, "콜백 포트가 없습니다", 400)
		return
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1024 || p > 65535 {
		http.Error(w, "콜백 포트가 올바르지 않습니다", 400)
		return
	}
	u.Host = "127.0.0.1:" + port

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rq, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	resp, err := (&http.Client{}).Do(rq)
	if err != nil {
		http.Error(w, "콜백 서버에 연결할 수 없습니다: "+err.Error()+
			" — CLI 가 아직 대기 중인지 터미널을 확인하세요", 502)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	log.Printf("relayed oauth callback to %s -> %s", u.Host, resp.Status)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": resp.StatusCode, "body": string(body),
	})
}

// termImage picks the image backing the admin terminal. It must be the fullest
// variant: this shell exists to log the CLIs in, and the lean variants omit
// some of them, so using the default would make `gemini` a command-not-found.
func (s *server) termImage() string {
	for _, img := range []string{"hunydev/base:go-gemini", "hunydev/base:latest"} {
		if err := exec.Command("docker", "image", "inspect", img).Run(); err == nil {
			return img
		}
	}
	return s.cfg.SourceImage[s.cfg.Repos[0]]
}

// handleContext returns the machine-context block for a variant, so the admin
// can read exactly what the AI CLIs will be told about this environment.
func (a *admin) handleContext(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query().Get("variant")
	if v == "" {
		v = "min"
	}
	body := a.srv.agentContext(v)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"variant": v, "context": body})
}

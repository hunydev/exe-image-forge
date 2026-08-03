package main

import (
	"bufio"
	"bytes"
	"context"
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

const sessionCookie = "exe_image_forge_admin"

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
	ok, _ := a.sessionState(r)
	return ok
}

// sessionState is the lightweight source of truth used by both pages to notice
// logout and expiry without running the more expensive credential inspection.
func (a *admin) sessionState(r *http.Request) (bool, time.Time) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false, time.Time{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[c.Value]
	if s == nil {
		return false, time.Time{}
	}
	if !s.expires.After(time.Now()) {
		delete(a.sessions, c.Value)
		return false, time.Time{}
	}
	return true, s.expires
}

func (a *admin) handleSession(w http.ResponseWriter, r *http.Request) {
	ok, expires := a.sessionState(r)
	out := map[string]any{
		"authed":   ok,
		"passkeys": a.passkeyCountFor(r),
	}
	if ok {
		out["expires"] = expires.UTC().Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
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
	ok, retry := s.checkPassword(r, req.Password)
	if retry > 0 {
		wait := int(retry.Round(time.Second).Seconds())
		if wait < 1 {
			wait = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		http.Error(w, "too many attempts, retry in "+strconv.Itoa(wait)+"s", 429)
		return
	}
	if !ok {
		http.Error(w, "wrong password", 403)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: a.newSession(), Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 8 * 3600,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (a *admin) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	w.Write([]byte(`{"ok":true}`))
}

// handleCreds reports credential state. Available unauthenticated in an
// aggregate-only form so the main page can warn before sign-in.
func (a *admin) handleCreds(w http.ResponseWriter, r *http.Request) {
	var creds []Cred
	if a.srv.demo {
		creds = demoCredentials()
	} else {
		creds = inspectCreds(a.srv.cfg.AuthHome)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if a.authed(r) {
		json.NewEncoder(w).Encode(map[string]any{
			"creds": creds, "warnings": summarize(creds), "authed": true,
			"baked": a.srv.bakedAt(), "authed_home": a.srv.cfg.AuthHome,
			"passkeys": a.passkeyCountFor(r), "passkeys_total": a.pk.count(),
			"versions": a.srv.toolVersions(), "terminal_mode": a.srv.terminalMode,
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

	if a.srv.demo {
		go func() {
			time.Sleep(250 * time.Millisecond)
			a.bakeMu.Lock()
			a.bakeRunning = false
			a.bakeAt = time.Now()
			a.bakeLog = []string{
				"[demo] validated allowlisted credential fixtures",
				"[demo] simulated 16 credentialed image variants",
				"[demo] no Docker image was built or pushed",
			}
			a.bakeMu.Unlock()
		}()
		w.Write([]byte(`{"started":true,"demo":true}`))
		return
	}

	go func() {
		command := os.Getenv("FORGE_COMMAND_PATH")
		if command == "" {
			command = "/usr/local/bin/exe-image-forge"
		}
		cmd := exec.Command(command, "bake")
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

type hostAuthSpec struct {
	command string
	args    []string
	env     []string
}

// hostAuthCommand maps an opaque UI tool name to one exact login command. It
// deliberately does not accept a shell command from the browser.
func hostAuthCommand(tool string) (hostAuthSpec, bool) {
	switch tool {
	case "gh":
		return hostAuthSpec{command: "gh", args: []string{"auth", "login", "--git-protocol", "https"}}, true
	case "codex":
		return hostAuthSpec{command: "codex", args: []string{"login", "--device-auth"}}, true
	case "claude":
		return hostAuthSpec{command: "claude", args: []string{"auth", "login"}}, true
	case "gemini":
		return hostAuthSpec{command: "gemini", env: []string{"NO_BROWSER=true"}}, true
	case "wrangler":
		return hostAuthSpec{command: "wrangler", args: []string{"login", "--no-use-keyring"}}, true
	default:
		return hostAuthSpec{}, false
	}
}

func authTerminalEnv(home string, extra []string) []string {
	drop := []string{"HOME=", "XDG_CONFIG_HOME=", "TERM=", "LANG=", "USER=", "LOGNAME="}
	env := make([]string, 0, len(os.Environ())+len(extra)+5)
	for _, entry := range os.Environ() {
		keep := true
		for _, prefix := range drop {
			if strings.HasPrefix(entry, prefix) {
				keep = false
				break
			}
		}
		if keep {
			env = append(env, entry)
		}
	}
	env = append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+home+"/.config",
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"USER=exedev",
		"LOGNAME=exedev",
	)
	return append(env, extra...)
}

// handleTerm bridges a websocket to a PTY. Normal installations use a shell
// inside the full base image. A self-hosted appliance uses auth-host mode,
// which works before the base images exist and permits only fixed login CLIs.
func (a *admin) handleTerm(w http.ResponseWriter, r *http.Request) {
	if a.srv.demo {
		a.handleDemoTerm(w, r)
		return
	}
	var spec hostAuthSpec
	if a.srv.terminalMode == "auth-host" {
		var ok bool
		spec, ok = hostAuthCommand(r.URL.Query().Get("tool"))
		if !ok {
			http.Error(w, "unknown or missing authentication tool", http.StatusBadRequest)
			return
		}
	}
	// One terminal at a time. Concurrent login processes would race on the same
	// authentication home.
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

	containerName := ""
	cleanup := func() {}
	var cmd *exec.Cmd
	if a.srv.terminalMode == "auth-host" {
		cmd = exec.Command(spec.command, spec.args...)
		cmd.Dir = a.srv.cfg.AuthHome
		cmd.Env = authTerminalEnv(a.srv.cfg.AuthHome, spec.env)
	} else {
		containerName = "image-forge-term-" + randHex(4)
		args := []string{
			"run", "--rm", "-i", "-t", "--name", containerName,
			"--network", "host", "--pull", "never",
			"-e", "HOME=/home/exedev", "-e", "TERM=xterm-256color",
			"-e", "LANG=en_US.UTF-8",
			"-u", "1000:1000",
			"-v", a.srv.cfg.AuthHome + ":/home/exedev",
			"-w", "/home/exedev",
			a.srv.termImage(),
			"bash", "-l",
		}
		cmd = exec.Command("docker", args...)
		cleanup = func() {
			// The container holds the pty; stop it so --rm cleans up.
			exec.Command("docker", "rm", "-f", containerName).Run()
		}
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		c.Write(r.Context(), websocket.MessageText, []byte("pty start failed: "+err.Error()+"\r\n"))
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer func() {
		f.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		cleanup()
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
// reach. Wrangler uses localhost:8976, and older Gemini flows can use a random
// local port. Those redirects land on the user's laptop, not this VM. The user
// pastes the dead URL here and we issue the request from inside the VM, where
// the waiting CLI listener actually is.
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
		http.Error(w, "could not parse URL", 400)
		return
	}
	// Only ever talk to a loopback callback listener. Without this the endpoint
	// would be an authenticated SSRF primitive into the VM's network.
	host := u.Hostname()
	if u.Scheme != "http" && u.Scheme != "https" {
		http.Error(w, "only http(s) URLs are allowed", 400)
		return
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		http.Error(w, "only localhost or 127.0.0.1 callback URLs are allowed", 400)
		return
	}
	port := u.Port()
	if port == "" {
		http.Error(w, "callback URL has no port", 400)
		return
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1024 || p > 65535 {
		http.Error(w, "callback port is invalid", 400)
		return
	}
	u.Host = "127.0.0.1:" + port

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rq, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	resp, err := (&http.Client{}).Do(rq)
	if err != nil {
		http.Error(w, "could not reach callback server: "+err.Error()+
			" — check that the CLI is still waiting in the terminal", 502)
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
	base := imageRepo(s.baseImage())
	for _, img := range []string{base + ":go-gemini", s.baseImage()} {
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

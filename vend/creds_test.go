package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkJWT(exp time.Time) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "email": "me@example.com"})
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func write(t *testing.T, home, rel, body string) {
	t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func find(cs []Cred, tool string) Cred {
	for _, c := range cs {
		if c.Tool == tool {
			return c
		}
	}
	return Cred{}
}

func TestAllMissing(t *testing.T) {
	home := t.TempDir()
	for _, c := range inspectCreds(home) {
		if c.State != "missing" {
			t.Errorf("%s: got %q want missing", c.Tool, c.State)
		}
	}
	if w := summarize(inspectCreds(home)); len(w) != 1 {
		t.Errorf("warnings = %v, want one missing line", w)
	}
}

func TestGhLoggedIn(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".config/gh/hosts.yml", "github.com:\n    user: hunydev\n    oauth_token: gho_abc123\n    git_protocol: https\n")
	c := find(inspectCreds(home), "gh")
	if c.State != "ok" {
		t.Errorf("state = %q, want ok", c.State)
	}
	if c.Detail == "" {
		t.Error("want a detail with the account name")
	}
}

func TestGhGarbage(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".config/gh/hosts.yml", "github.com:\n    git_protocol: https\n")
	if c := find(inspectCreds(home), "gh"); c.State != "unknown" {
		t.Errorf("state = %q, want unknown", c.State)
	}
}

func TestCodexChatGPTFresh(t *testing.T) {
	home := t.TempDir()
	body, _ := json.Marshal(map[string]any{
		"tokens": map[string]string{
			"id_token":     mkJWT(time.Now().Add(20 * 24 * time.Hour)),
			"access_token": "at", "refresh_token": "rt", "account_id": "acc",
		},
	})
	write(t, home, ".codex/auth.json", string(body))
	c := find(inspectCreds(home), "codex")
	if c.State != "ok" {
		t.Errorf("state = %q, want ok", c.State)
	}
	if !c.Refreshable {
		t.Error("want refreshable")
	}
	if c.SecondsLeft < int64(19*24*3600) {
		t.Errorf("seconds_left = %d, too small", c.SecondsLeft)
	}
}

// An expired access token with a refresh token is not an emergency: the CLI
// renews it silently. It must be reported as stale, never expired.
func TestCodexExpiredButRefreshable(t *testing.T) {
	home := t.TempDir()
	body, _ := json.Marshal(map[string]any{
		"tokens": map[string]string{
			"id_token":      mkJWT(time.Now().Add(-2 * time.Hour)),
			"refresh_token": "rt",
		},
	})
	write(t, home, ".codex/auth.json", string(body))
	c := find(inspectCreds(home), "codex")
	if c.State != "stale" {
		t.Errorf("state = %q, want stale", c.State)
	}
	if c.SecondsLeft >= 0 {
		t.Errorf("seconds_left = %d, want negative", c.SecondsLeft)
	}
}

func TestCodexAPIKey(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".codex/auth.json", `{"OPENAI_API_KEY":"sk-test"}`)
	if c := find(inspectCreds(home), "codex"); c.State != "ok" {
		t.Errorf("state = %q, want ok", c.State)
	}
}

func TestClaudeFreshAndExpired(t *testing.T) {
	home := t.TempDir()
	mk := func(exp time.Time, refresh string) string {
		b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "at", "refreshToken": refresh,
			"expiresAt": exp.UnixMilli(), "subscriptionType": "max",
		}})
		return string(b)
	}
	write(t, home, ".claude/.credentials.json", mk(time.Now().Add(8*time.Hour), "rt"))
	if c := find(inspectCreds(home), "claude"); c.State != "ok" || c.Detail != "max" {
		t.Errorf("got %q/%q, want ok/max", c.State, c.Detail)
	}

	// No refresh token and past expiry: genuinely needs a re-login.
	write(t, home, ".claude/.credentials.json", mk(time.Now().Add(-time.Hour), ""))
	if c := find(inspectCreds(home), "claude"); c.State != "expired" {
		t.Errorf("state = %q, want expired", c.State)
	}
}

func TestGeminiExpiryAndAccount(t *testing.T) {
	home := t.TempDir()
	b, _ := json.Marshal(map[string]any{
		"access_token": "at", "refresh_token": "rt",
		"expiry_date": time.Now().Add(30 * time.Minute).UnixMilli(),
	})
	write(t, home, ".gemini/oauth_creds.json", string(b))
	write(t, home, ".gemini/google_accounts.json", `{"active":"me@gmail.com"}`)
	c := find(inspectCreds(home), "gemini")
	// Under 24h left but refreshable => ok, because refresh is automatic.
	if c.State != "ok" {
		t.Errorf("state = %q, want ok", c.State)
	}
	if c.Detail != "me@gmail.com" {
		t.Errorf("detail = %q", c.Detail)
	}
}

func TestSummarizeSeparatesSeverity(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".config/gh/hosts.yml", "github.com:\n    oauth_token: gho_x\n")
	b, _ := json.Marshal(map[string]any{"tokens": map[string]string{
		"id_token": mkJWT(time.Now().Add(-time.Hour)), "refresh_token": "rt"}})
	write(t, home, ".codex/auth.json", string(b))
	cb, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "at", "expiresAt": time.Now().Add(-time.Hour).UnixMilli()}})
	write(t, home, ".claude/.credentials.json", string(cb))

	w := summarize(inspectCreds(home))
	if len(w) != 3 {
		t.Fatalf("warnings = %v, want missing+expired+stale lines", w)
	}
}

// The relay endpoint is an authenticated fetch primitive, so its host
// restriction is the only thing standing between a stolen session and the VM's
// internal network. Cover the rejection cases explicitly.
func TestRelayRejectsNonLoopback(t *testing.T) {
	a := &admin{srv: &server{cfg: Config{Salt: "00", Hash: "x"}}, sessions: map[string]*session{}}
	tok := a.newSession()

	for _, tc := range []struct {
		name, url string
		wantCode  int
	}{
		{"external host", "http://evil.example.com:8080/x", 400},
		{"metadata service", "http://169.254.169.254/latest/meta-data/", 400},
		{"private range", "http://10.0.0.5:8000/v2/", 400},
		{"registry via name", "http://registry:5000/v2/_catalog", 400},
		{"file scheme", "file:///etc/hunyimg/config.json", 400},
		{"no port", "http://localhost/oauth2callback", 400},
		{"privileged port", "http://127.0.0.1:22/", 400},
		{"loopback ok but nothing listening", "http://127.0.0.1:59999/oauth2callback?code=x", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"url": tc.url})
			r := httptest.NewRequest("POST", "/admin/api/relay", bytes.NewReader(body))
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
			w := httptest.NewRecorder()
			a.require(a.handleRelay)(w, r)
			if w.Code != tc.wantCode {
				t.Errorf("%s: got %d want %d (%s)", tc.url, w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}

func TestRelayNeedsSession(t *testing.T) {
	a := &admin{srv: &server{}, sessions: map[string]*session{}}
	body, _ := json.Marshal(map[string]string{"url": "http://127.0.0.1:59999/x"})
	r := httptest.NewRequest("POST", "/admin/api/relay", bytes.NewReader(body))
	w := httptest.NewRecorder()
	a.require(a.handleRelay)(w, r)
	if w.Code != 401 {
		t.Errorf("got %d, want 401", w.Code)
	}
}

// A real loopback listener must be reached, proving the relay actually works
// for the gemini flow it exists to serve.
func TestRelayReachesLoopbackListener(t *testing.T) {
	got := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.URL.String()
		w.Write([]byte("authenticated"))
	}))
	defer ts.Close()

	a := &admin{srv: &server{}, sessions: map[string]*session{}}
	tok := a.newSession()
	// ts.URL is 127.0.0.1:<port>; keep the path/query the CLI would produce.
	body, _ := json.Marshal(map[string]string{"url": ts.URL + "/oauth2callback?code=abc&state=xyz"})
	r := httptest.NewRequest("POST", "/admin/api/relay", bytes.NewReader(body))
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	w := httptest.NewRecorder()
	a.require(a.handleRelay)(w, r)

	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	select {
	case u := <-got:
		if u != "/oauth2callback?code=abc&state=xyz" {
			t.Errorf("listener saw %q, query must be preserved verbatim", u)
		}
	default:
		t.Fatal("listener was never reached")
	}
}

// Login commands must never fall back to a localhost callback.
func TestLoginCommandsAreHeadless(t *testing.T) {
	creds := inspectCreds(t.TempDir())
	want := map[string]string{
		"gh":     "gh auth login --git-protocol https",
		"codex":  "codex login --device-auth",
		"claude": "claude setup-token",
		"gemini": "gemini",
	}
	for _, c := range creds {
		if got := want[c.Tool]; got != c.LoginCmd {
			t.Errorf("%s: login_cmd = %q, want %q", c.Tool, c.LoginCmd, got)
		}
	}
	// Only gemini lacks a device flow, so only it should ask for the relay.
	for _, c := range creds {
		if (c.Tool == "gemini") != c.NeedsRelay {
			t.Errorf("%s: needs_relay = %v", c.Tool, c.NeedsRelay)
		}
	}
}

// The updater and the image must not drift apart: if a CLI is installed but
// the updater does not know how to update it, it silently rots forever. This
// checks the two halves stay in sync.
func TestUpdaterCoversEveryTrackedCLI(t *testing.T) {
	script, err := os.ReadFile("../image/files/update-ai-clis")
	if err != nil {
		t.Skipf("update script not available: %v", err)
	}
	src := string(script)
	for _, tool := range []string{"codex", "claude", "gemini", "gh"} {
		if !strings.Contains(src, "update_"+tool+"()") {
			t.Errorf("update-ai-clis has no update_%s(); %s would never be updated", tool, tool)
		}
	}
	// The default tool list must actually run them.
	if !strings.Contains(src, `TOOLS:-codex claude gemini gh`) {
		t.Error("default TOOLS list does not cover all four CLIs")
	}
	// Every tool the credential inspector reports on should be updatable.
	for _, c := range inspectCreds(t.TempDir()) {
		if !strings.Contains(src, "update_"+c.Tool+"()") {
			t.Errorf("credential tool %q has no updater", c.Tool)
		}
	}
}

// A failure updating one CLI must not abort the others, and must not leave the
// script exiting non-zero silently in the middle.
func TestUpdaterIsFaultTolerant(t *testing.T) {
	script, err := os.ReadFile("../image/files/update-ai-clis")
	if err != nil {
		t.Skipf("update script not available: %v", err)
	}
	src := string(script)
	if strings.Contains(src, "set -euo pipefail") {
		t.Error("script uses `set -e`: one failing CLI would abort the rest")
	}
	if !strings.Contains(src, "|| rc=1") {
		t.Error("per-tool failures are not collected into the exit status")
	}
	// systemd must not treat a partial update as a boot failure.
	unit, err := os.ReadFile("../image/files/ai-cli-update.service")
	if err != nil {
		t.Skipf("unit not available: %v", err)
	}
	if !strings.Contains(string(unit), "SuccessExitStatus=0 1") {
		t.Error("ai-cli-update.service will report failure when a single CLI update fails")
	}
	timer, err := os.ReadFile("../image/files/ai-cli-update.timer")
	if err != nil {
		t.Fatal(err)
	}
	// Without a boot trigger, a VM from an old image stays stale until the
	// next daily tick, which may never come for short-lived VMs.
	if !strings.Contains(string(timer), "OnBootSec=") {
		t.Error("timer has no OnBootSec; freshly created VMs would not catch up")
	}
	if !strings.Contains(string(timer), "RandomizedDelaySec=") {
		t.Error("timer has no jitter; many VMs would stampede the release servers")
	}
}

// The variant mapping is the contract between the web form, the image tags and
// the Dockerfile build args; a mismatch means the wrong image gets vended.
func TestVariantFor(t *testing.T) {
	for _, tc := range []struct {
		withGo, withGemini bool
		want               string
	}{
		{false, false, "min"},
		{false, true, "gemini"},
		{true, false, "go"},
		{true, true, "go-gemini"},
	} {
		if got := variantFor(tc.withGo, tc.withGemini); got != tc.want {
			t.Errorf("variantFor(go=%v,gemini=%v) = %q, want %q",
				tc.withGo, tc.withGemini, got, tc.want)
		}
	}

	// The smallest image must be what you get when you ask for nothing, since
	// an empty JSON body decodes to all-false.
	var req grantReq
	if err := json.Unmarshal([]byte(`{"repo":"hunydev/dev"}`), &req); err != nil {
		t.Fatal(err)
	}
	if v := variantFor(req.WithGo, req.WithGemini); v != "min" {
		t.Errorf("a request that omits the toggles yields %q, want the minimal image", v)
	}

	// Every variant the server can produce must be one the build script knows
	// how to build, and vice versa.
	script, err := os.ReadFile("../hunyimg")
	if err != nil {
		t.Skipf("hunyimg not readable: %v", err)
	}
	for _, name := range variantNames {
		if !strings.Contains(string(script), name+")") {
			t.Errorf("variant %q has no case in hunyimg's variant_args", name)
		}
	}
	for _, combo := range [][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
		v := variantFor(combo[0], combo[1])
		found := false
		for _, n := range variantNames {
			if n == v {
				found = true
			}
		}
		if !found {
			t.Errorf("variantFor produced %q, which is not in variantNames", v)
		}
	}
}

// Optional components must be the last layers in the Dockerfile. If they were
// earlier, each variant would get its own copy of the ~590MB codex+claude
// layer and the registry would balloon.
func TestOptionalComponentsAreLastLayers(t *testing.T) {
	b, err := os.ReadFile("../image/Dockerfile")
	if err != nil {
		t.Skipf("Dockerfile not readable: %v", err)
	}
	df := string(b)
	cli := strings.Index(df, "TOOLS=\"codex claude gh\"")
	if cli < 0 {
		t.Fatal("could not find the AI CLI install step")
	}
	for _, arg := range []string{"ARG WITH_GEMINI", "ARG WITH_GO"} {
		i := strings.Index(df, arg)
		if i < 0 {
			t.Errorf("%s missing from Dockerfile", arg)
			continue
		}
		if i < cli {
			t.Errorf("%s appears before the expensive CLI layer; every variant "+
				"would duplicate codex+claude instead of sharing them", arg)
		}
	}
	// Defaults in the Dockerfile must match the defaults the web form sends.
	if !strings.Contains(df, "ARG WITH_GO=0") {
		t.Error("WITH_GO should default to 0 to match the web form's default")
	}
	if !strings.Contains(df, "ARG WITH_GEMINI=0") {
		t.Error("WITH_GEMINI should default to 0 to match the web form's default")
	}
}

// An image built without gemini must not have it reinstated by the update
// timer, or the user's choice of a smaller image would silently expire.
func TestUpdaterDoesNotReinstateOmittedComponents(t *testing.T) {
	b, err := os.ReadFile("../image/files/update-ai-clis")
	if err != nil {
		t.Skipf("update script not readable: %v", err)
	}
	if !strings.Contains(string(b), "INSTALL_MISSING") {
		t.Error("update_gemini has no guard against installing into an image " +
			"that deliberately excluded it")
	}
}

// The machine-context files are what make an agent aware of this VM. Getting
// the paths wrong means the file is written but never read, which is silent.
func TestAgentContextTargetsTheDocumentedGlobalPaths(t *testing.T) {
	b, err := os.ReadFile("../image/files/write-agent-context")
	if err != nil {
		t.Skipf("script not readable: %v", err)
	}
	src := string(b)
	// These are each CLI's documented global instruction file.
	for tool, path := range map[string]string{
		"codex":  ".codex/AGENTS.md",
		"claude": ".claude/CLAUDE.md",
		"gemini": ".gemini/GEMINI.md",
	} {
		if !strings.Contains(src, path) {
			t.Errorf("%s: global instruction file %q is never written", tool, path)
		}
	}

	// Content must be delimited so a user's own notes survive regeneration.
	if !strings.Contains(src, "BEGIN hunydev machine context") ||
		!strings.Contains(src, "END hunydev machine context") {
		t.Error("generated block is not delimited by markers; regeneration would " +
			"destroy anything the user added to these files")
	}

	// Optional components must be reported conditionally. A static list would
	// claim go/gemini exist in the min variant.
	for _, guard := range []string{"have go ", "have gemini "} {
		if !strings.Contains(src, guard) {
			t.Errorf("no presence check %q; the context would lie about lean variants", guard)
		}
	}
}

// The context must be regenerated on the VM, not frozen at build time: the
// facts it states belong to the running machine.
func TestAgentContextRegeneratesAtBoot(t *testing.T) {
	unit, err := os.ReadFile("../image/files/agent-context.service")
	if err != nil {
		t.Skipf("unit not readable: %v", err)
	}
	u := string(unit)
	if !strings.Contains(u, "WantedBy=multi-user.target") {
		t.Error("service is not wired into boot")
	}
	// ConditionPathExists / ConditionFirstBoot would make it run only once,
	// leaving later boots describing the build host.
	if strings.Contains(u, "ConditionFirstBoot") {
		t.Error("service is limited to first boot; later boots would keep stale facts")
	}
	if !strings.Contains(u, "SuccessExitStatus=0 1") {
		t.Error("a failure writing a documentation file should not degrade boot")
	}

	df, err := os.ReadFile("../image/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(df), "systemctl enable agent-context.service") {
		t.Error("agent-context.service is never enabled in the image")
	}
	// The variant name must be recorded for the context to report it.
	if !strings.Contains(string(df), "/etc/hunydev-variant") {
		t.Error("variant is not recorded, so the context cannot name it")
	}
}

// Baking credentials must not overlay the per-variant context files, or every
// variant would inherit whichever one seeded the auth home.
func TestBakeDoesNotClobberAgentContext(t *testing.T) {
	b, err := os.ReadFile("../hunyimg")
	if err != nil {
		t.Skipf("hunyimg not readable: %v", err)
	}
	src := string(b)
	for _, f := range []string{".codex/AGENTS.md", ".claude/CLAUDE.md", ".gemini/GEMINI.md"} {
		if !strings.Contains(src, "--exclude="+f) {
			t.Errorf("bake does not exclude %s; the baked image would carry a "+
				"context file describing a different variant", f)
		}
	}
}

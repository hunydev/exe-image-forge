package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	write(t, home, ".config/gh/hosts.yml", "github.com:\n    user: exe-image-forge\n    oauth_token: gho_abc123\n    git_protocol: https\n")
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
		{"file scheme", "file:///etc/exe-image-forge/config.json", 400},
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
		"claude": "claude auth login",
		"gemini": "NO_BROWSER=true gemini",
	}
	for _, c := range creds {
		if got := want[c.Tool]; got != c.LoginCmd {
			t.Errorf("%s: login_cmd = %q, want %q", c.Tool, c.LoginCmd, got)
		}
	}
	// Every CLI now has a paste-the-code flow, so nothing should need the
	// localhost callback relay.
	for _, c := range creds {
		if c.NeedsRelay {
			t.Errorf("%s: needs_relay is set, but all login commands avoid "+
				"localhost callbacks now", c.Tool)
		}
	}

	// `claude setup-token` only prints a token to export; it writes nothing to
	// disk, so a user who ran it saw "success" while the UI kept reporting the
	// credential as missing. The login command must be one that persists.
	for _, c := range creds {
		if c.Tool == "claude" && strings.Contains(c.LoginCmd, "setup-token") {
			t.Error("claude login_cmd is setup-token, which does not persist " +
				"credentials; the UI would never see the login")
		}
	}
}

// The admin terminal exists to log the CLIs in, so it must run an image that
// actually contains all of them. The default variant omits Go and Gemini.
func TestTerminalUsesTheFullestVariant(t *testing.T) {
	b, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "termImage()") {
		t.Fatal("terminal does not select its image explicitly")
	}
	if !strings.Contains(src, `base + ":go-gemini"`) ||
		!strings.Contains(src, "s.baseImage()") {
		t.Error("terminal image does not prefer the variant containing every " +
			"CLI; `gemini` would be command-not-found in the login shell")
	}
}

func TestImageRepoAndConfiguredBaseImage(t *testing.T) {
	for ref, want := range map[string]string{
		"team/base:latest":                   "team/base",
		"localhost:5000/team/base:go-gemini": "localhost:5000/team/base",
		"team/base":                          "team/base",
	} {
		if got := imageRepo(ref); got != want {
			t.Errorf("imageRepo(%q) = %q, want %q", ref, got, want)
		}
	}

	t.Setenv("FORGE_BASE_IMAGE", "")
	s := &server{cfg: Config{
		SourceImage: map[string]string{"acme/base": "acme/base:latest"},
		DevImage:    "acme/dev:latest",
	}}
	if got := s.baseImage(); got != "acme/base:latest" {
		t.Fatalf("baseImage() = %q, want custom prefix", got)
	}

	t.Setenv("FORGE_BASE_IMAGE", "registry.example:5000/custom/base:latest")
	if got := s.baseImage(); got != "registry.example:5000/custom/base:latest" {
		t.Fatalf("baseImage() ignored forge.env value: %q", got)
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
		codex, claude, goTool, gemini bool
		want                          string
	}{
		{false, false, false, false, "core"},
		{false, false, false, true, "core-gemini"},
		{false, false, true, false, "core-go"},
		{false, false, true, true, "core-go-gemini"},
		{true, false, false, false, "codex"},
		{true, false, false, true, "codex-gemini"},
		{true, false, true, false, "codex-go"},
		{true, false, true, true, "codex-go-gemini"},
		{false, true, false, false, "claude"},
		{false, true, false, true, "claude-gemini"},
		{false, true, true, false, "claude-go"},
		{false, true, true, true, "claude-go-gemini"},
		{true, true, false, false, "min"},
		{true, true, false, true, "gemini"},
		{true, true, true, false, "go"},
		{true, true, true, true, "go-gemini"},
	} {
		if got := variantFor(tc.codex, tc.claude, tc.goTool, tc.gemini); got != tc.want {
			t.Errorf("variantFor(codex=%v,claude=%v,go=%v,gemini=%v) = %q, want %q",
				tc.codex, tc.claude, tc.goTool, tc.gemini, got, tc.want)
		}
	}

	// A cached/older client omits Codex and Claude fields. Preserve the old
	// default rather than unexpectedly vending a tool-less core image.
	var req grantReq
	if err := json.Unmarshal([]byte(`{"repo":"exe-image-forge/dev"}`), &req); err != nil {
		t.Fatal(err)
	}
	if v := variantFor(defaultTrue(req.WithCodex), defaultTrue(req.WithClaude), req.WithGo, req.WithGemini); v != "min" {
		t.Errorf("a legacy request that omits new toggles yields %q, want min", v)
	}

	// Every variant the server can produce must be one the build script knows
	// how to build, and vice versa.
	script, err := os.ReadFile("../exe-image-forge")
	if err != nil {
		t.Skipf("exe-image-forge not readable: %v", err)
	}
	for _, name := range variantNames {
		if !strings.Contains(string(script), name) {
			t.Errorf("variant %q is absent from exe-image-forge", name)
		}
	}
	for mask := 0; mask < 16; mask++ {
		v := variantFor(mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0)
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

func TestWebPickerSendsOptionalAgentChoices(t *testing.T) {
	b, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, tool := range []string{"codex", "claude", "gemini", "go"} {
		if !strings.Contains(src, `id="c-`+tool+`"`) {
			t.Errorf("web picker has no %s checkbox", tool)
		}
		if !strings.Contains(src, "with_"+tool+":") {
			t.Errorf("grant request does not send with_%s", tool)
		}
	}
	for _, tool := range []string{"codex", "claude"} {
		if !strings.Contains(src, `id="c-`+tool+`" checked`) {
			t.Errorf("%s should remain selected by default for backward compatibility", tool)
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
	base := strings.Index(df, "TOOLS=gh")
	if base < 0 {
		t.Fatal("could not find the shared base CLI install step")
	}
	for _, arg := range []string{"ARG WITH_CODEX", "ARG WITH_CLAUDE", "ARG WITH_GEMINI", "ARG WITH_GO"} {
		i := strings.Index(df, arg)
		if i < 0 {
			t.Errorf("%s missing from Dockerfile", arg)
			continue
		}
		if i < base {
			t.Errorf("%s appears before the shared base layer", arg)
		}
	}
	// Defaults in the Dockerfile must match the defaults the web form sends.
	if !strings.Contains(df, "ARG WITH_CODEX=1") {
		t.Error("WITH_CODEX should default to 1 for backward compatibility")
	}
	if !strings.Contains(df, "ARG WITH_CLAUDE=1") {
		t.Error("WITH_CLAUDE should default to 1 for backward compatibility")
	}
	if !strings.Contains(df, "ARG WITH_GO=0") {
		t.Error("WITH_GO should default to 0 to match the web form's default")
	}
	if !strings.Contains(df, "ARG WITH_GEMINI=0") {
		t.Error("WITH_GEMINI should default to 0 to match the web form's default")
	}
}

// An image built without an optional CLI must not have it reinstated by the
// update timer, or the user's smaller-image choice would silently expire.
func TestUpdaterDoesNotReinstateOmittedComponents(t *testing.T) {
	b, err := os.ReadFile("../image/files/update-ai-clis")
	if err != nil {
		t.Skipf("update script not readable: %v", err)
	}
	src := string(b)
	for _, tool := range []string{"codex", "claude", "gemini"} {
		start := strings.Index(src, "update_"+tool+"(){")
		if start < 0 {
			t.Fatalf("update_%s not found", tool)
		}
		body := src[start:]
		if end := strings.Index(body, "\n}"); end >= 0 {
			body = body[:end]
		}
		if !strings.Contains(body, "INSTALL_MISSING") ||
			!strings.Contains(body, "command -v "+tool) {
			t.Errorf("update_%s can reinstate a deliberately omitted tool", tool)
		}
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
	if !strings.Contains(src, "BEGIN exe-image-forge machine context") ||
		!strings.Contains(src, "END exe-image-forge machine context") {
		t.Error("generated block is not delimited by markers; regeneration would " +
			"destroy anything the user added to these files")
	}

	// Optional components must be reported conditionally.
	for _, guard := range []string{"have codex ", "have claude ", "have go ", "have gemini "} {
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
	if !strings.Contains(string(df), "/etc/exe-image-forge-variant") {
		t.Error("variant is not recorded, so the context cannot name it")
	}
}

// Baking credentials must not overlay the per-variant context files, or every
// variant would inherit whichever one seeded the auth home. The allowlist now
// enforces this by construction; see TestBakeAllowlistExcludesAgentContext.
func TestBakeDoesNotClobberAgentContext(t *testing.T) {
	for _, f := range []string{".codex/AGENTS.md", ".claude/CLAUDE.md", ".gemini/GEMINI.md"} {
		if strings.Contains(credAllowlist(t), f) {
			t.Errorf("bake would copy %s; the baked image would carry a "+
				"context file describing a different variant", f)
		}
	}
}

// Credential detection must not depend solely on a private file layout. When
// the file is absent or unrecognised, the CLI's own status command is the
// authority; without this a successful login can read as "missing" forever.
func TestClaudeDetectionFallsBackToTheCLI(t *testing.T) {
	b, err := os.ReadFile("creds.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "claudeAuthStatus") {
		t.Fatal("no fallback to `claude auth status`")
	}
	if !strings.Contains(src, `"auth", "status", "--json"`) {
		t.Error("fallback does not use the machine-readable status output")
	}
	// The fallback must only run when the file check came up empty, or every
	// page load would pay for a container start.
	i := strings.Index(src, `if cl.State == "missing" {`)
	j := strings.Index(src, "claudeAuthStatus(home)")
	if i < 0 || j < 0 || j < i {
		t.Error("fallback is not gated on the file check having failed")
	}
}

// The recognised credential file must match where claude actually writes.
func TestClaudeCredentialPath(t *testing.T) {
	for _, c := range inspectCreds(t.TempDir()) {
		if c.Tool != "claude" {
			continue
		}
		if c.File != ".claude/.credentials.json" {
			t.Errorf("claude credential file = %q; claude writes "+
				"~/.claude/.credentials.json when libsecret is unavailable, "+
				"which is the case in this image", c.File)
		}
	}
}

// Gemini CLI 0.53 encrypts tokens into .gemini/gemini-credentials.json and no
// longer writes the plaintext oauth_creds.json, so looking only for the old
// path reported a completed login as missing.
func TestGeminiDetectsEncryptedCredentials(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".gemini/gemini-credentials.json",
		"c2d812bd:76f060d5:6d0a3037e25b7f2f70e6dab53376a484")
	write(t, home, ".gemini/google_accounts.json", `{"active":"a@example.com"}`)

	for _, c := range inspectCreds(home) {
		if c.Tool != "gemini" {
			continue
		}
		if c.State != "ok" {
			t.Errorf("state = %q, want ok: the encrypted credential file is "+
				"where gemini stores its login now", c.State)
		}
		if c.Detail != "a@example.com" {
			t.Errorf("detail = %q, want the signed-in account", c.Detail)
		}
	}
}

// The older plaintext layout must keep working, including its expiry.
func TestGeminiStillReadsLegacyPlaintextCredentials(t *testing.T) {
	home := t.TempDir()
	exp := time.Now().Add(time.Hour).UnixMilli()
	write(t, home, ".gemini/oauth_creds.json", fmt.Sprintf(
		`{"access_token":"a","refresh_token":"r","expiry_date":%d}`, exp))

	for _, c := range inspectCreds(home) {
		if c.Tool != "gemini" {
			continue
		}
		if c.State != "ok" || !c.Refreshable {
			t.Errorf("legacy creds: state=%q refreshable=%v", c.State, c.Refreshable)
		}
		if c.Expires == "" {
			t.Error("legacy creds carry an expiry; it should be surfaced")
		}
	}
}

// Gemini gained a paste-the-code flow (NO_BROWSER=true redirects to
// codeassist.google.com/authcode), so it no longer needs the callback relay.
func TestGeminiLoginAvoidsLocalhostCallback(t *testing.T) {
	for _, c := range inspectCreds(t.TempDir()) {
		if c.Tool != "gemini" {
			continue
		}
		if !strings.Contains(c.LoginCmd, "NO_BROWSER") {
			t.Errorf("login_cmd = %q; without NO_BROWSER gemini opens a "+
				"localhost callback the user's browser cannot reach", c.LoginCmd)
		}
		if c.NeedsRelay {
			t.Error("needs_relay is set, but the paste-code flow needs no relay")
		}
	}
}

// bake ships an image to other people. It must copy an explicit allowlist of
// credential files, never sweep the auth home with excludes: the admin
// terminal leaves conversation logs there (.gemini/tmp/*/logs.json and
// chats/*.jsonl, codex state_*.sqlite, .bash_history), and a denylist ships
// whatever nobody thought to exclude.
func TestBakeCopiesAnAllowlistNotTheWholeHome(t *testing.T) {
	b, err := os.ReadFile("../exe-image-forge")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "cmd_bake()")
	if i < 0 {
		t.Fatal("cmd_bake not found")
	}
	bake := src[i:]
	if j := strings.Index(bake, "\ncmd_"); j > 0 {
		bake = bake[:j]
	}

	if !strings.Contains(bake, "CRED_FILES") {
		t.Error("bake does not use the credential allowlist")
	}
	// Tarring the auth home directly is the bug: it takes everything present.
	if strings.Contains(bake, `tar -C "$AUTHHOME"`) {
		t.Error("bake tars $AUTHHOME wholesale; conversation logs and shell " +
			"history would be baked into the published image")
	}

	// Things that must never be in the allowlist.
	for _, bad := range []string{
		".gemini/tmp", ".gemini/history", ".bash_history",
		".codex/sessions", ".claude.json", ".codex/state",
	} {
		if strings.Contains(credAllowlist(t), bad) {
			t.Errorf("allowlist contains %q, which holds conversation or "+
				"session data, not credentials", bad)
		}
	}
	// And the credentials that must be in it, or the image ships unauthenticated.
	for _, want := range []string{
		".codex/auth.json", ".claude/.credentials.json",
		".gemini/gemini-credentials.json", ".config/gh/hosts.yml",
	} {
		if !strings.Contains(credAllowlist(t), want) {
			t.Errorf("allowlist is missing %q", want)
		}
	}
}

func credAllowlist(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../exe-image-forge")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "CRED_FILES=(")
	if i < 0 {
		t.Fatal("CRED_FILES not defined")
	}
	j := strings.Index(src[i:], ")")
	return src[i : i+j]
}

// The agent-context files are generated per variant inside the image; baking a
// copy from the auth home would overwrite one variant's context with another's.
func TestBakeAllowlistExcludesAgentContext(t *testing.T) {
	for _, f := range []string{".codex/AGENTS.md", ".claude/CLAUDE.md", ".gemini/GEMINI.md"} {
		if strings.Contains(credAllowlist(t), f) {
			t.Errorf("allowlist contains %q; it is generated in the image", f)
		}
	}
}

// Gemini encrypts credentials with a key derived from scrypt(hostname+user),
// so the file copied into an image cannot be decrypted when that image boots
// on a different host. bake must convert it to a portable form first, on the
// machine that logged in; otherwise every image ships a gemini that is not
// logged in while the admin page cheerfully reports "ok".
func TestBakeConvertsGeminiCredentialsToAPortableForm(t *testing.T) {
	b, err := os.ReadFile("../exe-image-forge")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "gemini-export-creds.js") {
		t.Fatal("bake does not convert gemini credentials; the encrypted file " +
			"is keyed to this hostname and is useless in the shipped image")
	}
	if !strings.Contains(src, "/etc/exe-image-forge-credentialed") {
		t.Error("baked image has no credentialed marker, so generated agent " +
			"context cannot distinguish it from the logged-out base image")
	}
	// The conversion has to happen on this host, so the hostname used for the
	// key derivation matches the one that performed the login.
	if !strings.Contains(src, `--hostname "$(hostname)"`) {
		t.Error("conversion does not pin the hostname; the key would not derive")
	}
	// An API-key login has no file for the CLI to find; it reads the env.
	if !strings.Contains(src, "ENV GEMINI_API_KEY=") {
		t.Error("an API-key credential is never baked into the image env")
	}

	if _, err := os.Stat("../image/files/gemini-export-creds.js"); err != nil {
		t.Errorf("converter missing: %v", err)
	}
}

// The vending page lists both repos, and their bare names do not reveal that
// only one carries credentials. Defaulting to the credential-less base image
// meant the obvious choice produced a VM where every CLI was logged out --
// the exact problem this project exists to solve.
func TestVendingPrefersAndLabelsTheCredentialedImage(t *testing.T) {
	s := &server{cfg: Config{Repos: []string{"exe-image-forge/dev", "exe-image-forge/base"}}}
	info := s.repoInfo()
	if len(info) != 2 {
		t.Fatalf("got %d repos", len(info))
	}
	// First entry is the default selection in the UI.
	if info[0]["name"] != "exe-image-forge/dev" || info[0]["baked"] != true {
		t.Errorf("default repo is %v (baked=%v); the credentialed image must "+
			"come first", info[0]["name"], info[0]["baked"])
	}
	for _, r := range info {
		label, _ := r["label"].(string)
		if !strings.Contains(label, "signed in") && !strings.Contains(label, "credentials") {
			t.Errorf("label %q does not say whether credentials are included", label)
		}
	}
}

func TestSessionStateExpiresImmediately(t *testing.T) {
	a := &admin{sessions: map[string]*session{}}
	a.sessions["active"] = &session{expires: time.Now().Add(time.Hour)}
	a.sessions["expired"] = &session{expires: time.Now().Add(-time.Second)}

	request := func(token string) *http.Request {
		r := httptest.NewRequest("GET", "/api/session", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		return r
	}
	if ok, expires := a.sessionState(request("active")); !ok || expires.IsZero() {
		t.Error("active session was not reported with its expiry")
	}
	if ok, _ := a.sessionState(request("expired")); ok {
		t.Error("expired session was accepted")
	}
	a.mu.Lock()
	_, stillStored := a.sessions["expired"]
	a.mu.Unlock()
	if stillStored {
		t.Error("expired session was not removed immediately")
	}
}

func TestPasswordLoginCreatesObservableSession(t *testing.T) {
	const password = "test-password"
	salt := "0123456789abcdef"
	a := &admin{
		srv:      &server{cfg: Config{Salt: salt, Hash: hashPassword(password, salt)}},
		sessions: map[string]*session{},
		pk:       loadPasskeyStore(filepath.Join(t.TempDir(), "passkeys.json")),
	}
	body, _ := json.Marshal(map[string]string{"password": password})
	loginReq := httptest.NewRequest("POST", "/admin/api/login", bytes.NewReader(body))
	loginResp := httptest.NewRecorder()
	a.handleLogin(loginResp, loginReq)
	if loginResp.Code != http.StatusOK {
		t.Fatalf("login returned %d: %s", loginResp.Code, loginResp.Body)
	}
	var cookie *http.Cookie
	for _, c := range loginResp.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
			break
		}
	}
	if cookie == nil {
		t.Fatal("login did not issue a session cookie")
	}

	sessionReq := httptest.NewRequest("GET", "/api/session", nil)
	sessionReq.Host = "images.example.com"
	sessionReq.AddCookie(cookie)
	sessionResp := httptest.NewRecorder()
	a.handleSession(sessionResp, sessionReq)
	var state struct {
		Authed  bool   `json:"authed"`
		Expires string `json:"expires"`
	}
	if err := json.Unmarshal(sessionResp.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Authed || state.Expires == "" {
		t.Errorf("session endpoint returned %+v, want authenticated with expiry", state)
	}
	if cache := sessionResp.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cache)
	}
}

func TestWebUIUsesEnglishSessionGatesAndAdminTabs(t *testing.T) {
	index, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := os.ReadFile("admin.html")
	if err != nil {
		t.Fatal(err)
	}
	passkey, err := os.ReadFile("passkey.js")
	if err != nil {
		t.Fatal(err)
	}
	all := string(index) + string(admin) + string(passkey)
	for _, r := range all {
		if r >= '\uac00' && r <= '\ud7a3' {
			t.Fatal("Korean text remains in the browser UI")
		}
	}
	indexSource := string(index)
	for _, marker := range []string{
		`id="authgate"`, `id="grantapp" class="hide"`, "/api/session",
		"BroadcastChannel", "setInterval(()=>checkSession().catch(()=>{}),1000)",
	} {
		if !strings.Contains(indexSource, marker) {
			t.Errorf("vending page is missing session gate marker %q", marker)
		}
	}
	adminSource := string(admin)
	for _, tab := range []string{"overview", "logins", "images", "security"} {
		if !strings.Contains(adminSource, `data-tab="`+tab+`"`) ||
			!strings.Contains(adminSource, `data-panel="`+tab+`"`) {
			t.Errorf("admin UI is missing the %q tab or panel", tab)
		}
	}
}

// bake must confirm the logins actually made it into the image. A dev image
// that ships logged out looks fine until someone tries to use it.
func TestBakeVerifiesTheLoginsSurvive(t *testing.T) {
	b, err := os.ReadFile("../exe-image-forge")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "cmd_bake()")
	bake := src[i:]
	if j := strings.Index(bake, "\ncmd_"); j > 0 {
		bake = bake[:j]
	}
	for _, tool := range []string{"gh", "codex", "claude", "gemini"} {
		if !strings.Contains(bake, `"`+tool+`"`) && !strings.Contains(bake, " "+tool+" ") {
			t.Errorf("bake does not verify %s is logged in afterwards", tool)
		}
	}
	if !strings.Contains(bake, "NOT logged in") {
		t.Error("bake does not warn when a credential failed to carry over")
	}
}

// Layers are served zstd, not gzip: ~20% smaller and several times faster to
// decompress, which is most of the wait after the bytes land. buildx needs
// force-compression, or it passes the parent's gzip layers straight through
// and the setting silently does nothing.
func TestPublishedLayersUseZstd(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *server) publish(")
	if i < 0 {
		t.Fatal("publish not found")
	}
	pub := src[i:]
	if j := strings.Index(pub, "\nfunc "); j > 0 {
		pub = pub[:j]
	}
	if !strings.Contains(pub, "compression=zstd") {
		t.Error("publish does not request zstd layers")
	}
	if !strings.Contains(pub, "force-compression=true") {
		t.Error("publish omits force-compression, so buildx will pass through " +
			"the parent's gzip layers and zstd will silently not apply")
	}
	// Plain `docker build` cannot set layer compression at all.
	if strings.Contains(pub, `"docker", "build"`) {
		t.Error("publish uses `docker build`, which cannot emit zstd layers")
	}
}

// The image build must agree with publish, or the parent layers are gzip and
// every vended tag pays to recompress them.
func TestImageBuildUsesZstd(t *testing.T) {
	b, err := os.ReadFile("../exe-image-forge")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"COMPRESSION=${COMPRESSION:-zstd}", "force-compression=true"} {
		if !strings.Contains(src, want) {
			t.Errorf("exe-image-forge build is missing %q", want)
		}
	}
}

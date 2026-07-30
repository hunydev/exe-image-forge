package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
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

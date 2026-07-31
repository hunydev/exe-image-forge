package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Cred describes the login state of one CLI inside the auth home.
type Cred struct {
	Tool string `json:"tool"`
	// Name is the human label shown in the UI.
	Name string `json:"name"`
	File string `json:"file"`
	// State is one of: missing, ok, stale, expired, unknown.
	State string `json:"state"`
	// Expires is the access-token expiry, if we can determine it.
	Expires string `json:"expires,omitempty"`
	// SecondsLeft is negative once expired.
	SecondsLeft int64 `json:"seconds_left,omitempty"`
	// Refreshable means a refresh token is present, so an expired access
	// token is not actually a problem; the CLI renews it on first use.
	Refreshable bool `json:"refreshable"`
	// Detail is a short human note (account name, subscription, reason).
	Detail string `json:"detail,omitempty"`
	// LoginCmd is the command the user should run in the admin terminal.
	LoginCmd string `json:"login_cmd"`
	// NeedsRelay marks CLIs whose OAuth flow ends at a localhost callback that
	// the user's browser cannot reach, so the URL must be relayed.
	NeedsRelay bool `json:"needs_relay"`
}

// jwtExp pulls the exp claim out of a JWT without verifying it. We only use it
// for display, never for a trust decision.
func jwtExp(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp   int64  `json:"exp"`
		Email string `json:"email"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// finish fills in the derived fields from an expiry instant.
func (c *Cred) setExpiry(t time.Time) {
	c.Expires = t.UTC().Format(time.RFC3339)
	left := time.Until(t)
	c.SecondsLeft = int64(left.Seconds())
	switch {
	case left <= 0 && c.Refreshable:
		c.State = "stale"
	case left <= 0:
		c.State = "expired"
	case left < 24*time.Hour && !c.Refreshable:
		c.State = "stale"
	default:
		c.State = "ok"
	}
}

var ghTokenRe = regexp.MustCompile(`(?m)^\s+oauth_token:\s*(\S+)`)
var ghUserRe = regexp.MustCompile(`(?m)^\s+user:\s*(\S+)`)

func inspectCreds(home string) []Cred {
	j := func(p ...string) string { return filepath.Join(append([]string{home}, p...)...) }

	out := []Cred{}

	// --- gh ---------------------------------------------------------
	gh := Cred{Tool: "gh", Name: "GitHub CLI", File: ".config/gh/hosts.yml",
		State: "missing", LoginCmd: "gh auth login --git-protocol https"}
	if b, err := os.ReadFile(j(".config", "gh", "hosts.yml")); err == nil && len(b) > 0 {
		if m := ghTokenRe.FindSubmatch(b); m != nil {
			// gh tokens do not carry an expiry; they are revoked, not aged out.
			gh.State = "ok"
			gh.Refreshable = true
			gh.Detail = "토큰 (만료 없음)"
			if u := ghUserRe.FindSubmatch(b); u != nil {
				gh.Detail = string(u[1]) + " · 만료 없음"
			}
		} else {
			gh.State = "unknown"
			gh.Detail = "hosts.yml 에 토큰이 없습니다"
		}
	}
	out = append(out, gh)

	// --- codex ------------------------------------------------------
	// codex: --device-auth avoids the localhost:1455 callback entirely.
	cx := Cred{Tool: "codex", Name: "Codex CLI", File: ".codex/auth.json",
		State: "missing", LoginCmd: "codex login --device-auth"}
	var cxa struct {
		APIKey *string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if readJSON(j(".codex", "auth.json"), &cxa) == nil {
		switch {
		case cxa.Tokens != nil && cxa.Tokens.RefreshToken != "":
			cx.Refreshable = true
			cx.Detail = "ChatGPT 로그인"
			if t, ok := jwtExp(cxa.Tokens.IDToken); ok {
				cx.setExpiry(t)
			} else {
				cx.State = "ok"
			}
		case cxa.APIKey != nil && *cxa.APIKey != "":
			cx.State = "ok"
			cx.Refreshable = true
			cx.Detail = "API 키 (만료 없음)"
		default:
			cx.State = "unknown"
			cx.Detail = "auth.json 을 해석할 수 없습니다"
		}
	}
	out = append(out, cx)

	// --- claude -----------------------------------------------------
	// claude: setup-token redirects to platform.claude.com and prints a code to
	// paste back, so no localhost listener is involved.
	cl := Cred{Tool: "claude", Name: "Claude Code", File: ".claude/.credentials.json",
		State: "missing", LoginCmd: "claude setup-token"}
	var cla struct {
		OAuth *struct {
			AccessToken      string `json:"accessToken"`
			RefreshToken     string `json:"refreshToken"`
			ExpiresAt        int64  `json:"expiresAt"`
			SubscriptionType string `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	if readJSON(j(".claude", ".credentials.json"), &cla) == nil && cla.OAuth != nil {
		cl.Refreshable = cla.OAuth.RefreshToken != ""
		cl.Detail = cla.OAuth.SubscriptionType
		if cla.OAuth.ExpiresAt > 0 {
			cl.setExpiry(time.UnixMilli(cla.OAuth.ExpiresAt))
		} else if cla.OAuth.AccessToken != "" {
			cl.State = "ok"
		} else {
			cl.State = "unknown"
		}
	}
	out = append(out, cl)

	// --- gemini -----------------------------------------------------
	// gemini: no device flow exists; it always spawns a localhost listener, so
	// the callback URL has to be relayed back into the VM. See handleRelay.
	gm := Cred{Tool: "gemini", Name: "Gemini CLI", File: ".gemini/oauth_creds.json",
		State: "missing", LoginCmd: "gemini", NeedsRelay: true}
	var gma struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiryDate   int64  `json:"expiry_date"`
	}
	if readJSON(j(".gemini", "oauth_creds.json"), &gma) == nil {
		gm.Refreshable = gma.RefreshToken != ""
		var acct struct {
			Active string `json:"active"`
		}
		if readJSON(j(".gemini", "google_accounts.json"), &acct) == nil && acct.Active != "" {
			gm.Detail = acct.Active
		}
		if gma.ExpiryDate > 0 {
			gm.setExpiry(time.UnixMilli(gma.ExpiryDate))
		} else if gma.AccessToken != "" {
			gm.State = "ok"
		} else {
			gm.State = "unknown"
		}
	}
	out = append(out, gm)

	return out
}

// summarize turns credential states into headline warnings for the main page.
func summarize(creds []Cred) []string {
	var warn []string
	var missing, expired, stale []string
	for _, c := range creds {
		switch c.State {
		case "missing":
			missing = append(missing, c.Name)
		case "expired", "unknown":
			expired = append(expired, c.Name)
		case "stale":
			stale = append(stale, c.Name)
		}
	}
	if len(missing) > 0 {
		warn = append(warn, fmt.Sprintf("로그인되지 않음: %s", strings.Join(missing, ", ")))
	}
	if len(expired) > 0 {
		warn = append(warn, fmt.Sprintf("만료됨 (재로그인 필요): %s", strings.Join(expired, ", ")))
	}
	if len(stale) > 0 {
		warn = append(warn, fmt.Sprintf("액세스 토큰 만료 — 첫 실행 시 자동 갱신됩니다: %s", strings.Join(stale, ", ")))
	}
	return warn
}

// toolVersions reads the version manifest baked into an image by the
// update-ai-clis script. Reading a file beats running four CLIs, each of which
// can take seconds to start.
func (s *server) toolVersions() map[string]string {
	img := s.cfg.DevImage
	if img == "" {
		return nil
	}
	s.verMu.Lock()
	if s.verCache != nil && time.Since(s.verAt) < 5*time.Minute {
		v := s.verCache
		s.verMu.Unlock()
		return v
	}
	s.verMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Read from the fullest variant so every optional tool reports a version;
	// the leaner variants share these binaries, they just omit some.
	base, _, _ := strings.Cut(img, ":")
	var out []byte
	var err error
	for _, cand := range []string{base + ":go-gemini", img, "hunydev/base:go-gemini"} {
		out, err = exec.CommandContext(ctx, "docker", "run", "--rm", "--entrypoint", "cat",
			cand, "/etc/ai-cli-versions.json").Output()
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil
	}
	var v map[string]string
	if err := json.Unmarshal(out, &v); err != nil {
		return nil
	}
	s.verMu.Lock()
	s.verCache, s.verAt = v, time.Now()
	s.verMu.Unlock()
	return v
}

// variantInfo describes one buildable image variant for the vending page.
type variantInfo struct {
	Built bool   `json:"built"`
	Bytes int64  `json:"bytes,omitempty"`
	Size  string `json:"size,omitempty"`
}

// Variants are ordered smallest-first; the names match the image tags and the
// WITH_* build args in the Dockerfile.
var variantNames = []string{"min", "gemini", "go", "go-gemini"}

func humanSize(b int64) string {
	const u = 1024
	if b < u*u {
		return fmt.Sprintf("%dKB", b/u)
	}
	if b < u*u*u {
		return fmt.Sprintf("%dMB", b/(u*u))
	}
	return fmt.Sprintf("%.1fGB", float64(b)/float64(u*u*u))
}

// variants reports which image variants exist and how big they are, so the
// vending page can price each option instead of making the user guess.
//
// The reported size is the compressed size, which matches the sum of the layer
// blobs in the registry manifest and is therefore what a `docker pull`
// actually transfers. The on-disk size after unpacking is several times larger.
func (s *server) variants() map[string]variantInfo {
	s.verMu.Lock()
	if s.varCache != nil && time.Since(s.varAt) < 2*time.Minute {
		v := s.varCache
		s.verMu.Unlock()
		return v
	}
	s.verMu.Unlock()

	// Price the variants using whichever repo the user will actually pull.
	// The baked dev image is the interesting one when it exists.
	repo := "hunydev/base"
	if s.cfg.DevImage != "" {
		if _, err := exec.Command("docker", "image", "inspect", s.cfg.DevImage).Output(); err == nil {
			repo = "hunydev/dev"
		}
	}

	out := map[string]variantInfo{}
	for _, name := range variantNames {
		info := variantInfo{}
		b, err := exec.Command("docker", "image", "inspect", repo+":"+name,
			"--format", "{{.Size}}").Output()
		if err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
				info.Built, info.Bytes, info.Size = true, n, humanSize(n)
			}
		}
		out[name] = info
	}
	s.verMu.Lock()
	s.varCache, s.varAt = out, time.Now()
	s.verMu.Unlock()
	return out
}

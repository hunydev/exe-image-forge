package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	cx := Cred{Tool: "codex", Name: "Codex CLI", File: ".codex/auth.json",
		State: "missing", LoginCmd: "codex login"}
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
	cl := Cred{Tool: "claude", Name: "Claude Code", File: ".claude/.credentials.json",
		State: "missing", LoginCmd: "claude  # 그다음 /login"}
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
	gm := Cred{Tool: "gemini", Name: "Gemini CLI", File: ".gemini/oauth_creds.json",
		State: "missing", LoginCmd: "gemini  # 그다음 /auth"}
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

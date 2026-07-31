package main

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const demoPassword = "forge-demo"

// loopbackListenAddress prevents the fixture-backed demo server from ever
// becoming a remotely reachable vending service.
func loopbackListenAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func demoConfig(dir string) Config {
	salt := "64656d6f2d6f6e6c792d73616c742121"
	return Config{
		Salt:       salt,
		Hash:       hashPassword(demoPassword, salt),
		PullHost:   "127.0.0.1:18080",
		Repos:      []string{"demo/dev", "demo/base"},
		VMToken:    "",
		TTLMinutes: 30,
		SourceImage: map[string]string{
			"demo/dev":  "demo/dev:latest",
			"demo/base": "demo/base:latest",
		},
		AuthHome:    filepath.Join(dir, "authhome"),
		DevImage:    "demo/dev:latest",
		PasskeyFile: filepath.Join(dir, "passkeys.json"),
	}
}

func demoCredentials() []Cred {
	expires := time.Now().Add(14 * 24 * time.Hour)
	expiry := expires.UTC().Format(time.RFC3339)
	seconds := int64(time.Until(expires).Seconds())
	return []Cred{
		{
			Tool: "gh", Name: "GitHub CLI", File: ".config/gh/hosts.yml",
			State: "ok", Refreshable: true, Detail: "demo-user · no expiry",
			LoginCmd: "gh auth login --git-protocol https",
		},
		{
			Tool: "codex", Name: "Codex CLI", File: ".codex/auth.json",
			State: "ok", Expires: expiry, SecondsLeft: seconds, Refreshable: true,
			Detail: "Demo ChatGPT sign-in", LoginCmd: "codex login --device-auth",
		},
		{
			Tool: "claude", Name: "Claude Code", File: ".claude/.credentials.json",
			State: "ok", Expires: expiry, SecondsLeft: seconds, Refreshable: true,
			Detail: "Demo subscription", LoginCmd: "claude auth login",
		},
		{
			Tool: "gemini", Name: "Gemini CLI", File: ".gemini/gemini-credentials.json",
			State: "ok", Refreshable: true, Detail: "demo@example.invalid",
			LoginCmd: "NO_BROWSER=true gemini",
		},
	}
}

func demoVariants() map[string]variantInfo {
	out := make(map[string]variantInfo, len(variantNames))
	base := int64(245 * 1024 * 1024)
	for i, name := range variantNames {
		size := base + int64(i)*23*1024*1024
		out[name] = variantInfo{Built: true, Bytes: size, Size: humanSize(size)}
	}
	return out
}

func demoToolVersions() map[string]string {
	return map[string]string{
		"updated": "2026-01-15T12:00:00Z",
		"gh":      "2.85.0",
		"node":    "24.0.0",
		"python":  "3.12.3",
		"uv":      "0.9.0",
		"codex":   "0.110.0",
		"claude":  "2.1.0",
		"gemini":  "0.60.0",
		"go":      "1.25.0",
	}
}

func demoAgentContext(variant string) string {
	if variant == "" {
		variant = "min"
	}
	return fmt.Sprintf(`# Exe Image Forge demo context

This is fixture data from the loopback-only development server.

- Variant: %s
- OS: Ubuntu 24.04 LTS
- AI CLIs: reported from deterministic demo fixtures
- Credentials: simulated; no provider tokens are present
- Registry: simulated; no image is built or pushed
`, variant)
}

func (a *admin) handleDemoTerm(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	_ = conn.Write(ctx, websocket.MessageText, []byte(
		"Exe Image Forge demo terminal\r\nCommands are echoed but never executed.\r\n$ ",
	))
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if len(data) > 0 && data[0] == 0 {
			continue
		}
		if kind != websocket.MessageText && kind != websocket.MessageBinary {
			continue
		}
		text := strings.ReplaceAll(string(data), "\n", "\r\n")
		if err := conn.Write(ctx, websocket.MessageText,
			[]byte(text+"\r\n[demo] command execution is disabled\r\n$ ")); err != nil {
			return
		}
	}
}

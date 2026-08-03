package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestHostAuthCommandAllowlist(t *testing.T) {
	tests := []struct {
		tool    string
		command string
		args    []string
		env     []string
	}{
		{tool: "gh", command: "gh", args: []string{"auth", "login", "--git-protocol", "https"}},
		{tool: "codex", command: "codex", args: []string{"login", "--device-auth"}},
		{tool: "claude", command: "claude", args: []string{"auth", "login"}},
		{tool: "gemini", command: "gemini", env: []string{"NO_BROWSER=true"}},
		{tool: "wrangler", command: "wrangler", args: []string{"login", "--no-use-keyring"}},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			got, ok := hostAuthCommand(tt.tool)
			if !ok {
				t.Fatal("allowlisted tool was rejected")
			}
			if got.command != tt.command || !slices.Equal(got.args, tt.args) || !slices.Equal(got.env, tt.env) {
				t.Fatalf("hostAuthCommand(%q) = %#v", tt.tool, got)
			}
		})
	}
}

func TestHostAuthCommandRejectsBrowserCommands(t *testing.T) {
	for _, tool := range []string{"", "bash", "gh auth status", "../codex", "gemini; id"} {
		if spec, ok := hostAuthCommand(tool); ok {
			t.Errorf("hostAuthCommand(%q) unexpectedly allowed %#v", tool, spec)
		}
	}
}

func TestAuthHostTerminalRejectsUnknownToolBeforeUpgrade(t *testing.T) {
	a := &admin{
		srv:      &server{terminalMode: "auth-host"},
		sessions: map[string]*session{},
	}
	for _, tool := range []string{"", "bash", "gh%20auth%20status"} {
		request := httptest.NewRequest(http.MethodGet, "/admin/api/term?tool="+tool, nil)
		response := httptest.NewRecorder()
		a.handleTerm(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("tool %q returned %d, want 400", tool, response.Code)
		}
	}
}

func TestAuthTerminalEnvReplacesIdentityValues(t *testing.T) {
	t.Setenv("HOME", "/should/not/survive")
	t.Setenv("XDG_CONFIG_HOME", "/should/not/survive/config")
	t.Setenv("USER", "someone")
	t.Setenv("LOGNAME", "someone")
	t.Setenv("TERM", "dumb")
	t.Setenv("LANG", "C")

	env := authTerminalEnv("/var/lib/forge/authhome", []string{"NO_BROWSER=true"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{
		"\nHOME=/var/lib/forge/authhome\n",
		"\nXDG_CONFIG_HOME=/var/lib/forge/authhome/.config\n",
		"\nUSER=exedev\n",
		"\nLOGNAME=exedev\n",
		"\nTERM=xterm-256color\n",
		"\nLANG=en_US.UTF-8\n",
		"\nNO_BROWSER=true\n",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment is missing %q", strings.TrimSpace(want))
		}
	}
	for _, forbidden := range []string{"/should/not/survive", "\nUSER=someone\n", "\nTERM=dumb\n"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("environment retained %q", forbidden)
		}
	}
}

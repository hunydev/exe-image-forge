package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminUsesOnlyEmbeddedTerminalAssets(t *testing.T) {
	source := string(adminHTML)
	if strings.Contains(source, "cdn.jsdelivr.net") {
		t.Fatal("authenticated admin page still executes CDN content")
	}
	for _, path := range []string{
		`href="/assets/xterm.css"`,
		`src="/assets/xterm.js"`,
		`src="/assets/addon-fit.js"`,
	} {
		if !strings.Contains(source, path) {
			t.Errorf("admin page is missing local asset %s", path)
		}
	}
	if len(xtermJS) < 100_000 || len(xtermCSS) < 1_000 || len(addonFitJS) < 1_000 {
		t.Fatal("one or more embedded terminal distributions are unexpectedly empty")
	}
}

func TestPageCSPAllowsPinnedInlineCodeButNoArbitraryScript(t *testing.T) {
	for name, page := range map[string][]byte{"index": indexHTML, "admin": adminHTML} {
		t.Run(name, func(t *testing.T) {
			csp := pageCSP(page)
			if !strings.Contains(csp, "script-src 'self' 'sha256-") {
				t.Errorf("CSP has no inline script hash: %s", csp)
			}
			scriptDirective := strings.Split(strings.Split(csp, "script-src ")[1], ";")[0]
			if strings.Contains(scriptDirective, "unsafe-inline") ||
				strings.Contains(scriptDirective, "http:") ||
				strings.Contains(scriptDirective, "https:") {
				t.Errorf("script directive is too broad: %s", scriptDirective)
			}
			for _, directive := range []string{
				"default-src 'none'", "frame-ancestors 'none'",
				"object-src 'none'", "base-uri 'none'",
			} {
				if !strings.Contains(csp, directive) {
					t.Errorf("CSP missing %q: %s", directive, csp)
				}
			}
		})
	}
}

func TestSecurityHeadersWrapEveryResponse(t *testing.T) {
	handler := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	for header, want := range map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Strict-Transport-Security":  "max-age=31536000",
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if got := w.Header().Get("Permissions-Policy"); !strings.Contains(got, "camera=()") {
		t.Errorf("Permissions-Policy = %q", got)
	}
}

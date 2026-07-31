package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkey is a stored credential. Credentials are scoped to the RP ID they were
// created under: a passkey minted on image-forge.exe.xyz is unusable on
// images.example.com and vice versa, so we keep the RP ID and filter by it.
type Passkey struct {
	Label      string              `json:"label"`
	RPID       string              `json:"rpid"`
	Credential webauthn.Credential `json:"credential"`
	Created    time.Time           `json:"created"`
	LastUsed   time.Time           `json:"last_used,omitempty"`
	Uses       int                 `json:"uses"`
}

// passkeyStore persists credentials and the stable user handle.
type passkeyStore struct {
	path string

	mu     sync.Mutex
	Handle string     `json:"handle"`
	Keys   []*Passkey `json:"keys"`
}

func loadPasskeyStore(path string) *passkeyStore {
	s := &passkeyStore{path: path}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, s); err != nil {
			log.Printf("passkeys: %v (starting empty)", err)
		}
	}
	if s.Handle == "" {
		s.Handle = randHex(32)
		s.saveLocked()
	}
	return s
}

func (s *passkeyStore) saveLocked() {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("passkeys marshal: %v", err)
		return
	}
	os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("passkeys write: %v", err)
		return
	}
	os.Rename(tmp, s.path)
}

// forRP returns the credentials usable on the given RP ID.
func (s *passkeyStore) forRP(rpid string) []*Passkey {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Passkey
	for _, k := range s.Keys {
		if k.RPID == rpid {
			out = append(out, k)
		}
	}
	return out
}

func (s *passkeyStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Keys)
}

func (s *passkeyStore) add(k *Passkey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Keys = append(s.Keys, k)
	s.saveLocked()
}

func (s *passkeyStore) remove(rpid, credID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.Keys {
		if k.RPID == rpid && base64.RawURLEncoding.EncodeToString(k.Credential.ID) == credID {
			s.Keys = append(s.Keys[:i], s.Keys[i+1:]...)
			s.saveLocked()
			return true
		}
	}
	return false
}

// touch records a successful authentication and persists the updated counter.
func (s *passkeyStore) touch(rpid string, cred *webauthn.Credential) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	for _, k := range s.Keys {
		if k.RPID == rpid && base64.RawURLEncoding.EncodeToString(k.Credential.ID) == id {
			// Persist the authenticator sign count so clone detection keeps working.
			k.Credential.Authenticator.SignCount = cred.Authenticator.SignCount
			k.LastUsed = time.Now()
			k.Uses++
			s.saveLocked()
			return
		}
	}
}

// adminUser adapts the store to webauthn.User. There is exactly one account.
type adminUser struct {
	handle []byte
	keys   []*Passkey
}

func (u *adminUser) WebAuthnID() []byte          { return u.handle }
func (u *adminUser) WebAuthnName() string        { return "admin" }
func (u *adminUser) WebAuthnDisplayName() string { return "Exe Image Forge admin" }
func (u *adminUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.keys))
	for _, k := range u.keys {
		out = append(out, k.Credential)
	}
	return out
}

// rpFor derives the WebAuthn relying party from the request host. The service is
// reachable under more than one hostname (the exe.xyz name and, once DNS is set
// up, images.example.com), and a passkey is permanently bound to the RP ID it was
// created with, so this must follow the host the user actually visited.
func (a *admin) rpFor(r *http.Request) (*webauthn.WebAuthn, string, error) {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if i := strings.IndexByte(host, ','); i >= 0 {
		host = strings.TrimSpace(host[:i])
	}
	hostname, port := host, ""
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		hostname, port = host[:i], host[i+1:]
	}
	if hostname == "" {
		return nil, "", errors.New("no host header")
	}

	// The proxy terminates TLS, so a forwarded request is https even though it
	// arrives here over plain http. Loopback is allowed to be http: browsers
	// treat localhost as a secure context, which is what makes local testing
	// of the passkey flow possible at all.
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" {
		scheme = "http"
	}
	origin := scheme + "://" + hostname
	if port != "" {
		origin += ":" + port
	}

	w, err := webauthn.New(&webauthn.Config{
		RPID:          hostname,
		RPDisplayName: "Exe Image Forge",
		RPOrigins:     []string{origin},
	})
	if err != nil {
		return nil, "", err
	}
	return w, hostname, nil
}

func (a *admin) userFor(rpid string) *adminUser {
	a.pk.mu.Lock()
	handle := a.pk.Handle
	a.pk.mu.Unlock()
	return &adminUser{handle: []byte(handle), keys: a.pk.forRP(rpid)}
}

// ---- registration (requires an existing admin session) ------------------

func (a *admin) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	wa, rpid, err := a.rpFor(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	user := a.userFor(rpid)
	creation, sd, err := wa.BeginRegistration(user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationPreferred,
		}),
		webauthn.WithExclusions(webauthn.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()),
	)
	if err != nil {
		http.Error(w, "begin registration: "+err.Error(), 500)
		return
	}
	a.mu.Lock()
	a.regSession = sd
	a.regRPID = rpid
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(creation)
}

func (a *admin) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	label := strings.TrimSpace(r.URL.Query().Get("label"))
	if label == "" {
		label = "passkey"
	}
	if len(label) > 60 {
		label = label[:60]
	}
	wa, rpid, err := a.rpFor(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a.mu.Lock()
	sd, sdRP := a.regSession, a.regRPID
	a.regSession = nil
	a.mu.Unlock()
	if sd == nil || sdRP != rpid {
		http.Error(w, "등록 세션이 없습니다. 다시 시도하세요", 400)
		return
	}
	cred, err := wa.FinishRegistration(a.userFor(rpid), *sd, r)
	if err != nil {
		http.Error(w, "등록 실패: "+err.Error(), 400)
		return
	}
	a.pk.add(&Passkey{Label: label, RPID: rpid, Credential: *cred, Created: time.Now()})
	log.Printf("passkey registered: %q on %s", label, rpid)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "label": label})
}

// ---- login (no password needed) -----------------------------------------

func (a *admin) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	wa, rpid, err := a.rpFor(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(a.pk.forRP(rpid)) == 0 {
		http.Error(w, "이 도메인에 등록된 패스키가 없습니다", 404)
		return
	}
	assertion, sd, err := wa.BeginDiscoverableLogin()
	if err != nil {
		http.Error(w, "begin login: "+err.Error(), 500)
		return
	}
	a.mu.Lock()
	a.loginSession = sd
	a.loginRPID = rpid
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(assertion)
}

func (a *admin) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	wa, rpid, err := a.rpFor(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a.mu.Lock()
	sd, sdRP := a.loginSession, a.loginRPID
	a.loginSession = nil
	a.mu.Unlock()
	if sd == nil || sdRP != rpid {
		http.Error(w, "로그인 세션이 없습니다. 다시 시도하세요", 400)
		return
	}

	// Rate-limit passkey attempts on the same counter the password uses, so a
	// broken or hostile client cannot hammer this endpoint for free.
	a.srv.mu.Lock()
	if time.Now().Before(a.srv.lockout) {
		wait := int(time.Until(a.srv.lockout).Seconds()) + 1
		a.srv.mu.Unlock()
		http.Error(w, fmt.Sprintf("too many attempts, retry in %ds", wait), 429)
		return
	}
	a.srv.mu.Unlock()

	discover := func(rawID, userHandle []byte) (webauthn.User, error) {
		a.pk.mu.Lock()
		want := a.pk.Handle
		a.pk.mu.Unlock()
		if string(userHandle) != want {
			return nil, errors.New("unknown user handle")
		}
		return a.userFor(rpid), nil
	}
	cred, err := wa.FinishDiscoverableLogin(discover, *sd, r)
	if err != nil {
		a.srv.mu.Lock()
		a.srv.fails++
		if a.srv.fails >= 5 {
			a.srv.lockout = time.Now().Add(time.Duration(a.srv.fails-4) * time.Minute)
		}
		a.srv.mu.Unlock()
		log.Printf("passkey login failed on %s: %v", rpid, err)
		http.Error(w, "인증 실패: "+err.Error(), 403)
		return
	}
	if cred.Authenticator.CloneWarning {
		log.Printf("passkey clone warning on %s; refusing", rpid)
		http.Error(w, "복제된 인증기로 판단되어 거부했습니다", 403)
		return
	}
	a.srv.mu.Lock()
	a.srv.fails = 0
	a.srv.mu.Unlock()
	a.pk.touch(rpid, cred)

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: a.newSession(), Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 8 * 3600,
	})
	log.Printf("passkey login ok on %s", rpid)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// ---- management ----------------------------------------------------------

func (a *admin) handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	_, rpid, err := a.rpFor(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	type item struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		RPID     string `json:"rpid"`
		Created  string `json:"created"`
		LastUsed string `json:"last_used,omitempty"`
		Uses     int    `json:"uses"`
		Current  bool   `json:"current_domain"`
	}
	a.pk.mu.Lock()
	out := []item{}
	for _, k := range a.pk.Keys {
		it := item{
			ID:      base64.RawURLEncoding.EncodeToString(k.Credential.ID),
			Label:   k.Label,
			RPID:    k.RPID,
			Created: k.Created.UTC().Format(time.RFC3339),
			Uses:    k.Uses,
			Current: k.RPID == rpid,
		}
		if !k.LastUsed.IsZero() {
			it.LastUsed = k.LastUsed.UTC().Format(time.RFC3339)
		}
		out = append(out, it)
	}
	a.pk.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"passkeys": out, "rpid": rpid})
}

func (a *admin) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		ID   string `json:"id"`
		RPID string `json:"rpid"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	rpid := req.RPID
	if rpid == "" {
		_, rpid, _ = a.rpFor(r)
	}
	if !a.pk.remove(rpid, req.ID) {
		http.Error(w, "패스키를 찾을 수 없습니다", 404)
		return
	}
	log.Printf("passkey removed on %s", rpid)
	w.Write([]byte(`{"ok":true}`))
}

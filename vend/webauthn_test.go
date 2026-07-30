package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// softAuthenticator is a minimal software WebAuthn authenticator. It exists so
// the registration and assertion ceremonies are exercised for real (actual
// COSE keys, actual ECDSA signatures, actual CBOR attestation objects) rather
// than mocked, because the parts most likely to break are exactly the encoding
// details a mock would paper over.
type softAuthenticator struct {
	t       *testing.T
	key     *ecdsa.PrivateKey
	credID  []byte
	rpIDsum [32]byte
	signCnt uint32
	aaguid  [16]byte
}

func newSoftAuthenticator(t *testing.T, rpID string) *softAuthenticator {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	rand.Read(id)
	return &softAuthenticator{t: t, key: k, credID: id, rpIDsum: sha256.Sum256([]byte(rpID))}
}

// coseKey encodes the public key as a COSE_Key (ES256 / P-256).
func (a *softAuthenticator) coseKey() []byte {
	a.t.Helper()
	x := make([]byte, 32)
	y := make([]byte, 32)
	a.key.PublicKey.X.FillBytes(x)
	a.key.PublicKey.Y.FillBytes(y)
	// Keys must be canonically ordered for the CBOR decoder used by the library.
	m := map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y}
	b, err := cbor.Marshal(m)
	if err != nil {
		a.t.Fatal(err)
	}
	return b
}

// authData builds the authenticator data structure. includeCred adds the
// attested credential data used during registration.
func (a *softAuthenticator) authData(includeCred bool, flags byte) []byte {
	var buf bytes.Buffer
	buf.Write(a.rpIDsum[:])
	buf.WriteByte(flags)
	var cnt [4]byte
	binary.BigEndian.PutUint32(cnt[:], a.signCnt)
	buf.Write(cnt[:])
	if includeCred {
		buf.Write(a.aaguid[:])
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(a.credID)))
		buf.Write(l[:])
		buf.Write(a.credID)
		buf.Write(a.coseKey())
	}
	return buf.Bytes()
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (a *softAuthenticator) clientData(typ, challenge, origin string) []byte {
	a.t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false,
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return b
}

// makeCredential produces the JSON body a browser would POST to register/finish.
func (a *softAuthenticator) makeCredential(challenge, origin string) []byte {
	a.t.Helper()
	// UP | UV | AT  (present, verified, attested credential data included)
	ad := a.authData(true, 0x01|0x04|0x40)
	att, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": ad,
	})
	if err != nil {
		a.t.Fatal(err)
	}
	cd := a.clientData("webauthn.create", challenge, origin)
	body, _ := json.Marshal(map[string]any{
		"id": b64u(a.credID), "rawId": b64u(a.credID), "type": "public-key",
		"clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON": b64u(cd), "attestationObject": b64u(att),
			"transports": []string{"internal"},
		},
	})
	return body
}

// getAssertion produces the JSON body a browser would POST to login/finish.
func (a *softAuthenticator) getAssertion(challenge, origin, userHandle string) []byte {
	a.t.Helper()
	a.signCnt++
	ad := a.authData(false, 0x01|0x04)
	cd := a.clientData("webauthn.get", challenge, origin)
	cdHash := sha256.Sum256(cd)
	signed := append(append([]byte{}, ad...), cdHash[:]...)
	digest := sha256.Sum256(signed)
	r, s, err := ecdsa.Sign(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}
	sig, err := asn1ECDSA(r, s)
	if err != nil {
		a.t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"id": b64u(a.credID), "rawId": b64u(a.credID), "type": "public-key",
		"clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON": b64u(cd), "authenticatorData": b64u(ad),
			"signature": b64u(sig), "userHandle": b64u([]byte(userHandle)),
		},
	})
	return body
}

// asn1ECDSA encodes (r,s) as the DER SEQUENCE WebAuthn expects.
func asn1ECDSA(r, s *big.Int) ([]byte, error) {
	type ecdsaSig struct{ R, S *big.Int }
	return asn1Marshal(ecdsaSig{r, s})
}

func newTestAdmin(t *testing.T) *admin {
	t.Helper()
	return &admin{
		srv:      &server{cfg: Config{Salt: "00", Hash: "unused"}},
		sessions: map[string]*session{},
		pk:       loadPasskeyStore(filepath.Join(t.TempDir(), "passkeys.json")),
	}
}

// req builds a request that looks like it came through the exe.dev proxy.
func req(method, path string, body []byte, host, cookie string) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("X-Forwarded-Host", host)
	r.Header.Set("X-Forwarded-Proto", "https")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	return r
}

// TestPasskeyFullCeremony registers a credential and then logs in with it,
// with no password involved in the second step.
func TestPasskeyFullCeremony(t *testing.T) {
	const host = "images.huny.dev"
	a := newTestAdmin(t)
	tok := a.newSession()

	// --- registration ---
	w := httptest.NewRecorder()
	a.require(a.handlePasskeyRegisterBegin)(w, req("POST", "/admin/api/passkey/register/begin", nil, host, tok))
	if w.Code != 200 {
		t.Fatalf("register/begin: %d %s", w.Code, w.Body)
	}
	var creation struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &creation); err != nil {
		t.Fatal(err)
	}
	if creation.PublicKey.RP.ID != host {
		t.Fatalf("rp.id = %q, want %q", creation.PublicKey.RP.ID, host)
	}

	auth := newSoftAuthenticator(t, host)
	origin := "https://" + host
	body := auth.makeCredential(creation.PublicKey.Challenge, origin)

	w = httptest.NewRecorder()
	a.require(a.handlePasskeyRegisterFinish)(w,
		req("POST", "/admin/api/passkey/register/finish?label=MacBook", body, host, tok))
	if w.Code != 200 {
		t.Fatalf("register/finish: %d %s", w.Code, w.Body)
	}
	if got := a.pk.count(); got != 1 {
		t.Fatalf("stored %d passkeys, want 1", got)
	}

	// Credentials must survive a restart, or a reboot would lock the user out
	// of the passkey path entirely.
	reloaded := loadPasskeyStore(a.pk.path)
	if len(reloaded.forRP(host)) != 1 {
		t.Fatal("passkey did not persist to disk")
	}
	if reloaded.Handle != a.pk.Handle {
		t.Fatal("user handle changed across reload; existing passkeys would break")
	}

	// --- login, with no session cookie at all ---
	w = httptest.NewRecorder()
	a.handlePasskeyLoginBegin(w, req("POST", "/admin/api/passkey/login/begin", nil, host, ""))
	if w.Code != 200 {
		t.Fatalf("login/begin: %d %s", w.Code, w.Body)
	}
	var assertion struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	json.Unmarshal(w.Body.Bytes(), &assertion)

	body = auth.getAssertion(assertion.PublicKey.Challenge, origin, a.pk.Handle)
	w = httptest.NewRecorder()
	a.handlePasskeyLoginFinish(w, req("POST", "/admin/api/passkey/login/finish", body, host, ""))
	if w.Code != 200 {
		t.Fatalf("login/finish: %d %s", w.Code, w.Body)
	}

	// A usable session cookie must come back.
	var newTok string
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			newTok = c.Value
		}
	}
	if newTok == "" {
		t.Fatal("no session cookie issued")
	}
	if !a.authed(req("GET", "/x", nil, host, newTok)) {
		t.Fatal("issued cookie does not authenticate")
	}

	// Usage counters should have been recorded.
	if k := a.pk.forRP(host)[0]; k.Uses != 1 || k.LastUsed.IsZero() {
		t.Errorf("usage not recorded: uses=%d lastUsed=%v", k.Uses, k.LastUsed)
	}
}

// A passkey registered for one hostname must not authenticate on another. This
// is the property that makes it safe to serve the same admin page on both the
// exe.xyz name and a custom domain.
func TestPasskeyIsScopedToDomain(t *testing.T) {
	const host = "images.huny.dev"
	a := newTestAdmin(t)
	tok := a.newSession()

	w := httptest.NewRecorder()
	a.require(a.handlePasskeyRegisterBegin)(w, req("POST", "/x", nil, host, tok))
	var creation struct {
		PublicKey struct{ Challenge string } `json:"publicKey"`
	}
	json.Unmarshal(w.Body.Bytes(), &creation)
	auth := newSoftAuthenticator(t, host)
	w = httptest.NewRecorder()
	a.require(a.handlePasskeyRegisterFinish)(w,
		req("POST", "/x?label=k", auth.makeCredential(creation.PublicKey.Challenge, "https://"+host), host, tok))
	if w.Code != 200 {
		t.Fatalf("setup failed: %s", w.Body)
	}

	// Same credential, different host: login must not even start.
	w = httptest.NewRecorder()
	a.handlePasskeyLoginBegin(w, req("POST", "/x", nil, "hunydev-images.exe.xyz", ""))
	if w.Code != 404 {
		t.Errorf("login/begin on other domain: %d, want 404", w.Code)
	}
	if n := len(a.pk.forRP("hunydev-images.exe.xyz")); n != 0 {
		t.Errorf("other domain sees %d keys, want 0", n)
	}
}

// A replayed or forged assertion must be rejected.
func TestPasskeyRejectsBadAssertion(t *testing.T) {
	const host = "images.huny.dev"
	origin := "https://" + host
	a := newTestAdmin(t)
	tok := a.newSession()

	w := httptest.NewRecorder()
	a.require(a.handlePasskeyRegisterBegin)(w, req("POST", "/x", nil, host, tok))
	var creation struct {
		PublicKey struct{ Challenge string } `json:"publicKey"`
	}
	json.Unmarshal(w.Body.Bytes(), &creation)
	auth := newSoftAuthenticator(t, host)
	w = httptest.NewRecorder()
	a.require(a.handlePasskeyRegisterFinish)(w,
		req("POST", "/x?label=k", auth.makeCredential(creation.PublicKey.Challenge, origin), host, tok))
	if w.Code != 200 {
		t.Fatalf("setup: %s", w.Body)
	}

	begin := func() string {
		w := httptest.NewRecorder()
		a.handlePasskeyLoginBegin(w, req("POST", "/x", nil, host, ""))
		var as struct {
			PublicKey struct{ Challenge string } `json:"publicKey"`
		}
		json.Unmarshal(w.Body.Bytes(), &as)
		return as.PublicKey.Challenge
	}

	t.Run("wrong challenge", func(t *testing.T) {
		begin()
		body := auth.getAssertion(b64u([]byte("not-the-challenge-value-000000")), origin, a.pk.Handle)
		w := httptest.NewRecorder()
		a.handlePasskeyLoginFinish(w, req("POST", "/x", body, host, ""))
		if w.Code == 200 {
			t.Error("forged challenge accepted")
		}
	})

	t.Run("wrong origin", func(t *testing.T) {
		ch := begin()
		body := auth.getAssertion(ch, "https://evil.example.com", a.pk.Handle)
		w := httptest.NewRecorder()
		a.handlePasskeyLoginFinish(w, req("POST", "/x", body, host, ""))
		if w.Code == 200 {
			t.Error("wrong origin accepted")
		}
	})

	t.Run("unknown user handle", func(t *testing.T) {
		ch := begin()
		body := auth.getAssertion(ch, origin, "some-other-handle")
		w := httptest.NewRecorder()
		a.handlePasskeyLoginFinish(w, req("POST", "/x", body, host, ""))
		if w.Code == 200 {
			t.Error("unknown user handle accepted")
		}
	})

	t.Run("signature from a different key", func(t *testing.T) {
		ch := begin()
		other := newSoftAuthenticator(t, host)
		other.credID = auth.credID // claim the registered credential ID
		body := other.getAssertion(ch, origin, a.pk.Handle)
		w := httptest.NewRecorder()
		a.handlePasskeyLoginFinish(w, req("POST", "/x", body, host, ""))
		if w.Code == 200 {
			t.Error("signature from an unrelated key accepted")
		}
	})

	t.Run("no ceremony in flight", func(t *testing.T) {
		body := auth.getAssertion(b64u([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")), origin, a.pk.Handle)
		w := httptest.NewRecorder()
		a.handlePasskeyLoginFinish(w, req("POST", "/x", body, host, ""))
		if w.Code != 400 {
			t.Errorf("got %d, want 400 for a missing login session", w.Code)
		}
	})
}

func TestPasskeyDeleteAndListScoping(t *testing.T) {
	a := newTestAdmin(t)
	tok := a.newSession()
	// Two keys on different hostnames.
	for _, host := range []string{"images.huny.dev", "hunydev-images.exe.xyz"} {
		w := httptest.NewRecorder()
		a.require(a.handlePasskeyRegisterBegin)(w, req("POST", "/x", nil, host, tok))
		var c struct {
			PublicKey struct{ Challenge string } `json:"publicKey"`
		}
		json.Unmarshal(w.Body.Bytes(), &c)
		auth := newSoftAuthenticator(t, host)
		w = httptest.NewRecorder()
		a.require(a.handlePasskeyRegisterFinish)(w,
			req("POST", "/x?label="+host, auth.makeCredential(c.PublicKey.Challenge, "https://"+host), host, tok))
		if w.Code != 200 {
			t.Fatalf("%s: %s", host, w.Body)
		}
	}

	// The list shows every key but flags which belong to the current domain,
	// so a key registered elsewhere is visible and removable rather than
	// becoming invisible cruft.
	w := httptest.NewRecorder()
	a.require(a.handlePasskeyList)(w, req("GET", "/x", nil, "images.huny.dev", tok))
	var list struct {
		Passkeys []struct {
			ID      string `json:"id"`
			RPID    string `json:"rpid"`
			Current bool   `json:"current_domain"`
		} `json:"passkeys"`
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Passkeys) != 2 {
		t.Fatalf("listed %d, want 2", len(list.Passkeys))
	}
	cur := 0
	for _, k := range list.Passkeys {
		if k.Current {
			cur++
		}
	}
	if cur != 1 {
		t.Errorf("%d flagged current, want 1", cur)
	}

	// Delete the one from the other domain.
	var target struct{ ID, RPID string }
	for _, k := range list.Passkeys {
		if !k.Current {
			target.ID, target.RPID = k.ID, k.RPID
		}
	}
	body, _ := json.Marshal(target)
	w = httptest.NewRecorder()
	a.require(a.handlePasskeyDelete)(w, req("POST", "/x", body, "images.huny.dev", tok))
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if a.pk.count() != 1 {
		t.Errorf("count = %d, want 1", a.pk.count())
	}

	// Deleting again must 404 rather than silently succeed.
	w = httptest.NewRecorder()
	a.require(a.handlePasskeyDelete)(w, req("POST", "/x", body, "images.huny.dev", tok))
	if w.Code != 404 {
		t.Errorf("second delete: %d, want 404", w.Code)
	}
}

func TestPasskeyEndpointsNeedSession(t *testing.T) {
	a := newTestAdmin(t)
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"register/begin", a.handlePasskeyRegisterBegin},
		{"register/finish", a.handlePasskeyRegisterFinish},
		{"list", a.handlePasskeyList},
		{"delete", a.handlePasskeyDelete},
	} {
		w := httptest.NewRecorder()
		a.require(tc.h)(w, req("POST", "/x", []byte("{}"), "images.huny.dev", ""))
		if w.Code != 401 {
			t.Errorf("%s: %d, want 401", tc.name, w.Code)
		}
	}
}

// asn1Marshal is split out so the ECDSA signature encoding is obvious.
func asn1Marshal(v any) ([]byte, error) { return asn1.Marshal(v) }

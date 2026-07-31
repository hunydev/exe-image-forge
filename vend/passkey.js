// Shared WebAuthn client helpers, used by both the vending page and the admin
// page. The browser API speaks ArrayBuffers while JSON speaks base64url, so
// every value crossing that boundary is converted explicitly.
const pkB64uToBuf = v => {
  const b = v.replace(/-/g, '+').replace(/_/g, '/');
  const pad = b + '='.repeat((4 - b.length % 4) % 4);
  return Uint8Array.from(atob(pad), c => c.charCodeAt(0)).buffer;
};
const pkBufToB64u = b => btoa(String.fromCharCode(...new Uint8Array(b)))
  .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

// pkSupported reports whether this browser can do WebAuthn at all. Note that
// WebAuthn requires a secure context, so this is false over plain http on a
// non-loopback host.
function pkSupported() {
  return !!(window.PublicKeyCredential && navigator.credentials && window.isSecureContext);
}

// pkError turns the browser's opaque failures into something a human can act on.
function pkError(e) {
  if (e && e.name === 'NotAllowedError') return 'The request was cancelled or timed out';
  if (e && e.name === 'InvalidStateError') return 'A passkey is already registered on this device';
  if (e && e.name === 'SecurityError') return 'Passkeys require a secure HTTPS context';
  return String((e && e.message) || e).trim();
}

async function pkPost(url, body) {
  const opt = { method: 'POST' };
  if (body !== undefined) {
    opt.headers = { 'content-type': 'application/json' };
    opt.body = JSON.stringify(body);
  }
  const r = await fetch(url, opt);
  if (!r.ok) throw new Error((await r.text()).trim());
  const t = await r.text();
  return t ? JSON.parse(t) : {};
}

// pkRegisterCeremony runs the full create() flow and returns the server reply.
async function pkRegisterCeremony(label) {
  const opts = await pkPost('/admin/api/passkey/register/begin');
  const pk = opts.publicKey;
  pk.challenge = pkB64uToBuf(pk.challenge);
  pk.user.id = pkB64uToBuf(pk.user.id);
  (pk.excludeCredentials || []).forEach(c => c.id = pkB64uToBuf(c.id));
  const cred = await navigator.credentials.create({ publicKey: pk });
  return pkPost('/admin/api/passkey/register/finish?label=' + encodeURIComponent(label), {
    id: cred.id, rawId: pkBufToB64u(cred.rawId), type: cred.type,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: pkBufToB64u(cred.response.clientDataJSON),
      attestationObject: pkBufToB64u(cred.response.attestationObject),
      transports: cred.response.getTransports ? cred.response.getTransports() : [],
    },
  });
}

// pkLoginCeremony runs the full get() flow. On success the server has set the
// session cookie, which both pages then rely on.
async function pkLoginCeremony() {
  const opts = await pkPost('/admin/api/passkey/login/begin');
  const pk = opts.publicKey;
  pk.challenge = pkB64uToBuf(pk.challenge);
  (pk.allowCredentials || []).forEach(c => c.id = pkB64uToBuf(c.id));
  const cred = await navigator.credentials.get({ publicKey: pk });
  return pkPost('/admin/api/passkey/login/finish', {
    id: cred.id, rawId: pkBufToB64u(cred.rawId), type: cred.type,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: pkBufToB64u(cred.response.clientDataJSON),
      authenticatorData: pkBufToB64u(cred.response.authenticatorData),
      signature: pkBufToB64u(cred.response.signature),
      userHandle: cred.response.userHandle ? pkBufToB64u(cred.response.userHandle) : null,
    },
  });
}

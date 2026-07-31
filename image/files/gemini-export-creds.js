// Gemini CLI encrypts its credentials into ~/.gemini/gemini-credentials.json
// with a key derived from scrypt(hostname + username). A baked image runs on a
// different hostname, so that file can never be decrypted there and the login
// silently does not carry over.
//
// This converts the encrypted store into a portable form, to be run on the
// host that performed the login:
//   - OAuth  -> ~/.gemini/oauth_creds.json (plaintext; what the CLI reads when
//               GEMINI_FORCE_ENCRYPTED_FILE_STORAGE is unset, which is default)
//   - ApiKey -> prints the key so the caller can bake it as GEMINI_API_KEY
//
// Prints a one-word kind on stdout (oauth|apikey) and details on stderr.
const fs = require('fs'), os = require('os'), crypto = require('crypto'), path = require('path');

const dir = process.argv[2] || path.join(os.homedir(), '.gemini');
const src = path.join(dir, 'gemini-credentials.json');
if (!fs.existsSync(src)) { console.error('no encrypted credential file'); process.exit(2); }

const key = crypto.scryptSync('gemini-cli-oauth',
  `${os.hostname()}-${os.userInfo().username}-gemini-cli`, 32);

let store;
try {
  const [ivHex, tagHex, dataHex] = fs.readFileSync(src, 'utf8').trim().split(':');
  const dec = crypto.createDecipheriv('aes-256-gcm', key, Buffer.from(ivHex, 'hex'));
  dec.setAuthTag(Buffer.from(tagHex, 'hex'));
  store = JSON.parse(dec.update(Buffer.from(dataHex, 'hex'), undefined, 'utf8') + dec.final('utf8'));
} catch (e) {
  console.error('cannot decrypt: this must run with the same hostname and user '
    + 'that logged in (' + os.hostname() + '/' + os.userInfo().username + '): ' + e.message);
  process.exit(3);
}

// The store is {service: {account: json-string}}; find the first real token.
let rec = null;
for (const svc of Object.values(store)) {
  for (const v of Object.values(svc || {})) {
    try { const p = typeof v === 'string' ? JSON.parse(v) : v; if (p && p.token) { rec = p; break; } } catch {}
  }
  if (rec) break;
}
if (!rec) { console.error('no token in credential store'); process.exit(4); }

const t = rec.token;
if ((t.tokenType || '').toLowerCase() === 'apikey') {
  console.error('found API key credential');
  process.stdout.write('apikey ' + t.accessToken + '\n');
} else {
  const creds = {
    access_token: t.accessToken,
    refresh_token: t.refreshToken,
    token_type: t.tokenType || 'Bearer',
    scope: t.scope,
    expiry_date: t.expiresAt,
  };
  if (!creds.refresh_token) console.error('warning: no refresh token; the baked login will expire');
  fs.writeFileSync(path.join(dir, 'oauth_creds.json'), JSON.stringify(creds, null, 2), { mode: 0o600 });
  console.error('wrote portable oauth_creds.json');
  process.stdout.write('oauth\n');
}

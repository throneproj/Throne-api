import { html, useState, useEffect, useRef } from '../ui.js';
import { api } from '../client.js';
import { useNow } from '../store.js';
import { begin } from '../session.js';
import { errorText } from '../errors.js';

export function LoginView() {
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [retryAt, setRetryAt] = useState(0);
  const now = useNow(1000, retryAt > Date.now());
  const wait = Math.max(0, Math.ceil((retryAt - now) / 1000));
  const input = useRef(null);

  useEffect(() => {
    if (input.current) input.current.focus();
  }, []);

  const submit = async e => {
    e.preventDefault();
    if (!password || busy || wait > 0) return;
    setBusy(true);
    setError('');
    try {
      const st = await api.post('auth/login', { password }, { quiet: true });
      setPassword('');
      begin(st);
      return;
    } catch (err) {
      if (err.code === 'bad_password') setError('Wrong password.');
      else if (err.code === 'rate_limited') {
        setRetryAt(Date.now() + (err.retryAfter || 60) * 1000);
        setError('Too many failed attempts.');
      } else setError(errorText(err));
    }
    setBusy(false);
    if (input.current) input.current.select();
  };

  return html`<main class="login" id="main">
    <form class="login-card" onSubmit=${submit}>
      <div class="login-brand">
        <img src="icon.svg" alt="" width="44" height="44" />
        <h1>Throne</h1>
      </div>
      <input type="text" name="username" autocomplete="username" value="throne" hidden readOnly />
      <div class="field">
        <label for="login-password">Password</label>
        <input id="login-password" ref=${input} class="input" type="password" name="password"
          autocomplete="current-password" value=${password} required
          onInput=${e => setPassword(e.currentTarget.value)} />
      </div>
      ${error && html`<p class="form-error" role="alert">${error}${wait > 0 ? ` Try again in ${wait} s.` : ''}</p>`}
      <button class="btn primary block" type="submit" disabled=${busy || wait > 0 || !password}>
        ${busy ? 'Signing in…' : 'Sign in'}
      </button>
      <p class="muted small"><code>thronectl web</code> printed the password when it first started. Change it with <code>thronectl web passwd</code>.</p>
    </form>
  </main>`;
}

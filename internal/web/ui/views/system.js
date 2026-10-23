import { html, useState } from '../ui.js';
import { api } from '../client.js';
import { useStore, useBlocked, toast, toastError, confirmDialog } from '../store.js';
import { logout, waitForRestart } from '../session.js';
import { errorText } from '../errors.js';
import { Icon } from '../components/icons.js';
import { CopyButton, Notice, Spinner } from '../components/common.js';

function yesNo(v) {
  if (v === null || v === undefined) return html`<span class="muted">Unknown yet</span>`;
  return v ? html`<span class="ok">Yes</span>` : html`<span class="bad">No</span>`;
}

function ThroneInfo() {
  const status = useStore(s => s.status);
  const adminDenied = useStore(s => s.adminDenied);
  if (!status) return html`<section class="card"><div class="card-head"><h2>Throne</h2></div><p class="muted">Not known yet.</p></section>`;
  const caps = status.capabilities || {};
  const inbound = status.inbound || {};
  return html`<section class="card" aria-labelledby="sys-throne">
    <div class="card-head"><h2 id="sys-throne">Throne</h2></div>
    <dl class="facts">
      <dt>Version</dt><dd>${status.version || '—'}</dd>
      <dt>Mode</dt><dd>${caps.headless ? 'Headless' : 'With a window'}</dd>
      <dt>Core</dt><dd>${status.core ? html`<span class="ok">Up</span>` : html`<span class="bad">Down</span>`}</dd>
      <dt>Can run Tun</dt><dd>${yesNo(caps.tun)}</dd>
      <dt>Kill switch works here</dt><dd>${yesNo(caps.kill_switch)}</dd>
      <dt>Mixed inbound</dt><dd class="mono">${inbound.enabled ? `${inbound.address}:${inbound.port}` : 'Off'}</dd>
      <dt>Admin rights</dt><dd>${adminDenied ? html`<span class="bad">No</span>` : html`<span class="ok">Yes</span>`}</dd>
    </dl>
  </section>`;
}

function GatewayInfo() {
  const gw = useStore(s => s.auth.gateway) || {};
  return html`<section class="card" aria-labelledby="sys-gateway">
    <div class="card-head"><h2 id="sys-gateway">Web UI server</h2></div>
    <dl class="facts">
      <dt>thronectl</dt><dd>${gw.version || '—'}</dd>
      <dt>Throne address</dt><dd class="mono">${gw.throne || '—'}</dd>
      <dt>HTTPS</dt><dd>${gw.tls ? 'On' : html`<span class="warn">Off</span>`}</dd>
      ${gw.fingerprint && html`<dt>Certificate SHA-256</dt>
        <dd><div class="fingerprint"><code class="mono">${gw.fingerprint}</code><${CopyButton} text=${gw.fingerprint} label="Copy the fingerprint" /></div>
          <small class="muted">Compare it with the one thronectl web printed at its start before you trust the certificate.</small></dd>`}
    </dl>
  </section>`;
}

function PasswordCard() {
  const passwordFile = useStore(s => !!(s.auth.gateway && s.auth.gateway.password_file));
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [again, setAgain] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const mismatch = again !== '' && next !== again;
  const tooShort = next !== '' && next.length < 8;
  const ready = current && next.length >= 8 && next === again && !busy;

  const submit = async e => {
    e.preventDefault();
    if (!ready) return;
    setBusy(true);
    setError('');
    try {
      await api.post('auth/password', { current, new: next });
      setCurrent('');
      setNext('');
      setAgain('');
      toast('Password changed. Other sessions were signed out.', { kind: 'success' });
    } catch (err) {
      setError(err.code === 'bad_password' ? 'The current password is wrong.' : errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return html`<section class="card" aria-labelledby="sys-password">
    <div class="card-head"><h2 id="sys-password">Password</h2></div>
    ${passwordFile
      ? html`<p class="muted">The password comes from a password file (<code>--password-file</code>). Change it there and restart thronectl web.</p>`
      : html`<form onSubmit=${submit}>
        <input type="text" name="username" autocomplete="username" value="throne" hidden readOnly />
        <div class="field">
          <label for="pw-current">Current password</label>
          <input id="pw-current" class="input" type="password" autocomplete="current-password" value=${current}
            onInput=${e => setCurrent(e.currentTarget.value)} />
        </div>
        <div class="field">
          <label for="pw-new">New password</label>
          <input id="pw-new" class="input" type="password" autocomplete="new-password" value=${next} minLength="8"
            aria-describedby="pw-new-help" onInput=${e => setNext(e.currentTarget.value)} />
          <small id="pw-new-help" class=${tooShort ? 'field-error' : 'field-help'}>At least 8 characters.</small>
        </div>
        <div class="field">
          <label for="pw-again">New password again</label>
          <input id="pw-again" class="input" type="password" autocomplete="new-password" value=${again}
            onInput=${e => setAgain(e.currentTarget.value)} />
          ${mismatch && html`<small class="field-error">The passwords differ.</small>`}
        </div>
        ${error && html`<p class="form-error" role="alert">${error}</p>`}
        <div class="actions">
          <button type="submit" class="btn primary" disabled=${!ready}>${busy ? html`<${Spinner} small /> Changing…` : 'Change password'}</button>
        </div>
      </form>`}
  </section>`;
}

function ProgramCard() {
  const adminDenied = useStore(s => s.adminDenied);
  const busy = useStore(s => s.busyAction);
  const blocked = useBlocked();
  const [working, setWorking] = useState(false);
  const disabled = blocked || adminDenied || working || !!busy;

  const restart = async () => {
    const ok = await confirmDialog({
      title: 'Restart Throne',
      message: 'Throne restarts and the connection drops for a moment. A headless Throne under a service manager exits and is started again by it.',
      confirmLabel: 'Restart',
    });
    if (!ok) return;
    setWorking(true);
    try {
      const r = await api.post('api/v1/restart');
      waitForRestart(r.resume ? 'Throne is restarting and resumes the running profile…' : 'Throne is restarting…');
    } catch (e) {
      toastError(e, 'Restart failed');
    } finally {
      setWorking(false);
    }
  };

  const shutdown = async () => {
    const ok = await confirmDialog({
      title: 'Shut down Throne',
      message: 'Throne exits and the connection through it ends. The web UI cannot start it again: its service manager or container restart policy may, or you start it yourself.',
      confirmLabel: 'Shut down',
      danger: true,
    });
    if (!ok) return;
    setWorking(true);
    try {
      await api.post('api/v1/shutdown');
      toast('Throne is shutting down.');
    } catch (e) {
      toastError(e, 'Shut down failed');
    } finally {
      setWorking(false);
    }
  };

  return html`<section class="card" aria-labelledby="sys-program">
    <div class="card-head"><h2 id="sys-program">Program</h2></div>
    ${adminDenied && html`<${Notice} kind="info" icon="shield">Restart and shut down need admin rights on Throne.<//>`}
    <div class="actions">
      <button type="button" class="btn" disabled=${disabled} onClick=${restart}><${Icon} name="restart" />Restart Throne</button>
      <button type="button" class="btn danger" disabled=${disabled} onClick=${shutdown}><${Icon} name="power" />Shut down Throne</button>
    </div>
  </section>`;
}

function SessionCard() {
  return html`<section class="card" aria-labelledby="sys-session">
    <div class="card-head"><h2 id="sys-session">Session</h2></div>
    <p class="muted small">Sessions end after 12 hours without use, and after 7 days at most.</p>
    <div class="actions">
      <button type="button" class="btn" onClick=${() => logout()}><${Icon} name="logout" />Sign out</button>
    </div>
  </section>`;
}

export function SystemView() {
  return html`<div class="view">
    <h1 class="view-title">System</h1>
    <div class="grid">
      <${ThroneInfo} />
      <${GatewayInfo} />
      <${PasswordCard} />
      <${ProgramCard} />
      <${SessionCard} />
    </div>
  </div>`;
}

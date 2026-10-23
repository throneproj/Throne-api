import { html } from '../ui.js';
import { useStore, useBlocked, useNow } from '../store.js';
import { actions, dismissAdminNotice, dismissRestartWait } from '../session.js';
import { STATE_LABELS, DEGRADED, KILL_SWITCH_ERRORS, ADMIN_HELP } from '../errors.js';
import { formatRate, formatDuration } from '../format.js';
import { Icon } from './icons.js';
import { Spinner } from './common.js';

export const NAV_ITEMS = [
  { id: 'status', label: 'Status', icon: 'activity' },
  { id: 'profiles', label: 'Profiles', icon: 'list' },
  { id: 'traffic', label: 'Traffic', icon: 'chart' },
  { id: 'logs', label: 'Logs', icon: 'terminal' },
  { id: 'settings', label: 'Settings', icon: 'sliders' },
  { id: 'backup', label: 'Backup', icon: 'archive' },
  { id: 'system', label: 'System', icon: 'server' },
];

function ConnectButton() {
  const status = useStore(s => s.status);
  const busy = useStore(s => s.busyAction);
  const blocked = useBlocked();
  if (!status) {
    return html`<button type="button" class="btn" disabled><${Icon} name="play" /><span class="hide-xs">Connect</span></button>`;
  }
  const st = status.state;
  if (st === 'connecting' || st === 'stopping' || busy === 'connect' || busy === 'disconnect' || busy === 'restart') {
    const label = st === 'stopping' || busy === 'disconnect' ? 'Stopping…' : 'Connecting…';
    return html`<button type="button" class="btn" disabled aria-label=${label}><${Spinner} small /><span class="hide-xs">${label}</span></button>`;
  }
  if (st === 'running') {
    return html`<button type="button" class="btn" disabled=${blocked || !!busy} onClick=${() => actions.disconnect()}
      aria-label="Disconnect" title="Stop the running profile"><${Icon} name="stop" /><span class="hide-xs">Disconnect</span></button>`;
  }
  const none = status.last_profile_id < 0;
  return html`<button type="button" class="btn primary" disabled=${blocked || !!busy || none}
    onClick=${() => actions.connectLast()} aria-label="Connect"
    title=${none ? 'Choose a profile in Profiles first' : 'Start the last profile'}>
    <${Icon} name="play" /><span class="hide-xs">Connect</span></button>`;
}

export function Header() {
  const status = useStore(s => s.status);
  const rate = useStore(s => s.rates[s.rates.length - 1]);
  const state = status ? status.state : 'unknown';
  const running = state === 'running';
  return html`<header class="topbar">
    <a class="brand" href="#/status" aria-label="Throne: status">
      <img src="icon.svg" alt="" width="26" height="26" />
      <span class="brand-name">Throne</span>
    </a>
    <div class="conn" aria-live="polite">
      <span class=${`state-dot ${state}`} aria-hidden="true"></span>
      <div class="conn-text">
        <span class="conn-state">${status ? STATE_LABELS[state] || state : 'Loading…'}
          ${status && status.tun && html` <span class="tag">Tun</span>`}
          ${status && status.kill_switch && status.kill_switch !== 'off' && html` <span class=${`tag ks-${status.kill_switch}`} title=${`Kill switch: ${status.kill_switch}`}>KS</span>`}
        </span>
        <span class="conn-profile">${status && status.profile ? status.profile.name : ''}</span>
      </div>
    </div>
    ${running && html`<div class="rates" title="Proxy traffic per second">
      <span><span class="arrow" aria-hidden="true">↓</span><span class="sr-only">Download </span>${formatRate(rate ? rate.pd : 0)}</span>
      <span><span class="arrow" aria-hidden="true">↑</span><span class="sr-only">Upload </span>${formatRate(rate ? rate.pu : 0)}</span>
    </div>`}
    <${ConnectButton} />
  </header>`;
}

export function Nav({ route }) {
  return html`<nav class="nav" aria-label="Main">
    <ul>
      ${NAV_ITEMS.map(it => html`<li key=${it.id}>
        <a href=${`#/${it.id}`} class="nav-link" aria-current=${route === it.id ? 'page' : undefined}>
          <${Icon} name=${it.icon} size=${20} />
          <span>${it.label}</span>
        </a>
      </li>`)}
    </ul>
  </nav>`;
}

function Banner({ kind, icon = 'alert', children, action, onDismiss }) {
  return html`<div class=${`banner ${kind}`} role=${kind === 'bad' ? 'alert' : 'status'}>
    <${Icon} name=${icon} />
    <div class="banner-body">${children}</div>
    ${action}
    ${onDismiss && html`<button type="button" class="icon-btn small" aria-label="Dismiss" onClick=${onDismiss}><${Icon} name="x" size=${16} /></button>`}
  </div>`;
}

const UNAVAILABLE_TEXT = {
  restarting: 'Throne is restarting…',
  starting: 'Throne is starting…',
  exiting: 'Throne is shutting down…',
};

export const THRONE_DOWN_TEXT = {
  throne_unreachable: 'Throne is not reachable. Retrying…',
  throne_timeout: 'Throne does not answer. Retrying…',
  throne_rejected: 'Throne refuses the web UI: the API key is probably wrong, or Throne blocks this address for a minute after failed attempts.',
};

export function Banners() {
  const gatewayLost = useStore(s => s.gatewayLost);
  const throne = useStore(s => s.throne);
  const unavailable = useStore(s => s.unavailable);
  const status = useStore(s => s.status);
  const adminDenied = useStore(s => s.adminDenied);
  const adminDismissed = useStore(s => s.adminNoticeDismissed);
  const busy = useStore(s => s.busyAction);
  const blocked = useBlocked();
  const out = [];
  if (gatewayLost) {
    out.push(html`<${Banner} key="gw" kind="bad">Lost the connection to the web UI server. Reconnecting…<//>`);
  } else if (!throne.reachable) {
    const transient = !!UNAVAILABLE_TEXT[throne.code];
    out.push(html`<${Banner} key="throne" kind=${transient ? 'warn' : 'bad'} icon=${transient ? 'refresh' : 'alert'}>
      <strong>${THRONE_DOWN_TEXT[throne.code] || UNAVAILABLE_TEXT[throne.code] || THRONE_DOWN_TEXT.throne_unreachable}</strong>
      ${throne.message && html`<span>${throne.message}</span>`}
    <//>`);
  }
  if (unavailable && throne.reachable && !gatewayLost) {
    out.push(html`<${Banner} key="unavail" kind="warn" icon="refresh">
      <strong>${UNAVAILABLE_TEXT[unavailable.code] || 'Throne is not ready.'}</strong>
      <span>The page carries on once Throne answers again.</span>
    <//>`);
  }
  if (status && throne.reachable && !unavailable && !gatewayLost) {
    if (status.degraded) {
      const ks = status.degraded === 'kill_switch_failed' && status.kill_switch_error
        ? KILL_SWITCH_ERRORS[status.kill_switch_error] || status.kill_switch_error : '';
      const tunAction = status.degraded === 'tun_unavailable'
        ? html`<button type="button" class="btn small" disabled=${blocked || !!busy} onClick=${() => actions.setModes({ tun: false })}>Turn Tun off</button>`
        : null;
      out.push(html`<${Banner} key="degraded" kind="bad" action=${tunAction}>
        <strong>${DEGRADED[status.degraded] || `Throne is degraded (${status.degraded}).`}</strong>
        ${ks && html`<span>${ks}</span>`}
      <//>`);
    }
    if (status.restart_needed && status.state === 'running') {
      out.push(html`<${Banner} key="restart" kind="info" icon="restart"
        action=${html`<button type="button" class="btn small primary" disabled=${blocked || !!busy} onClick=${() => actions.restartProfile()}>Restart profile</button>`}>
        Some changes wait for the running profile to restart.
      <//>`);
    }
  }
  if (adminDenied && !adminDismissed) {
    out.push(html`<${Banner} key="admin" kind="info" icon="shield" onDismiss=${dismissAdminNotice}>${ADMIN_HELP}<//>`);
  }
  if (!out.length) return null;
  return html`<div class="banners">${out}</div>`;
}

export function RestartOverlay() {
  const wait = useStore(s => s.restartWait);
  const now = useNow(1000, !!wait);
  if (!wait) return null;
  const elapsed = Math.max(0, (now - wait.started) / 1000);
  const slow = elapsed > 180;
  return html`<div class="overlay" role="alertdialog" aria-modal="true" aria-labelledby="restart-title" aria-describedby="restart-desc">
    <div class="overlay-card">
      <${Spinner} />
      <h2 id="restart-title">${wait.title}</h2>
      <p id="restart-desc" class="muted">The page carries on when Throne answers again. ${formatDuration(elapsed)}</p>
      ${slow && html`<p class="small">This takes longer than expected. A headless Throne that exits to restart needs its service manager or container to start it again.</p>`}
      ${slow && html`<button type="button" class="btn" onClick=${dismissRestartWait}>Stop waiting</button>`}
    </div>
  </div>`;
}

import { html, render, useEffect, useRef } from './ui.js';
import { useStore, useRoute } from './store.js';
import { boot } from './session.js';
import { errorText } from './errors.js';
import { Header, Nav, Banners, RestartOverlay, NAV_ITEMS } from './components/shell.js';
import { Toasts, ConfirmHost, Spinner } from './components/common.js';
import { LoginView } from './views/login.js';
import { StatusView } from './views/status.js';
import { ProfilesView } from './views/profiles.js';
import { TrafficView } from './views/traffic.js';
import { LogsView } from './views/logs.js';
import { SettingsView } from './views/settings.js';
import { BackupView } from './views/backup.js';
import { SystemView } from './views/system.js';

const VIEWS = {
  status: StatusView,
  profiles: ProfilesView,
  traffic: TrafficView,
  logs: LogsView,
  settings: SettingsView,
  backup: BackupView,
  system: SystemView,
};

function Shell() {
  const route = useRoute();
  const main = useRef(null);
  const first = useRef(true);
  const View = VIEWS[route] || StatusView;
  useEffect(() => {
    const item = NAV_ITEMS.find(it => it.id === route);
    document.title = item ? `${item.label} · Throne` : 'Throne';
    if (first.current) {
      first.current = false;
      return;
    }
    window.scrollTo(0, 0);
    if (main.current) main.current.focus({ preventScroll: true });
  }, [route]);
  return html`<div class="shell">
    <a class="skip-link" href="#main" onClick=${e => { e.preventDefault(); if (main.current) main.current.focus(); }}>Skip to content</a>
    <${Header} />
    <${Nav} route=${route} />
    <main class="main" id="main" ref=${main} tabindex="-1">
      <${Banners} />
      <${View} key=${route} />
    </main>
    <${RestartOverlay} />
  </div>`;
}

function Offline() {
  const err = useStore(s => s.bootError);
  return html`<main class="login" id="main">
    <div class="login-card">
      <div class="login-brand"><img src="icon.svg" alt="" width="44" height="44" /><h1>Throne</h1></div>
      <p role="alert">${err ? errorText(err) : 'Cannot reach the web UI server.'}</p>
      <button type="button" class="btn primary block" onClick=${() => boot()}>Try again</button>
    </div>
  </main>`;
}

function App() {
  const phase = useStore(s => s.boot);
  const authenticated = useStore(s => s.auth.authenticated);
  let body;
  if (phase === 'loading') body = html`<div class="splash"><${Spinner} label="Loading" /></div>`;
  else if (phase === 'offline') body = html`<${Offline} />`;
  else body = authenticated ? html`<${Shell} />` : html`<${LoginView} />`;
  return html`${body}<${Toasts} /><${ConfirmHost} />`;
}

const root = document.getElementById('app');
root.textContent = '';
render(html`<${App} />`, root);
boot();

import { html } from '../ui.js';
import { api } from '../client.js';
import { useStore, useBlocked, useResource, useNow, toast, toastError } from '../store.js';
import { actions } from '../session.js';
import { STATE_LABELS, KILL_SWITCH_ERRORS } from '../errors.js';
import { formatDuration, formatDate, plural, timeAgo } from '../format.js';
import { Icon } from '../components/icons.js';
import { Loading, ErrorBox, Spinner } from '../components/common.js';
import { ProfileSummary } from '../components/profile-bits.js';
import { RateChart, ChartLegend } from '../components/chart.js';

const KIND_LABELS = { latency: 'Latency test', ip: 'IP test', speed: 'Speed test' };
const JOB_STATES = { running: 'Running', done: 'Done', cancelled: 'Cancelled' };
const KILL_SWITCH_STATES = { off: 'Off', arming: 'Arming…', armed: 'Armed', failed: 'Failed' };

export const RATE_SERIES = [
  { key: 'pd', label: 'Proxy download', cls: 's-down', area: true },
  { key: 'pu', label: 'Proxy upload', cls: 's-up' },
  { key: 'dd', label: 'Direct download', cls: 's-direct' },
];

function ConnectionCard({ status }) {
  const busy = useStore(s => s.busyAction);
  const blocked = useBlocked();
  const idle = status.state === 'idle';
  const lastId = status.last_profile_id;
  const last = useResource(
    () => (idle && lastId >= 0 ? api.get(`api/v1/profiles/${lastId}`, undefined, { quiet: true }) : Promise.resolve(null)),
    { deps: [idle, lastId], topics: ['profiles'], throttle: 3000 },
  );
  const profile = status.profile || (idle ? last.data : null);
  const transitional = status.state === 'connecting' || status.state === 'stopping';
  const disabled = blocked || !!busy;
  return html`<section class="card" aria-labelledby="conn-title">
    <div class="card-head">
      <h2 id="conn-title">Connection</h2>
      <span class=${`pill ${status.state}`}>${STATE_LABELS[status.state] || status.state}</span>
    </div>
    ${profile
      ? html`<${ProfileSummary} p=${profile} />`
      : html`<p class="muted">No profile has run yet. Pick one in <a href="#/profiles">Profiles</a>.</p>`}
    ${idle && profile && html`<p class="muted small">Connect starts this profile again.</p>`}
    <div class="actions">
      ${status.state === 'running' && html`
        <button type="button" class="btn" disabled=${disabled} onClick=${() => actions.disconnect()}><${Icon} name="stop" />Disconnect</button>
        <button type="button" class="btn" disabled=${disabled} onClick=${() => actions.restartProfile()}><${Icon} name="restart" />Restart</button>`}
      ${idle && html`<button type="button" class="btn primary" disabled=${disabled || lastId < 0} onClick=${() => actions.connectLast()}><${Icon} name="play" />Connect</button>`}
      ${(transitional || busy === 'connect' || busy === 'restart' || busy === 'disconnect') && html`<span class="muted"><${Spinner} small /> Working…</span>`}
    </div>
  </section>`;
}

function ModesCard({ status }) {
  const busy = useStore(s => s.busyAction);
  const blocked = useBlocked();
  const routes = useResource(() => api.get('api/v1/routes'), { topics: ['status'], throttle: 1500 });
  const caps = status.capabilities || {};
  const disabled = blocked || !!busy;
  const list = (routes.data && routes.data.routes) || [];
  const activeId = status.route ? status.route.id : '';
  let tunHint = '';
  if (caps.tun === false) tunHint = 'Throne lacks the privileges Tun needs, so turning it on fails.';
  else if (caps.tun === null || caps.tun === undefined) tunHint = 'Throne does not know yet whether it can run Tun.';
  return html`<section class="card" aria-labelledby="modes-title">
    <div class="card-head"><h2 id="modes-title">Modes and routing</h2>${busy === 'modes' || busy === 'route' ? html`<${Spinner} small label="Applying" />` : null}</div>
    <div class="switch-row">
      <label for="mode-tun">
        <span>Tun mode</span>
        <small>Sends all traffic of Throne's device through Throne. Changing it restarts a running profile.</small>
      </label>
      <input id="mode-tun" type="checkbox" role="switch" class="toggle" checked=${!!status.tun} disabled=${disabled}
        onChange=${e => actions.setModes({ tun: e.currentTarget.checked })} />
    </div>
    ${status.tun_conflict && html`<p class="note warn">${status.tun_conflict}</p>`}
    ${tunHint && !status.tun && html`<p class="muted small">${tunHint}</p>`}
    ${!caps.headless && html`<div class="switch-row">
      <label for="mode-sysproxy">
        <span>System proxy</span>
        <small>Sets Throne's mixed inbound as the system proxy of Throne's device.</small>
      </label>
      <input id="mode-sysproxy" type="checkbox" role="switch" class="toggle" checked=${!!status.system_proxy}
        disabled=${disabled} onChange=${e => actions.setModes({ system_proxy: e.currentTarget.checked })} />
    </div>`}
    <div class="field">
      <label for="route-select">Routing profile</label>
      <select id="route-select" class="input" value=${String(activeId)} disabled=${disabled || !list.length}
        onChange=${e => actions.setRoute(Number(e.currentTarget.value))}>
        ${!status.route && html`<option value="">(none)</option>`}
        ${list.map(r => html`<option key=${r.id} value=${String(r.id)}>${r.name}</option>`)}
      </select>
      ${routes.error && html`<${ErrorBox} error=${routes.error} compact onRetry=${routes.reload} />`}
    </div>
  </section>`;
}

function ThroneCard({ status }) {
  const caps = status.capabilities || {};
  const ksError = status.kill_switch === 'failed' && status.kill_switch_error
    ? KILL_SWITCH_ERRORS[status.kill_switch_error] || status.kill_switch_error : '';
  const inbound = status.inbound || {};
  return html`<section class="card" aria-labelledby="throne-title">
    <div class="card-head"><h2 id="throne-title">Throne</h2><span class="muted small">${status.version}</span></div>
    <dl class="facts">
      <dt>Core</dt><dd>${status.core ? html`<span class="ok">Up</span>` : html`<span class="bad">Down</span>`}</dd>
      <dt>Kill switch</dt>
      <dd>
        <span class=${`ks ks-${status.kill_switch}`}>${KILL_SWITCH_STATES[status.kill_switch] || status.kill_switch}</span>
        ${ksError && html`<div class="small">${ksError}</div>`}
        ${!caps.kill_switch && status.kill_switch === 'off' && html`<div class="muted small">Not available on this device.</div>`}
      </dd>
      <dt>Mixed inbound</dt>
      <dd class="mono">${inbound.enabled ? `${inbound.address}:${inbound.port}` : html`<span class="muted">Off</span>`}</dd>
      <dt>Mode</dt><dd>${caps.headless ? 'Headless' : 'With a window'}</dd>
    </dl>
    <p class="small"><a href="#/settings">Settings</a> · <a href="#/system">System</a></p>
  </section>`;
}

function RatesCard({ running }) {
  const rates = useStore(s => s.rates);
  const last = rates[rates.length - 1];
  return html`<section class="card" aria-labelledby="rates-title">
    <div class="card-head"><h2 id="rates-title">Live traffic</h2><a class="small" href="#/traffic">Details</a></div>
    ${running
      ? html`<${RateChart} samples=${rates} series=${RATE_SERIES} height=${90} />
        <${ChartLegend} series=${RATE_SERIES} sample=${last} />`
      : html`<p class="muted">No profile is running.</p>`}
  </section>`;
}

export function TestsCard() {
  const tests = useResource(() => api.get('api/v1/tests'), { topics: ['tests'] });
  const blocked = useBlocked();
  const jobs = (tests.data && tests.data.jobs) || [];
  const anyRunning = jobs.some(j => j.state === 'running');
  const now = useNow(1000, anyRunning);
  const cancel = async job => {
    try {
      const r = await api.del(`api/v1/tests/${job.id}`);
      toast(r.state === 'cancelled' ? 'The test was cancelled.' : 'The test is stopping.');
      tests.reload();
    } catch (e) {
      toastError(e, 'Cancel failed');
    }
  };
  return html`<section class="card" aria-labelledby="tests-title">
    <div class="card-head"><h2 id="tests-title">Tests</h2>
      <button type="button" class="icon-btn small" aria-label="Refresh tests" onClick=${tests.reload}><${Icon} name="refresh" size=${16} /></button>
    </div>
    ${tests.error && html`<${ErrorBox} error=${tests.error} compact onRetry=${tests.reload} />`}
    ${!tests.error && tests.loading && tests.data === undefined && html`<${Loading} />`}
    ${tests.data && !jobs.length && html`<p class="muted">No tests yet. Start them from Profiles.</p>`}
    ${jobs.length > 0 && html`<ul class="job-list">
      ${jobs.slice(0, 10).map(j => {
        const end = j.state === 'running' ? now / 1000 : j.finished_at || j.started_at;
        return html`<li key=${j.id} class=${`job ${j.state}`}>
          <div>
            <strong>${KIND_LABELS[j.kind] || j.kind}</strong>
            <span class="muted"> · ${plural((j.profile_ids || []).length, 'profile')} · ${JOB_STATES[j.state] || j.state}</span>
            <div class="muted small" title=${formatDate(j.started_at)}>${timeAgo(j.started_at, now)} · ${formatDuration(end - j.started_at)}</div>
          </div>
          ${j.state === 'running' && html`<button type="button" class="btn small" disabled=${blocked} onClick=${() => cancel(j)}>Cancel</button>`}
        </li>`;
      })}
    </ul>`}
  </section>`;
}

export function StatusView() {
  const status = useStore(s => s.status);
  const statusError = useStore(s => s.statusError);
  if (!status) {
    return html`<div class="view"><h1 class="view-title">Status</h1>
      ${statusError ? html`<${ErrorBox} error=${statusError} />` : html`<${Loading} />`}</div>`;
  }
  return html`<div class="view">
    <h1 class="view-title">Status</h1>
    <div class="grid">
      <${ConnectionCard} status=${status} />
      <${ModesCard} status=${status} />
      <${RatesCard} running=${status.state === 'running'} />
      <${ThroneCard} status=${status} />
      <${TestsCard} />
    </div>
  </div>`;
}

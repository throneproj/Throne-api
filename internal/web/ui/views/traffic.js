import { html, useState } from '../ui.js';
import { api } from '../client.js';
import { useStore, useResource } from '../store.js';
import { formatBytes, formatDate } from '../format.js';
import { Icon } from '../components/icons.js';
import { Loading, ErrorBox, Notice } from '../components/common.js';
import { RateChart, ChartLegend } from '../components/chart.js';
import { RATE_SERIES } from './status.js';

const LIVE_SERIES = [...RATE_SERIES, { key: 'du', label: 'Direct upload', cls: 's-direct-up' }];
const RANGES = [
  [86400, '24 hours'],
  [7 * 86400, '7 days'],
  [30 * 86400, '30 days'],
];
const ROWS = 100;

function Totals() {
  const [by, setBy] = useState('profile');
  const [range, setRange] = useState(86400);
  const [rows, setRows] = useState(ROWS);
  const res = useResource(() => {
    const to = Math.floor(Date.now() / 1000);
    return api.get('api/v1/stats/traffic', { by, from: to - range, to });
  }, { deps: [by, range] });
  const items = (res.data && res.data.items) || [];
  const max = items.reduce((m, it) => Math.max(m, (it.up || 0) + (it.down || 0)), 0) || 1;
  const sumUp = items.reduce((s, it) => s + (it.up || 0), 0);
  const sumDown = items.reduce((s, it) => s + (it.down || 0), 0);
  const disabled = res.error && res.error.code === 'stats_disabled';
  return html`<section class="card" aria-labelledby="totals-title">
    <div class="card-head">
      <h2 id="totals-title">Totals</h2>
      <button type="button" class="icon-btn small" aria-label="Refresh totals" onClick=${res.reload}><${Icon} name="refresh" size=${16} /></button>
    </div>
    <div class="toolbar">
      <div class="segmented" role="radiogroup" aria-label="Group by">
        ${[['profile', 'By profile'], ['app', 'By app']].map(([id, label]) => html`<label key=${id} class=${by === id ? 'seg active' : 'seg'}>
          <input type="radio" name="traffic-by" value=${id} checked=${by === id} onChange=${() => { setBy(id); setRows(ROWS); }} />${label}
        </label>`)}
      </div>
      <div class="segmented" role="radiogroup" aria-label="Period">
        ${RANGES.map(([sec, label]) => html`<label key=${sec} class=${range === sec ? 'seg active' : 'seg'}>
          <input type="radio" name="traffic-range" value=${String(sec)} checked=${range === sec} onChange=${() => { setRange(sec); setRows(ROWS); }} />${label}
        </label>`)}
      </div>
    </div>
    ${disabled && html`<${Notice} kind="info">Traffic statistics are turned off in Throne. Turn them on in Throne's settings to see totals here.<//>`}
    ${res.error && !disabled && html`<${ErrorBox} error=${res.error} onRetry=${res.reload} />`}
    ${!res.error && res.data === undefined && html`<${Loading} />`}
    ${res.data && !res.error && html`
      <p class="muted small">${formatDate(res.data.from)} – ${formatDate(res.data.to)} · ↑ ${formatBytes(sumUp)} · ↓ ${formatBytes(sumDown)}</p>
      ${!items.length && html`<p class="empty">No traffic in this period.</p>`}
      ${items.length > 0 && html`<div class="table-wrap"><table class="ttable">
        <thead><tr><th>${by === 'app' ? 'App' : 'Profile'}</th><th class="num">Upload</th><th class="num">Download</th><th class="num">Total</th></tr></thead>
        <tbody>
          ${items.slice(0, rows).map((it, i) => {
            const total = (it.up || 0) + (it.down || 0);
            return html`<tr key=${`${it.id ?? ''}-${it.name}-${i}`}>
              <td>
                <div class="ellipsis">${it.name || '(unknown)'}</div>
                <div class="bar" aria-hidden="true"><span style=${{ width: `${Math.max(1, (total / max) * 100)}%` }}></span></div>
              </td>
              <td class="num">${formatBytes(it.up)}</td>
              <td class="num">${formatBytes(it.down)}</td>
              <td class="num"><strong>${formatBytes(total)}</strong></td>
            </tr>`;
          })}
        </tbody>
      </table></div>`}
      ${items.length > rows && html`<button type="button" class="btn block more" onClick=${() => setRows(r => r + ROWS)}>Show more (${items.length - rows} more)</button>`}
    `}
  </section>`;
}

export function TrafficView() {
  const rates = useStore(s => s.rates);
  const running = useStore(s => !!(s.status && s.status.state === 'running'));
  const last = rates[rates.length - 1];
  const peak = rates.reduce((m, r) => Math.max(m, r.pd), 0);
  return html`<div class="view">
    <h1 class="view-title">Traffic</h1>
    <section class="card" aria-labelledby="live-title">
      <div class="card-head"><h2 id="live-title">Live</h2><span class="muted small">Peak download ${formatBytes(peak)}/s</span></div>
      ${!running && html`<p class="muted">No profile is running, so there is no traffic through Throne.</p>`}
      <${RateChart} samples=${rates} series=${LIVE_SERIES} height=${160} label="Traffic rate over the last two minutes" />
      <${ChartLegend} series=${LIVE_SERIES} sample=${last} />
      <p class="muted small">Updated every 2 seconds while this page is visible.</p>
    </section>
    <${Totals} />
  </div>`;
}

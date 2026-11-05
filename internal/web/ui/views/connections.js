import { html, useState, useEffect, useMemo, useRef } from '../ui.js';
import { api } from '../client.js';
import { useResource, getState, toast, toastError, confirmDialog, storageGet, storageSet } from '../store.js';
import { ADMIN_HELP } from '../errors.js';
import { formatBytes, formatRate, formatDuration, plural } from '../format.js';
import { Icon } from '../components/icons.js';
import { Loading, ErrorBox, Notice } from '../components/common.js';

const PAGE = 200;
const NO_ROWS = [];
// Errors that polling cannot fix: stop until the user tries again.
const STOPPED = new Set(['forbidden', 'not_found', 'method_not_allowed']);
const SEARCHED = ['process', 'process_path', 'source', 'source_name', 'destination', 'domain', 'outbound', 'rule', 'network', 'protocol'];
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

// numeric columns sort largest first on the first click.
const COLUMNS = [
  { key: 'source', label: 'Process or source', value: c => c.process || c.source_name || c.source },
  { key: 'destination', label: 'Destination', value: c => c.domain || c.destination },
  { key: 'network', label: 'Network', value: c => `${c.network} ${c.protocol}` },
  { key: 'outbound', label: 'Outbound', value: c => c.outbound },
  { key: 'rule', label: 'Rule', value: c => c.rule },
  { key: 'speed', label: 'Speed', value: c => c.download_speed + c.upload_speed, numeric: true },
  { key: 'total', label: 'Total', value: c => c.download + c.upload, numeric: true },
  { key: 'age', label: 'Age', value: c => -c.created_at, numeric: true },
];

function matches(c, q) {
  return SEARCHED.some(f => (c[f] || '').toLowerCase().includes(q));
}

function sortRows(rows, sort) {
  const col = COLUMNS.find(c => c.key === sort.key);
  if (!col) return rows;
  const dir = sort.desc ? -1 : 1;
  const cmp = col.numeric
    ? (a, b) => col.value(a) - col.value(b)
    : (a, b) => collator.compare(col.value(a) || '', col.value(b) || '');
  return rows.slice().sort((a, b) => dir * cmp(a, b));
}

function SortHeader({ col, sort, onSort }) {
  const active = sort.key === col.key;
  return html`<th class=${`c-${col.key}${col.numeric ? ' num' : ''}`} aria-sort=${active ? (sort.desc ? 'descending' : 'ascending') : undefined}>
    <button type="button" class="th-btn" onClick=${() => onSort(col)}>
      ${col.label}${active && html`<${Icon} name=${sort.desc ? 'arrowDown' : 'arrowUp'} size=${12} />`}
    </button>
  </th>`;
}

function ConnRow({ c, now, closing, onClose }) {
  const closed = c.closed_at > 0;
  const dest = c.domain || c.destination || '—';
  const from = c.source_name || c.source;
  return html`<tr class=${closed ? 'crow closed' : 'crow'}>
    <td class="c-source">
      <span class="ellipsis" title=${c.process_path || c.source || undefined}>${c.process || from || '—'}</span>
      ${c.process && from && html`<span class="ellipsis muted small sub">${from}</span>`}
    </td>
    <td class="c-destination">
      <span class="ellipsis" title=${c.destination || undefined}>${dest}</span>
      ${c.domain && c.destination && html`<span class="ellipsis muted small mono sub">${c.destination}</span>`}
    </td>
    <td class="c-network"><span class="badge">${c.network || '?'}</span>${c.protocol && html` <span class="muted small">${c.protocol}</span>`}</td>
    <td class="c-outbound"><span class="ellipsis" title=${c.outbound_tag || undefined}>${c.outbound || '—'}</span></td>
    <td class="c-rule">${c.rule
      ? html`<span class="ellipsis small" title=${c.rule_item ? `${c.rule}\n${c.rule_item}` : c.rule}>${c.rule}</span>`
      : html`<span class="muted small">Final outbound</span>`}</td>
    <td class="c-speed num">${closed
      ? html`<span class="muted">—</span>`
      : html`<span title="Download">↓ ${formatRate(c.download_speed)}</span><span title="Upload">↑ ${formatRate(c.upload_speed)}</span>`}</td>
    <td class="c-total num"><span title="Received">↓ ${formatBytes(c.download)}</span><span title="Sent">↑ ${formatBytes(c.upload)}</span></td>
    <td class="c-age num">${formatDuration(((closed ? c.closed_at : now) - c.created_at) / 1000)}${closed && html`<span class="muted small">closed</span>`}</td>
    <td class="c-act">${!closed && html`<button type="button" class="icon-btn small" aria-label=${`Close the connection to ${dest}`}
        title="Close the connection" disabled=${closing} onClick=${() => onClose(c)}><${Icon} name="x" size=${16} /></button>`}</td>
  </tr>`;
}

export function ConnectionsView() {
  const res = useResource(() => api.get('api/v1/connections'));
  const errorRef = useRef(null);
  errorRef.current = res.error;
  const [filter, setFilter] = useState('');
  const [showClosed, setShowClosed] = useState(() => storageGet('throne.connClosed') !== '0');
  const [sort, setSort] = useState({ key: '', desc: false });
  const [limit, setLimit] = useState(PAGE);
  const [closing, setClosing] = useState(() => new Set());

  useEffect(() => {
    const tick = () => {
      const st = getState();
      if (document.visibilityState !== 'visible' || st.gatewayLost || !st.throne.reachable || st.unavailable || st.restartWait) return;
      if (errorRef.current && STOPPED.has(errorRef.current.code)) return;
      res.reload();
    };
    const timer = setInterval(tick, 1000);
    document.addEventListener('visibilitychange', tick);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', tick);
    };
  }, []);

  const data = res.data;
  const rows = (data && data.connections) || NO_ROWS;
  const q = filter.trim().toLowerCase();
  const shown = useMemo(() => {
    const list = rows.filter(c => (showClosed || !c.closed_at) && (!q || matches(c, q)));
    return sortRows(list, sort);
  }, [rows, showClosed, q, sort]);
  const openCount = useMemo(() => rows.filter(c => !c.closed_at).length, [rows]);

  useEffect(() => {
    setLimit(PAGE);
  }, [q, sort, showClosed]);

  const toggleClosed = () => {
    storageSet('throne.connClosed', showClosed ? '0' : null);
    setShowClosed(!showClosed);
  };
  const onSort = col => setSort(s => {
    if (s.key !== col.key) return { key: col.key, desc: !!col.numeric };
    if (s.desc === !!col.numeric) return { key: col.key, desc: !s.desc };
    return { key: '', desc: false };
  });

  const close = async c => {
    setClosing(s => new Set(s).add(c.id));
    try {
      await api.del('api/v1/connections', { ids: [c.id] });
      res.reload();
    } catch (e) {
      toastError(e, 'Closing failed');
    } finally {
      setClosing(s => {
        const next = new Set(s);
        next.delete(c.id);
        return next;
      });
    }
  };

  const closeAll = async () => {
    const ok = await confirmDialog({
      title: 'Close all connections',
      message: `Close all ${plural(openCount, 'open connection')}? Programs usually open new ones right away.`,
      confirmLabel: 'Close all',
      danger: true,
    });
    if (!ok) return;
    try {
      const r = await api.del('api/v1/connections', { all: true });
      toast(`Closed ${plural(r.closed ?? 0, 'connection')}.`, { kind: 'success' });
      res.reload();
    } catch (e) {
      toastError(e, 'Closing failed');
    }
  };

  const err = res.error;
  if (err && STOPPED.has(err.code)) {
    return html`<div class="view">
      <h1 class="view-title">Connections</h1>
      ${err.code === 'forbidden'
        ? html`<${Notice} kind="info" icon="shield"><strong>The connection list needs admin rights.</strong><span>${ADMIN_HELP}</span><//>`
        : html`<${Notice} kind="info">This Throne is too old to list its connections.<//>`}
      <p><button type="button" class="btn" onClick=${res.reload}>Try again</button></p>
    </div>`;
  }

  let body;
  if (!data && err && err.code === 'stats_disabled') {
    body = html`<${Notice} kind="info">Throne lists connections only while connection statistics are on in its basic settings.<//>`;
  } else if (!data) body = err ? html`<${ErrorBox} error=${err} onRetry=${res.reload} />` : html`<${Loading} />`;
  else if (!data.running) body = html`<p class="empty">No profile is running, so there are no connections.</p>`;
  else if (!data.updated_at) body = html`<${Loading} text="Waiting for Throne's first list…" />`;
  else if (!rows.length) body = html`<p class="empty">No connections yet.</p>`;
  else if (!shown.length) body = html`<p class="empty">${q ? `No connection matches “${filter.trim()}”.` : 'No open connections. Show the closed ones to see the rest.'}</p>`;
  else {
    body = html`<div class="table-wrap">
      <table class="ctable">
        <thead>
          <tr>
            ${COLUMNS.map(col => html`<${SortHeader} key=${col.key} col=${col} sort=${sort} onSort=${onSort} />`)}
            <th class="c-act"><span class="sr-only">Close</span></th>
          </tr>
        </thead>
        <tbody>
          ${shown.slice(0, limit).map(c => html`<${ConnRow} key=${c.id} c=${c} now=${data.updated_at}
            closing=${closing.has(c.id)} onClose=${close} />`)}
        </tbody>
      </table>
    </div>
    ${shown.length > limit && html`<button type="button" class="btn block more" onClick=${() => setLimit(l => l + PAGE)}>
      Show more (${shown.length - limit} more)
    </button>`}`;
  }

  const running = !!(data && data.running);
  return html`<div class="view view-connections">
    <h1 class="view-title">Connections</h1>
    <div class="toolbar">
      <div class="search">
        <${Icon} name="search" size=${16} />
        <input type="search" class="input" placeholder="Filter by process, destination, outbound, rule…" aria-label="Filter connections"
          value=${filter} onInput=${e => setFilter(e.currentTarget.value)} />
      </div>
      <div class="conn-sort">
        <select class="input sort" aria-label="Sort by" value=${sort.key}
          onChange=${e => { const col = COLUMNS.find(c => c.key === e.currentTarget.value); setSort({ key: col ? col.key : '', desc: !!(col && col.numeric) }); }}>
          <option value="">Throne order</option>
          ${COLUMNS.map(col => html`<option key=${col.key} value=${col.key}>${col.label}</option>`)}
        </select>
        <button type="button" class="icon-btn" aria-label="Reverse the order" aria-pressed=${sort.desc ? 'true' : 'false'}
          disabled=${!sort.key} onClick=${() => setSort(s => ({ ...s, desc: !s.desc }))}><${Icon} name=${sort.desc ? 'arrowUp' : 'arrowDown'} /></button>
      </div>
      <label class="check"><input type="checkbox" checked=${showClosed} onChange=${toggleClosed} />Show closed</label>
      <button type="button" class="btn danger" disabled=${!running || !openCount} onClick=${closeAll}>
        <${Icon} name="x" /><span>Close all</span>
      </button>
    </div>
    ${data && err && html`<${ErrorBox} error=${err} compact onRetry=${res.reload} />`}
    ${running && data.updated_at > 0 && html`<div class="list-head">
      <span class="muted small">${openCount} open · ${rows.length - openCount} closed${q ? ` · ${shown.length} shown` : ''}</span>
      <span class="muted small hide-xs">Updated every second while this page is visible.</span>
    </div>`}
    ${body}
  </div>`;
}

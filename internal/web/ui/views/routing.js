import { html, useState, useEffect, useRef } from '../ui.js';
import { api } from '../client.js';
import { useStore, useBlocked, useResource, toast, toastError, confirmDialog } from '../store.js';
import { actions, loadStatus } from '../session.js';
import { Icon } from '../components/icons.js';
import { Loading, ErrorBox, Notice, Spinner, Dialog } from '../components/common.js';
import { RouteEditor, KIND_LABELS } from './route-editor.js';

const NO_LIST = [];
const WARP_MODES = [['wireguard', 'WireGuard'], ['amnezia', 'AmneziaWG'], ['masque', 'MASQUE']];
const TERMS_URL = 'https://www.cloudflare.com/application/terms/';

function modeLabel(mode) {
  const m = WARP_MODES.find(([id]) => id === mode);
  return m ? m[1] : mode;
}

function NewRouteDialog({ onClose, onCreated }) {
  const [name, setName] = useState('');
  const [kind, setKind] = useState('rules');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);

  const submit = async e => {
    e.preventDefault();
    if (!name.trim() || busy) return;
    setBusy(true);
    setError(null);
    try {
      const r = await api.post('api/v1/routes', { kind, name: name.trim() });
      const warnings = r.warnings || NO_LIST;
      toast(`Added ${r.route.name}.`, { kind: warnings.length ? 'info' : 'success', detail: warnings.join('\n') });
      onCreated(r.route);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  const footer = html`<button type="button" class="btn" onClick=${onClose} disabled=${busy}>Cancel</button>
    <button type="submit" form="route-new-form" class="btn primary" disabled=${!name.trim() || busy}>
      ${busy ? html`<${Spinner} small /> Adding…` : 'Add'}
    </button>`;

  return html`<${Dialog} title="New routing profile" onClose=${onClose} footer=${footer} busy=${busy}>
    <form id="route-new-form" onSubmit=${submit}>
      <div class="field">
        <label for="route-new-name">Name</label>
        <input id="route-new-name" class="input" value=${name} autocomplete="off" required data-autofocus
          onInput=${e => setName(e.currentTarget.value)} />
      </div>
      <div class="segmented" role="radiogroup" aria-label="Kind">
        ${[['rules', 'Rules'], ['raw', 'Raw JSON']].map(([id, label]) => html`<label key=${id} class=${kind === id ? 'seg active' : 'seg'}>
          <input type="radio" name="route-new-kind" value=${id} checked=${kind === id} onChange=${() => setKind(id)} />${label}
        </label>`)}
      </div>
      <p class="muted small">${kind === 'rules'
        ? "Rules made in the rule editor, starting with the ones Throne's window gives a new routing profile."
        : 'A whole sing-box route object, written as JSON.'}</p>
      <${ErrorBox} error=${error} />
    </form>
  <//>`;
}

function WarpCard() {
  const adminDenied = useStore(s => s.adminDenied);
  const blocked = useBlocked();
  const res = useResource(() => api.get('api/v1/warp'), { topics: ['settings'] });
  const [working, setWorking] = useState(null);
  const w = res.data;
  const disabled = blocked || adminDenied || !!working || !!(w && w.registering);

  const apply = async patch => {
    setWorking('apply');
    try {
      const r = await api.put('api/v1/warp', patch);
      res.setData(r);
      const what = 'enabled' in patch ? (r.enabled ? 'WARP is on.' : 'WARP is off.') : `WARP uses ${modeLabel(r.mode)}.`;
      toast(`${what}${r.restart_needed ? ' It applies when the running profile restarts.' : ''}`, { kind: 'success' });
      loadStatus();
    } catch (e) {
      toastError(e, 'WARP not changed');
    } finally {
      setWorking(null);
    }
  };

  const register = async () => {
    let accept = false;
    if (!w.terms_accepted) {
      const url = /^https:\/\//i.test(w.terms_url || '') ? w.terms_url : TERMS_URL;
      const ok = await confirmDialog({
        title: "Cloudflare's WARP terms",
        message: html`<p>Registering a WARP device needs acceptance of <a href=${url} target="_blank" rel="noopener noreferrer">Cloudflare's WARP terms</a>. Accept them and register?</p>`,
        confirmLabel: 'Accept and register',
      });
      if (!ok) return;
      accept = true;
    } else if (w.configured) {
      const ok = await confirmDialog({
        title: 'Register new identity',
        message: `Throne registers a new device with Cloudflare and replaces the ${modeLabel(w.mode)} identity it has.`,
        confirmLabel: 'Register',
      });
      if (!ok) return;
    }
    setWorking('register');
    try {
      const r = await api.post('api/v1/warp/register', accept ? { mode: w.mode, accept_terms: true } : { mode: w.mode });
      res.setData(r);
      if (r.registering) toast('Cloudflare is still answering. This page updates once the registration ends.', { kind: 'info' });
      else toast(`Registered a new ${modeLabel(r.mode)} identity.${r.restart_needed ? ' It applies when the running profile restarts.' : ''}`, { kind: 'success' });
      loadStatus();
    } catch (e) {
      toastError(e, 'Registration failed');
      res.reload();
    } finally {
      setWorking(null);
    }
  };

  const registering = working === 'register' || !!(w && w.registering);
  return html`<section class="card" aria-labelledby="warp-title">
    <div class="card-head"><h2 id="warp-title">WARP</h2>${working === 'apply' && html`<${Spinner} small label="Applying" />`}</div>
    ${!w && (res.error ? html`<${ErrorBox} error=${res.error} compact onRetry=${res.reload} />` : html`<${Loading} />`)}
    ${w && html`
      ${res.error && html`<${ErrorBox} error=${res.error} compact onRetry=${res.reload} />`}
      <div class="switch-row">
        <label for="warp-enabled">
          <span>Enable WARP</span>
          <small>Proxied traffic leaves through Cloudflare WARP, for services that refuse VPN addresses. Rules to warp-bypass skip it.</small>
        </label>
        <input id="warp-enabled" type="checkbox" role="switch" class="toggle" checked=${!!w.enabled}
          disabled=${disabled || (!w.enabled && !w.configured)} onChange=${e => apply({ enabled: e.currentTarget.checked })} />
      </div>
      ${!w.enabled && !w.configured && html`<p class="muted small">Register an identity for ${modeLabel(w.mode)} before turning WARP on.</p>`}
      <div class="field">
        <label for="warp-mode">Mode</label>
        <select id="warp-mode" class="input" value=${w.mode} disabled=${disabled} onChange=${e => apply({ mode: e.currentTarget.value })}>
          ${!WARP_MODES.some(([id]) => id === w.mode) && html`<option value=${w.mode}>${w.mode}</option>`}
          ${WARP_MODES.map(([id, label]) => html`<option key=${id} value=${id}>${label}</option>`)}
        </select>
      </div>
      <dl class="facts">
        <dt>Identity</dt><dd>${w.configured ? html`<span class="ok">Registered</span>` : html`<span class="warn">None</span>`}</dd>
        <dt>Endpoint</dt><dd class="mono">${w.endpoint || '—'}</dd>
        <dt>Addresses</dt><dd class="mono">${(w.addresses || NO_LIST).length ? w.addresses.map(a => html`<div key=${a}>${a}</div>`) : '—'}</dd>
      </dl>
      <div class="actions">
        <button type="button" class="btn" disabled=${disabled} onClick=${register}>
          ${registering ? html`<${Spinner} small />` : html`<${Icon} name="key" />`}${registering ? 'Registering…' : 'Register new identity'}
        </button>
      </div>
      ${registering && html`<p class="muted small" role="status">Cloudflare can take up to a minute and a half to answer.</p>`}
    `}
  </section>`;
}

export function RoutingView() {
  const blocked = useBlocked();
  const adminDenied = useStore(s => s.adminDenied);
  const busy = useStore(s => s.busyAction);
  const running = useStore(s => !!(s.status && s.status.state === 'running'));
  const res = useResource(() => api.get('api/v1/routes'), { topics: ['status'], throttle: 1000 });
  const routes = (res.data && res.data.routes) || NO_LIST;
  const owned = routes.some(r => r.source_group_id >= 0);
  const groupsRes = useResource(() => (owned ? api.get('api/v1/groups') : Promise.resolve(null)), { deps: [owned], topics: ['groups'], throttle: 3000 });
  const groups = (groupsRes.data && groupsRes.data.groups) || NO_LIST;
  const [editing, setEditing] = useState(null);
  const [creating, setCreating] = useState(false);
  const title = useRef(null);
  const wasEditing = useRef(false);

  useEffect(() => {
    if (editing === null && wasEditing.current && title.current) title.current.focus();
    wasEditing.current = editing !== null;
  }, [editing]);

  if (editing !== null) {
    return html`<${RouteEditor} key=${editing} id=${editing} onClose=${() => { setEditing(null); res.reload(); }} />`;
  }

  const groupName = id => {
    const g = groups.find(x => x.id === id);
    return g ? g.name : 'a subscription group';
  };

  const remove = async r => {
    const notes = [
      r.active && 'Another routing profile becomes the active one.',
      'Groups that use it follow the active routing profile again.',
      r.source_group_id >= 0 && `${groupName(r.source_group_id)} stops importing a routing profile.`,
    ].filter(Boolean).join(' ');
    const ok = await confirmDialog({
      title: 'Delete routing profile',
      message: `Delete ${r.name}? This cannot be undone. ${notes}`,
      confirmLabel: 'Delete',
      danger: true,
    });
    if (!ok) return;
    try {
      const out = await api.del(`api/v1/routes/${r.id}`);
      toast(`Deleted ${r.name}.${out.restart_needed ? ' The running profile changes routing when it restarts.' : ''}`, { kind: 'success' });
      res.reload();
      loadStatus();
    } catch (e) {
      toastError(e, 'Delete failed');
    }
  };

  const adminTitle = adminDenied ? 'Needs admin rights on Throne' : undefined;
  return html`<div class="view">
    <h1 class="view-title" tabindex="-1" ref=${title}>Routing</h1>
    ${adminDenied && html`<${Notice} kind="info" icon="shield">Creating, editing and deleting routing profiles, and changing WARP, need admin rights on Throne.<//>`}
    <div class="grid two">
      <section class="card" aria-labelledby="routes-title">
        <div class="card-head">
          <h2 id="routes-title">Routing profiles</h2>
          <button type="button" class="btn small" disabled=${blocked || adminDenied} title=${adminTitle} onClick=${() => setCreating(true)}>
            <${Icon} name="plus" size=${16} />New
          </button>
        </div>
        ${res.error && html`<${ErrorBox} error=${res.error} compact onRetry=${res.reload} />`}
        ${!res.error && res.data === undefined && html`<${Loading} />`}
        ${res.data && !routes.length && html`<p class="empty">No routing profiles.</p>`}
        ${routes.length > 0 && html`<ul class="route-list">
          ${routes.map(r => html`<li key=${r.id} class=${r.active ? 'route-item active' : 'route-item'}>
            <div class="route-info">
              <button type="button" class="route-name" title=${`Edit ${r.name}`} onClick=${() => setEditing(r.id)}>${r.name}</button>
              <div class="route-meta">
                ${r.active && html`<span class="badge accent">Active</span>`}
                ${r.in_use && running && html`<span class="badge accent">In use</span>`}
                ${r.kind && html`<span class="badge">${KIND_LABELS[r.kind] || r.kind}</span>`}
                ${r.source_group_id >= 0 && html`<span class="muted small">From ${groupName(r.source_group_id)}: its updates replace the rules</span>`}
              </div>
            </div>
            <div class="route-actions">
              ${!r.active && html`<button type="button" class="btn small" disabled=${blocked || !!busy}
                title=${running ? 'Restarts the running profile unless its group has its own routing profile' : undefined}
                onClick=${() => actions.setRoute(r.id)}>Make active</button>`}
              <button type="button" class="icon-btn small" aria-label=${`Edit ${r.name}`} title="Edit" onClick=${() => setEditing(r.id)}>
                <${Icon} name="edit" size=${16} />
              </button>
              <button type="button" class="icon-btn small" aria-label=${`Delete ${r.name}`}
                title=${adminTitle || (routes.length < 2 ? 'Throne keeps at least one routing profile' : 'Delete')}
                disabled=${blocked || adminDenied || routes.length < 2} onClick=${() => remove(r)}>
                <${Icon} name="trash" size=${16} />
              </button>
            </div>
          </li>`)}
        </ul>`}
        ${busy === 'route' && html`<p class="muted small"><${Spinner} small /> Switching the routing profile…</p>`}
      </section>
      <${WarpCard} />
    </div>
    ${creating && html`<${NewRouteDialog} onClose=${() => setCreating(false)}
      onCreated=${route => { setCreating(false); res.reload(); setEditing(route.id); }} />`}
  </div>`;
}

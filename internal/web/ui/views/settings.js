import { html, useState } from '../ui.js';
import { api } from '../client.js';
import { useStore, useBlocked, useResource, toast, toastError, confirmDialog } from '../store.js';
import { loadStatus } from '../session.js';
import { KILL_SWITCH_ERRORS } from '../errors.js';
import { plural, formatBytes, timeAgo, formatDate } from '../format.js';
import { Icon } from '../components/icons.js';
import { Loading, ErrorBox, Notice, Spinner } from '../components/common.js';
import { SETTINGS_GROUPS, KNOWN_KEYS, parseValue, inferDef } from './settings-schema.js';

const RESTART_TEXT = {
  profile: 'Applies when the running profile restarts.',
  tun: 'Applies when the running profile restarts, in Tun mode.',
};
const KS_STATES = { off: 'off', arming: 'arming', armed: 'armed', failed: 'failed' };

function sameValue(a, b) {
  if (a === b) return true;
  return (a === '' || a === null || a === undefined) && (b === '' || b === null || b === undefined);
}

function SettingField({ def, value, error, disabled, changed, onChange }) {
  const id = `set-${def.key}`;
  const restart = RESTART_TEXT[def.restart] || '';
  const hasHelp = !!(def.help || restart);
  const describedBy = [hasHelp ? `${id}-help` : '', error ? `${id}-err` : ''].filter(Boolean).join(' ') || undefined;
  const help = hasHelp && html`<small id=${`${id}-help`} class="field-help">
    ${def.help}${def.help && restart ? ' ' : ''}${restart && html`<span class="restart-tag">${restart}</span>`}
  </small>`;
  const err = error && html`<small id=${`${id}-err`} class="field-error">${error}</small>`;
  const cls = `field${changed ? ' changed' : ''}${error ? ' invalid' : ''}`;
  if (def.type === 'bool') {
    return html`<div class=${cls}>
      <div class="switch-row">
        <label for=${id}><span>${def.label}</span></label>
        <input id=${id} type="checkbox" role="switch" class="toggle" checked=${!!value} disabled=${disabled}
          aria-describedby=${describedBy} onChange=${e => onChange(e.currentTarget.checked)} />
      </div>
      ${help}${err}
    </div>`;
  }
  let control;
  if (def.type === 'enum') {
    const current = String(value ?? '');
    const options = def.options.includes(current) ? def.options : [current, ...def.options];
    control = html`<select id=${id} class="input" value=${current} disabled=${disabled} aria-describedby=${describedBy}
      onChange=${e => onChange(e.currentTarget.value)}>
      ${options.map(o => html`<option key=${o} value=${o}>${o === '' ? '(empty)' : o}</option>`)}
    </select>`;
  } else if (def.type === 'number') {
    control = html`<div class="input-unit">
      <input id=${id} class="input" type="text" inputmode="numeric" autocomplete="off" value=${String(value ?? '')}
        disabled=${disabled} aria-describedby=${describedBy} aria-invalid=${error ? 'true' : undefined}
        onInput=${e => onChange(e.currentTarget.value)} />
      ${def.unit && html`<span class="unit">${def.unit}</span>`}
    </div>`;
  } else if (def.type === 'json') {
    control = html`<textarea id=${id} class="input mono" rows="3" readOnly=${true} value=${JSON.stringify(value, null, 2)}></textarea>`;
  } else {
    control = html`<input id=${id} class="input" type="text" spellcheck=${false} autocomplete="off" value=${String(value ?? '')}
      disabled=${disabled} aria-describedby=${describedBy} aria-invalid=${error ? 'true' : undefined}
      onInput=${e => onChange(e.currentTarget.value)} />`;
  }
  return html`<div class=${cls}>
    <label for=${id}>${def.label}</label>
    ${control}
    ${help}${err}
  </div>`;
}

function KillSwitchInfo() {
  const status = useStore(s => s.status);
  const state = status ? status.kill_switch : '';
  const err = status && status.kill_switch === 'failed' && status.kill_switch_error
    ? KILL_SWITCH_ERRORS[status.kill_switch_error] || status.kill_switch_error : '';
  return html`<div class="prose small">
    <p>While the kill switch is on, Throne blocks every connection of its device that does not go through Throne, also while no profile runs. Loopback stays open, and so does the local network unless Throne is set otherwise. Replies to clients of the Throne API still get out, so this web UI keeps working; other programs, such as an SSH session from outside the local network, are cut off when it arms.</p>
    <p>When it is on but cannot work, Throne refuses to start profiles until it works or is turned off.</p>
    ${status && html`<p>Now: <strong>${KS_STATES[state] || state}</strong>${err ? ` — ${err}` : ''}${status.capabilities && !status.capabilities.kill_switch ? ' (not available on this device)' : ''}</p>`}
  </div>`;
}

function GeoCard() {
  const blocked = useBlocked();
  const res = useResource(() => api.get('api/v1/geo-assets'));
  const [busyGeo, setBusyGeo] = useState(null);
  const [busyRules, setBusyRules] = useState(false);
  const files = (res.data && res.data.files) || [];
  const fileKey = name => name.replace(/\.[^.]+$/, '');

  const update = async (which, names, onlyMissing) => {
    setBusyGeo(which);
    try {
      const body = { only_missing: !!onlyMissing, timeout: 120 };
      if (names) body.files = names;
      const r = await api.post('api/v1/geo-assets/update', body);
      const out = r.files || [];
      const failed = out.filter(f => !f.ok);
      if (r.state === 'downloading') toast('The downloads carry on in Throne.');
      else if (failed.length) {
        toast(`${failed.map(f => f.name).join(', ')} failed to download.`, {
          kind: 'error',
          detail: failed.map(f => `${f.name}: ${f.error || 'failed'}`).join('; '),
        });
      } else if (out.length && out.every(f => f.skipped)) toast('No file was missing.');
      else toast('Geo files updated.', { kind: 'success' });
      res.reload();
    } catch (e) {
      toastError(e, 'Download failed');
    } finally {
      setBusyGeo(null);
    }
  };

  const updateRules = async () => {
    setBusyRules(true);
    try {
      const r = await api.post('api/v1/rule-sets/update', { timeout: 90 });
      const failures = r.failures || [];
      if (r.state === 'updating') toast('The rule-set update carries on in Throne.');
      else if (failures.length) {
        toast(`${r.updated || 0} rule-sets updated, ${failures.length} failed.`, {
          kind: 'error',
          detail: failures.map(f => `${f.tag}: ${f.error}`).join('; '),
        });
      } else toast(`${plural(r.updated || 0, 'rule-set')} updated.`, { kind: 'success' });
    } catch (e) {
      toastError(e, 'Rule-set update failed');
    } finally {
      setBusyRules(false);
    }
  };

  const geoBusy = busyGeo !== null;
  return html`<section class="card" aria-labelledby="geo-title">
    <div class="card-head"><h2 id="geo-title">Geo files and rule-sets</h2></div>
    ${res.error && html`<${ErrorBox} error=${res.error} compact onRetry=${res.reload} />`}
    ${!res.error && res.data === undefined && html`<${Loading} />`}
    ${files.length > 0 && html`<ul class="file-list">
      ${files.map(f => html`<li key=${f.name}>
        <div class="file-info">
          <strong>${f.name}</strong>
          <span class="muted small">${f.present ? `${formatBytes(f.size)} · updated ${timeAgo(f.updated_at)}` : 'Missing'}</span>
          <span class="muted small mono ellipsis" title=${f.url}>${f.url}</span>
        </div>
        <button type="button" class="btn small" disabled=${blocked || geoBusy} title=${f.present ? formatDate(f.updated_at) : undefined}
          onClick=${() => update(f.name, [fileKey(f.name)], false)}>
          ${busyGeo === f.name ? html`<${Spinner} small />` : html`<${Icon} name="download" size=${16} />`}${f.present ? 'Update' : 'Download'}
        </button>
      </li>`)}
    </ul>`}
    <div class="actions">
      <button type="button" class="btn" disabled=${blocked || geoBusy || !files.length} onClick=${() => update('all', null, false)}>
        ${busyGeo === 'all' ? html`<${Spinner} small />` : html`<${Icon} name="download" />`}Update all geo files
      </button>
      <button type="button" class="btn" disabled=${blocked || geoBusy || !files.some(f => !f.present)} onClick=${() => update('missing', null, true)}>
        ${busyGeo === 'missing' ? html`<${Spinner} small />` : html`<${Icon} name="download" />`}Download missing
      </button>
      <button type="button" class="btn" disabled=${blocked || busyRules} onClick=${updateRules}
        title="Download the remote rule-sets of the running profile again">
        ${busyRules ? html`<${Spinner} small />` : html`<${Icon} name="refresh" />`}Update rule-sets
      </button>
    </div>
  </section>`;
}

export function SettingsView() {
  const res = useResource(() => api.get('api/v1/settings'), { topics: ['settings'] });
  const adminDenied = useStore(s => s.adminDenied);
  const blocked = useBlocked();
  const [edits, setEdits] = useState({});
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState(null);
  const [serverErrors, setServerErrors] = useState({});

  const server = res.data && res.data.settings;
  if (!server) {
    return html`<div class="view"><h1 class="view-title">Settings</h1>
      ${res.error ? html`<${ErrorBox} error=${res.error} onRetry=${res.reload} />` : html`<${Loading} />`}
      <${GeoCard} /></div>`;
  }

  const groups = SETTINGS_GROUPS
    .map(g => ({ ...g, keys: g.keys.filter(k => k.key in server) }))
    .filter(g => g.keys.length);
  const unknown = Object.keys(server).filter(k => !KNOWN_KEYS.has(k)).sort().map(k => inferDef(k, server[k]));
  if (unknown.length) groups.push({ title: 'Other', keys: unknown });
  const defs = new Map(groups.flatMap(g => g.keys.map(d => [d.key, d])));

  const changes = Object.keys(edits)
    .filter(k => defs.has(k) && defs.get(k).type !== 'json')
    .map(k => ({ key: k, ...parseValue(defs.get(k), edits[k]) }))
    .filter(c => c.error || !sameValue(c.value, server[c.key]));
  const errors = {};
  for (const c of changes) if (c.error) errors[c.key] = c.error;
  const dirty = changes.length > 0;
  const valid = !changes.some(c => c.error);
  const readOnly = adminDenied || blocked || saving;

  const valueOf = key => (key in edits ? edits[key] : server[key]);
  const setValue = (key, value) => {
    setEdits(e => ({ ...e, [key]: value }));
    if (saveError) setSaveError(null);
    if (serverErrors[key]) setServerErrors(s => ({ ...s, [key]: '' }));
  };

  const save = async () => {
    if (!dirty || !valid || saving) return;
    const body = {};
    for (const c of changes) body[c.key] = c.value;
    if (body.kill_switch === true) {
      const ok = await confirmDialog({
        title: 'Turn on the kill switch',
        message: "Throne then blocks every connection of its device that does not go through Throne, also while no profile runs. The Throne API and this web UI stay reachable, but other remote sessions, such as SSH from outside the local network, are cut off.",
        confirmLabel: 'Turn on',
      });
      if (!ok) return;
    }
    setSaving(true);
    setSaveError(null);
    setServerErrors({});
    try {
      const r = await api.put('api/v1/settings', { settings: body });
      res.setData({ settings: r.settings || { ...server, ...body } });
      setEdits({});
      const n = (r.changed || []).length;
      const later = r.restart_needed ? ' Some changes apply when the running profile restarts.' : '';
      toast(n ? `Saved ${plural(n, 'setting')}.${later}` : 'Nothing changed.', { kind: 'success' });
      loadStatus();
    } catch (e) {
      setSaveError(e);
      if (e.code === 'bad_request' && e.detail) {
        const key = [...defs.keys()].find(k => e.detail.includes(k));
        if (key) setServerErrors({ [key]: e.detail });
      }
    } finally {
      setSaving(false);
    }
  };

  const discard = () => {
    setEdits({});
    setSaveError(null);
    setServerErrors({});
  };

  return html`<div class=${dirty ? 'view has-savebar' : 'view'}>
    <h1 class="view-title">Settings</h1>
    ${adminDenied && html`<${Notice} kind="info" icon="shield">Changing settings needs admin rights on Throne, so this page is read-only.<//>`}
    <form class="settings" onSubmit=${e => { e.preventDefault(); save(); }}>
      ${groups.map(g => html`<section key=${g.title} class="card" aria-labelledby=${`sg-${g.title}`}>
        <div class="card-head"><h2 id=${`sg-${g.title}`}>${g.title}</h2></div>
        ${g.title === 'Kill switch' && html`<${KillSwitchInfo} />`}
        <div class="fields">
          ${g.keys.map(def => html`<${SettingField} key=${def.key} def=${def} value=${valueOf(def.key)}
            error=${errors[def.key] || serverErrors[def.key] || ''} disabled=${readOnly}
            changed=${changes.some(c => c.key === def.key)} onChange=${v => setValue(def.key, v)} />`)}
        </div>
      </section>`)}
      ${dirty && html`<div class="savebar" role="region" aria-label="Unsaved changes">
        ${saveError ? html`<${ErrorBox} error=${saveError} compact />` : html`<span>${plural(changes.length, 'unsaved change')}${valid ? '' : ' · fix the marked fields'}</span>`}
        <div class="savebar-actions">
          <button type="button" class="btn" onClick=${discard} disabled=${saving}>Discard</button>
          <button type="submit" class="btn primary" disabled=${!valid || saving || adminDenied || blocked}>
            ${saving ? html`<${Spinner} small /> Saving…` : 'Save'}
          </button>
        </div>
      </div>`}
    </form>
    <${GeoCard} />
  </div>`;
}

import { html, useState, useEffect, useMemo, useRef } from '../ui.js';
import { api } from '../client.js';
import { useStore, useBlocked, useResource, toast, confirmDialog } from '../store.js';
import { loadStatus } from '../session.js';
import { plural, timeAgo, formatDate } from '../format.js';
import { Icon } from '../components/icons.js';
import { Loading, ErrorBox, Notice, Spinner, Dialog } from '../components/common.js';

export const KIND_LABELS = { rules: 'Rules', raw: 'Raw JSON', remote: 'Remote' };
const SIMPLE = [
  ['proxy', 'Proxy'],
  ['direct', 'Direct'],
  ['block', 'Block'],
  ['warp_bypass', 'WARP bypass'],
];
const DNS_DEFAULTS = {
  remote_dns: 'https://8.8.8.8/dns-query',
  remote_dns_disable_ipv6: false,
  direct_dns: 'localhost',
  direct_dns_disable_ipv6: false,
  dns_final_out: 'remote',
  enable_dns_routing: true,
  fake_dns: false,
  fakeip_disable_ipv6: false,
};
const FIELDS = ['name', 'default_outbound', 'rules', 'simple', 'raw_route', 'prevent_modifications', 'remote_url', 'auto_update', 'custom_dns', 'dns'];
const NO_LIST = [];
const URL_RE = /^https?:\/\/\S+$/i;

let ruleSeq = 0;
const withKey = rule => ({ ...rule, key: ++ruleSeq });
const stripKey = ({ key, ...rule }) => rule;

function draftOf(route) {
  const d = {};
  for (const f of FIELDS) d[f] = route[f];
  d.name = route.name || '';
  d.rules = (route.rules || NO_LIST).map(withKey);
  d.simple = { ...(route.simple || {}) };
  return d;
}

// The fields that differ from what Throne returned; inside `simple`, only the changed boxes.
function changesOf(draft, base) {
  const out = {};
  for (const f of FIELDS) {
    if (f === 'simple') {
      const s = {};
      for (const [k] of SIMPLE) if ((draft.simple[k] || '') !== ((base.simple || {})[k] || '')) s[k] = draft.simple[k] || '';
      if (Object.keys(s).length) out.simple = s;
      continue;
    }
    const value = f === 'rules' ? draft.rules.map(stripKey) : f === 'name' ? draft.name.trim() : draft[f];
    if (JSON.stringify(value) !== JSON.stringify(base[f])) out[f] = value;
  }
  return out;
}

function jsonObjectError(text) {
  if (!text.trim()) return '';
  try {
    const v = JSON.parse(text);
    return v && typeof v === 'object' && !Array.isArray(v) ? '' : 'It must be a JSON object.';
  } catch (e) {
    return `Not valid JSON: ${e.message}`;
  }
}

function useOutbounds(schema) {
  const profilesRes = useResource(() => api.get('api/v1/profiles'), { topics: ['profiles'], throttle: 5000 });
  const groupsRes = useResource(() => api.get('api/v1/groups'), { topics: ['groups'], throttle: 5000 });
  const profiles = profilesRes.data && profilesRes.data.profiles;
  const groups = groupsRes.data && groupsRes.data.groups;
  return useMemo(() => {
    const fixed = (schema && schema.outbounds) || NO_LIST;
    const names = new Map(fixed.map(o => [o.id, o.name]));
    const byGroup = new Map((groups || NO_LIST).map(g => [g.id, { id: g.id, label: g.name, items: [] }]));
    for (const p of profiles || NO_LIST) {
      const g = byGroup.get(p.group);
      if (!g) continue;
      g.items.push(p);
      names.set(p.id, `[${g.label}] ${p.name}`);
    }
    return { fixed, names, groups: [...byGroup.values()].filter(g => g.items.length), ready: !!(profiles && groups) };
  }, [schema, profiles, groups]);
}

function outboundName(outbounds, id) {
  if (outbounds.names.has(id)) return outbounds.names.get(id);
  if (id < 0) return `outbound ${id}`;
  return outbounds.ready ? `Missing profile (id ${id})` : `Profile ${id}`;
}

function OutboundSelect({ id, value, outbounds, onChange, fixedOnly = false }) {
  return html`<select id=${id} class="input" value=${String(value)} onChange=${e => onChange(Number(e.currentTarget.value))}>
    ${!outbounds.names.has(value) && html`<option value=${String(value)}>${outboundName(outbounds, value)}</option>`}
    ${outbounds.fixed.map(o => html`<option key=${o.id} value=${String(o.id)}>${o.name}</option>`)}
    ${!fixedOnly && outbounds.groups.map(g => html`<optgroup key=${g.id} label=${g.label}>
      ${g.items.map(p => html`<option key=${p.id} value=${String(p.id)}>${p.name || `Profile ${p.id}`}</option>`)}
    </optgroup>`)}
  </select>`;
}

function valueText(v) {
  if (Array.isArray(v)) return v.length > 3 ? `${v.slice(0, 3).join(', ')} +${v.length - 3}` : v.join(', ');
  return String(v);
}

function ruleSummary(rule, meta, outbounds) {
  const at = rule.attributes || {};
  const conds = [];
  const opts = [];
  for (const [k, v] of Object.entries(at)) {
    if (k === 'action' || k === 'outbound') continue;
    const a = meta.get(k);
    const text = typeof v === 'boolean' ? (v ? k : `${k}: false`) : `${k}: ${valueText(v)}`;
    (a && a.match ? conds : opts).push(text);
  }
  let action = at.action || 'route';
  if ('outbound' in at) action += ` → ${outboundName(outbounds, at.outbound)}`;
  if (opts.length) action += ` (${opts.join(', ')})`;
  return { conds: conds.join(' · ') || 'Any connection', action };
}

// Form values: a list is edited as lines of text.
function toForm(a, v) {
  if (a.kind === 'list') return Array.isArray(v) ? v.join('\n') : String(v ?? '');
  if (a.kind === 'bool') return !!v;
  if (a.kind === 'outbound') return Number(v);
  return String(v ?? '');
}

function fromForm(a, v) {
  if (a.kind === 'list') return String(v || '').split('\n').map(s => s.trim()).filter(Boolean);
  if (a.kind === 'bool') return !!v;
  if (a.kind === 'outbound') return Number(v);
  return String(v ?? '').trim();
}

function emptyValue(a) {
  if (a.kind === 'bool') return false;
  if (a.kind === 'outbound') return -1;
  return '';
}

function isBlank(v) {
  return v === '' || v === false || (Array.isArray(v) && !v.length);
}

function AttrField({ a, value, outbounds, onChange, onRemove }) {
  const id = `rule-attr-${a.name}`;
  const remove = onRemove && html`<button type="button" class="icon-btn small" aria-label=${`Remove ${a.name}`} title="Remove"
    onClick=${onRemove}><${Icon} name="x" size=${16} /></button>`;
  if (a.kind === 'bool') {
    return html`<div class="attr-field attr-bool">
      <label class="check"><input id=${id} type="checkbox" checked=${!!value} onChange=${e => onChange(e.currentTarget.checked)} /><code>${a.name}</code></label>
      ${remove}
    </div>`;
  }
  let control;
  if (a.kind === 'list') {
    control = html`<textarea id=${id} class="input mono" rows="3" spellcheck=${false} value=${value} aria-describedby=${`${id}-help`}
      onInput=${e => onChange(e.currentTarget.value)}></textarea>
      <small id=${`${id}-help`} class="field-help">One per line.</small>`;
  } else if (a.kind === 'select') {
    const values = a.values.includes(value) ? a.values : [...a.values, value];
    control = html`<select id=${id} class="input" value=${value} onChange=${e => onChange(e.currentTarget.value)}>
      ${values.map(v => html`<option key=${v} value=${v}>${v === '' ? '(none)' : v}</option>`)}
    </select>`;
  } else if (a.kind === 'outbound') {
    control = html`<${OutboundSelect} id=${id} value=${value} outbounds=${outbounds} onChange=${onChange} />`;
  } else {
    control = html`<input id=${id} class="input" type="text" spellcheck=${false} autocomplete="off" value=${value}
      onInput=${e => onChange(e.currentTarget.value)} />`;
  }
  return html`<div class="field attr-field">
    <div class="attr-head"><label for=${id}><code>${a.name}</code></label>${remove}</div>
    ${control}
  </div>`;
}

function RuleDialog({ rule, isNew, schema, outbounds, onClose, onSave }) {
  const attrs = schema.attributes || NO_LIST;
  const meta = useMemo(() => new Map(attrs.map(a => [a.name, a])), [attrs]);
  const initial = rule.attributes || {};
  const actionMeta = meta.get('action');
  const [name, setName] = useState(rule.name || '');
  const [vals, setVals] = useState(() => {
    const v = {};
    for (const a of attrs) if (a.name in initial) v[a.name] = toForm(a, initial[a.name]);
    return v;
  });
  const [conds, setConds] = useState(() => attrs.filter(a => a.match && a.name in initial).map(a => a.name));
  const focusNext = useRef(null);
  // Attributes the schema does not describe (such as sniffers) go back unchanged.
  const extra = Object.entries(initial).filter(([k]) => !meta.has(k));

  useEffect(() => {
    if (!focusNext.current) return;
    const el = document.getElementById(`rule-attr-${focusNext.current}`);
    focusNext.current = null;
    if (el) el.focus();
  });

  const action = vals.action ?? (actionMeta && actionMeta.values.length ? actionMeta.values[0] : 'route');
  const actionValues = actionMeta ? (actionMeta.values.includes(action) ? actionMeta.values : [...actionMeta.values, action]) : [action];
  const options = attrs.filter(a => !a.match && a.name !== 'action' && (!a.actions.length || a.actions.includes(action)));
  const unused = attrs.filter(a => a.match && !conds.includes(a.name));
  const set = (k, v) => setVals(s => ({ ...s, [k]: v }));

  const addCond = k => {
    const a = meta.get(k);
    if (!a) return;
    const first = a.kind === 'bool' ? true : a.kind === 'select' ? a.values.find(v => v !== '') ?? '' : '';
    setVals(s => ({ ...s, [k]: k in s && !isBlank(fromForm(a, s[k])) ? s[k] : first }));
    setConds(c => [...c, k]);
    focusNext.current = k;
  };

  const submit = e => {
    e.preventDefault();
    const out = Object.fromEntries(extra);
    for (const k of conds) {
      const v = fromForm(meta.get(k), vals[k]);
      if (!isBlank(v)) out[k] = v;
    }
    out.action = action;
    for (const a of options) {
      const v = fromForm(a, vals[a.name] ?? emptyValue(a));
      if (a.kind === 'outbound' || !isBlank(v)) out[a.name] = v;
    }
    onSave({ ...rule, name: name.trim(), attributes: out });
  };

  const footer = html`<button type="button" class="btn" onClick=${onClose}>Cancel</button>
    <button type="submit" form="rule-form" class="btn primary">${isNew ? 'Add rule' : 'Done'}</button>`;

  return html`<${Dialog} title=${isNew ? 'New rule' : `Rule ${rule.name || ''}`} onClose=${onClose} footer=${footer} wide>
    <form id="rule-form" onSubmit=${submit}>
      <div class="field">
        <label for="rule-name">Name</label>
        <input id="rule-name" class="input" value=${name} autocomplete="off" onInput=${e => setName(e.currentTarget.value)} data-autofocus />
      </div>
      <fieldset class="rule-section">
        <legend>Conditions</legend>
        ${!conds.length && html`<p class="muted small">No condition: the rule applies to every connection.</p>`}
        ${conds.map(k => html`<${AttrField} key=${k} a=${meta.get(k)} value=${vals[k]} outbounds=${outbounds}
          onChange=${v => set(k, v)} onRemove=${() => setConds(c => c.filter(x => x !== k))} />`)}
        ${unused.length > 0 && html`<select class="input add-cond" aria-label="Add a condition" value=""
            onChange=${e => { const k = e.currentTarget.value; e.currentTarget.value = ''; if (k) addCond(k); }}>
          <option value="">Add a condition…</option>
          ${unused.map(a => html`<option key=${a.name} value=${a.name}>${a.name}</option>`)}
        </select>`}
      </fieldset>
      <fieldset class="rule-section">
        <legend>Action</legend>
        <div class="field">
          <label for="rule-attr-action" class="sr-only">action</label>
          <select id="rule-attr-action" class="input" value=${action} onChange=${e => set('action', e.currentTarget.value)}>
            ${actionValues.map(v => html`<option key=${v} value=${v}>${v}</option>`)}
          </select>
        </div>
        ${options.map(a => html`<${AttrField} key=${a.name} a=${a} value=${vals[a.name] ?? toForm(a, emptyValue(a))} outbounds=${outbounds}
          onChange=${v => set(a.name, v)} />`)}
        ${extra.length > 0 && html`<p class="muted small">Also kept as they are: ${extra.map(([k, v]) => `${k}: ${valueText(v)}`).join(' · ')}</p>`}
      </fieldset>
    </form>
  <//>`;
}

function RuleItem({ rule, index, count, summary, onToggle, onMove, onEdit, onDelete }) {
  const custom = rule.type === 'custom';
  const endpoint = rule.type === 'endpoint_preferred_by';
  const label = rule.name || `Rule ${index + 1}`;
  return html`<li class=${`rule-item${rule.disabled ? ' disabled' : ''}`} data-rule-key=${rule.key}>
    <span class="rule-num" aria-hidden="true">${index + 1}</span>
    <div class="rule-main">
      <div class="rule-name">
        <span class="ellipsis">${label}</span>
        ${!custom && html`<span class="badge">${endpoint ? 'Endpoint' : 'Basic'}</span>`}
        ${rule.disabled && html`<span class="badge">Off</span>`}
      </div>
      <div class="rule-sum small">${summary.conds}</div>
      <div class="rule-sum small muted">${summary.action}</div>
      ${!custom && html`<div class="muted small">${endpoint
        ? "Follows the profile's endpoints, which only Throne's window edits."
        : 'Its addresses and processes are edited in Basic.'}</div>`}
    </div>
    <div class="rule-actions">
      ${!endpoint && html`<input type="checkbox" role="switch" class="toggle" checked=${!rule.disabled}
        aria-label=${`Use ${label}`} title=${rule.disabled ? 'Off: kept but left out of the configuration' : 'On'} onChange=${() => onToggle(index)} />`}
      <button type="button" class="icon-btn small" data-move="-1" aria-label=${`Move ${label} up`} disabled=${index === 0}
        onClick=${() => onMove(index, -1)}><${Icon} name="arrowUp" size=${16} /></button>
      <button type="button" class="icon-btn small" data-move="1" aria-label=${`Move ${label} down`} disabled=${index === count - 1}
        onClick=${() => onMove(index, 1)}><${Icon} name="arrowDown" size=${16} /></button>
      ${custom && html`<button type="button" class="icon-btn small" aria-label=${`Edit ${label}`} onClick=${() => onEdit(index)}>
        <${Icon} name="edit" size=${16} /></button>
        <button type="button" class="icon-btn small" aria-label=${`Delete ${label}`} onClick=${() => onDelete(index)}>
        <${Icon} name="trash" size=${16} /></button>`}
    </div>
  </li>`;
}

function newRuleName(rules) {
  const names = new Set(rules.map(r => r.name));
  let n = rules.length + 1;
  while (names.has(`rule_${n}`)) n++;
  return `rule_${n}`;
}

function DnsFields({ dns, onChange }) {
  const set = (k, v) => onChange({ ...dns, [k]: v });
  const check = (k, label) => html`<label class="check"><input type="checkbox" checked=${!!dns[k]} onChange=${e => set(k, e.currentTarget.checked)} />${label}</label>`;
  return html`<div class="fields">
    <div class="field">
      <label for="route-dns-remote">Remote DNS</label>
      <input id="route-dns-remote" class="input" type="text" spellcheck=${false} autocomplete="off" value=${dns.remote_dns}
        onInput=${e => set('remote_dns', e.currentTarget.value)} />
      ${check('remote_dns_disable_ipv6', 'Disable IPv6')}
    </div>
    <div class="field">
      <label for="route-dns-direct">Direct DNS</label>
      <input id="route-dns-direct" class="input" type="text" spellcheck=${false} autocomplete="off" value=${dns.direct_dns}
        onInput=${e => set('direct_dns', e.currentTarget.value)} />
      ${check('direct_dns_disable_ipv6', 'Disable IPv6')}
    </div>
    <div class="field">
      <label for="route-dns-final">Default DNS server</label>
      <select id="route-dns-final" class="input" value=${dns.dns_final_out} onChange=${e => set('dns_final_out', e.currentTarget.value)}>
        ${['remote', 'direct'].map(v => html`<option key=${v} value=${v}>${v}</option>`)}
      </select>
      ${check('enable_dns_routing', 'DNS routing: resolve the domains this profile sends direct with the direct DNS')}
    </div>
    <div class="field">
      <span class="field-label">FakeIP</span>
      ${check('fake_dns', 'Enable FakeIP')}
      ${check('fakeip_disable_ipv6', 'No fake IPv6 addresses')}
    </div>
  </div>`;
}

export function RouteEditor({ id, onClose }) {
  const adminDenied = useStore(s => s.adminDenied);
  const blocked = useBlocked();
  const detail = useResource(() => api.get(`api/v1/routes/${id}`), { deps: [id] });
  const schemaRes = useResource(() => api.get('api/v1/routes/schema'));
  const schema = schemaRes.data;
  const outbounds = useOutbounds(schema);
  const meta = useMemo(() => new Map(((schema && schema.attributes) || NO_LIST).map(a => [a.name, a])), [schema]);
  const [base, setBase] = useState(null);
  const [draft, setDraft] = useState(null);
  const [ruleEdit, setRuleEdit] = useState(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState(null);
  const [warnings, setWarnings] = useState(NO_LIST);
  const title = useRef(null);
  const errorRef = useRef(null);
  const moveFocus = useRef(null);

  const load = route => {
    setBase(route);
    setDraft(draftOf(route));
  };
  useEffect(() => {
    if (detail.data) load(detail.data);
  }, [detail.data]);
  useEffect(() => {
    if (title.current) title.current.focus();
  }, []);
  useEffect(() => {
    if (error && errorRef.current) errorRef.current.scrollIntoView({ block: 'nearest' });
  }, [error]);
  // Moving a rule re-inserts its row, which drops focus: put it back on the same button.
  useEffect(() => {
    const m = moveFocus.current;
    if (!m) return;
    moveFocus.current = null;
    const row = document.querySelector(`[data-rule-key="${m.key}"]`);
    const btn = row && (row.querySelector(`[data-move="${m.dir}"]:not(:disabled)`) || row.querySelector('[data-move]:not(:disabled)'));
    if (btn) btn.focus();
  });

  const changes = draft && base ? changesOf(draft, base) : {};
  const dirty = Object.keys(changes).length > 0;
  const kind = base ? base.kind : '';
  const structured = kind === 'rules' || kind === 'remote';
  const rawError = draft && kind === 'raw' ? jsonObjectError(draft.raw_route || '') : '';
  const urlError = draft && kind === 'remote' && draft.remote_url.trim() && !URL_RE.test(draft.remote_url.trim()) ? 'Enter an http or https URL.' : '';
  const nameError = draft && !draft.name.trim() ? 'Enter a name.' : '';
  const invalid = !!(rawError || urlError || nameError);
  const readOnly = adminDenied || saving;

  const update = patch => setDraft(d => ({ ...d, ...(typeof patch === 'function' ? patch(d) : patch) }));
  const setRules = fn => update(d => ({ rules: fn(d.rules) }));

  const back = async () => {
    if (dirty && !(await confirmDialog({ title: 'Discard changes', message: 'Leave the routing profile without saving your changes?', confirmLabel: 'Discard', danger: true }))) return;
    onClose();
  };

  const discard = () => {
    setDraft(draftOf(base));
    setError(null);
  };

  const save = async e => {
    if (e) e.preventDefault();
    if (!dirty || invalid || saving || adminDenied) return;
    setSaving(true);
    setError(null);
    try {
      const r = await api.put(`api/v1/routes/${id}`, changes);
      load(r.route);
      setWarnings(r.warnings || NO_LIST);
      const later = r.restart_needed ? ' It applies when the running profile restarts.' : '';
      toast(`Saved ${r.route.name}.${later}`, { kind: 'success' });
      loadStatus();
    } catch (err) {
      setError(err);
    } finally {
      setSaving(false);
    }
  };

  const moveRule = (i, dir) => {
    moveFocus.current = { key: draft.rules[i].key, dir };
    setRules(rs => {
      const next = rs.slice();
      const [r] = next.splice(i, 1);
      next.splice(i + dir, 0, r);
      return next;
    });
  };
  const toggleRule = i => setRules(rs => rs.map((r, j) => (j === i ? { ...r, disabled: !r.disabled } : r)));
  const deleteRule = i => setRules(rs => rs.filter((_, j) => j !== i));
  const addRule = () => {
    const actionMeta = meta.get('action');
    const action = !actionMeta || actionMeta.values.includes('route') ? 'route' : actionMeta.values[0];
    setRuleEdit({ index: -1, rule: { type: 'custom', name: newRuleName(draft.rules), disabled: false, attributes: { action, outbound: -1 } } });
  };
  const saveRule = rule => {
    const { index } = ruleEdit;
    setRules(rs => (index < 0 ? [...rs, withKey(rule)] : rs.map((r, j) => (j === index ? rule : r))));
    setRuleEdit(null);
  };
  const setCustomDns = on => update(d => ({
    custom_dns: on,
    dns: on ? d.dns || { ...DNS_DEFAULTS } : base.dns === null && JSON.stringify(d.dns) === JSON.stringify(DNS_DEFAULTS) ? null : d.dns,
  }));

  const head = html`<div class="editor-head">
    <button type="button" class="icon-btn" aria-label="Back to the routing profiles" title="Back" onClick=${back}><${Icon} name="back" /></button>
    <h1 class="view-title" tabindex="-1" ref=${title}>${base ? base.name : 'Routing profile'}</h1>
    ${base && html`<span class="badge">${KIND_LABELS[kind] || kind}</span>`}
    ${base && base.active && html`<span class="badge accent">Active</span>`}
    ${base && base.in_use && html`<span class="badge accent">In use</span>`}
  </div>`;

  const loadError = detail.error || schemaRes.error;
  if (!draft || !schema) {
    return html`<div class="view">${head}
      ${loadError ? html`<${ErrorBox} error=${loadError} onRetry=${() => { detail.reload(); schemaRes.reload(); }} />` : html`<${Loading} />`}
    </div>`;
  }

  return html`<div class=${dirty ? 'view view-route has-savebar' : 'view view-route'}>
    ${head}
    ${adminDenied && html`<${Notice} kind="info" icon="shield">Editing routing profiles needs admin rights on Throne, so this page is read-only.<//>`}
    ${base.source_group_id >= 0 && html`<${Notice} kind="info">A subscription group provides this routing profile: its next update replaces the rules.<//>`}
    <form id="route-form" onSubmit=${save}>
      <fieldset class="editor-fields" disabled=${readOnly}>
        <section class="card" aria-labelledby="route-general">
          <div class="card-head"><h2 id="route-general">General</h2></div>
          <div class="fields">
            <div class=${`field${nameError ? ' invalid' : ''}`}>
              <label for="route-name">Name</label>
              <input id="route-name" class="input" value=${draft.name} autocomplete="off" aria-invalid=${nameError ? 'true' : undefined}
                onInput=${e => update({ name: e.currentTarget.value })} />
              ${nameError && html`<small class="field-error">${nameError}</small>`}
            </div>
            ${structured && html`<div class="field">
              <label for="route-default-out">Default outbound</label>
              <${OutboundSelect} id="route-default-out" value=${draft.default_outbound} outbounds=${outbounds} fixedOnly
                onChange=${v => update({ default_outbound: v })} />
              <small class="field-help">Where connections go that no rule matches.</small>
            </div>`}
          </div>
          ${kind === 'remote' && html`<div class="fields">
            <div class=${`field${urlError ? ' invalid' : ''}`}>
              <label for="route-url">URL</label>
              <input id="route-url" class="input" type="url" inputmode="url" spellcheck=${false} value=${draft.remote_url}
                aria-invalid=${urlError ? 'true' : undefined} onInput=${e => update({ remote_url: e.currentTarget.value })} />
              ${urlError
                ? html`<small class="field-error">${urlError}</small>`
                : html`<small class="field-help" title=${formatDate(base.remote_updated_at)}>
                  ${base.remote_updated_at ? `Fetched ${timeAgo(base.remote_updated_at)}.` : 'Not fetched yet.'} An update replaces the rules with the ones at the URL.
                </small>`}
            </div>
            <div class="field">
              <span class="field-label">Updates</span>
              <label class="check"><input type="checkbox" checked=${draft.auto_update} onChange=${e => update({ auto_update: e.currentTarget.checked })} />Update automatically</label>
            </div>
          </div>`}
          ${(base.endpoints || NO_LIST).length > 0 && html`<p class="muted small">${base.endpoints.length === 1 ? 'An endpoint profile runs' : `${base.endpoints.length} endpoint profiles run`} alongside it. Only Throne's window edits them.</p>`}
        </section>

        ${structured && html`<section class="card" aria-labelledby="route-basic">
          <div class="card-head"><h2 id="route-basic">Basic</h2></div>
          <p class="muted small" id="route-basic-help">One <code>prefix:value</code> per line. Prefixes: domain, suffix, keyword, regex, ruleset, ip, processName, processPath. For example <code>suffix:example.com</code> or <code>ip:10.0.0.0/8</code>.</p>
          <div class="fields">
            ${SIMPLE.map(([k, label]) => html`<div key=${k} class="field">
              <label for=${`route-simple-${k}`}>${label}</label>
              <textarea id=${`route-simple-${k}`} class="input mono" rows="5" spellcheck=${false} aria-describedby="route-basic-help"
                value=${draft.simple[k] || ''} onInput=${e => { const v = e.currentTarget.value; update(d => ({ simple: { ...d.simple, [k]: v } })); }}></textarea>
            </div>`)}
          </div>
        </section>`}

        ${structured && html`<section class="card" aria-labelledby="route-rules">
          <div class="card-head">
            <h2 id="route-rules">Rules</h2>
            <button type="button" class="btn small" onClick=${addRule}><${Icon} name="plus" size=${16} />Add rule</button>
          </div>
          <p class="muted small">Throne checks the rules from the top; the first that matches decides.</p>
          ${!draft.rules.length && html`<p class="empty">No rules: every connection goes to the default outbound.</p>`}
          ${draft.rules.length > 0 && html`<ol class="rule-list">
            ${draft.rules.map((r, i) => html`<${RuleItem} key=${r.key} rule=${r} index=${i} count=${draft.rules.length}
              summary=${ruleSummary(r, meta, outbounds)} onToggle=${toggleRule} onMove=${moveRule}
              onEdit=${j => setRuleEdit({ index: j, rule: draft.rules[j] })} onDelete=${deleteRule} />`)}
          </ol>`}
        </section>`}

        ${kind === 'raw' && html`<section class="card" aria-labelledby="route-raw">
          <div class="card-head"><h2 id="route-raw">Route JSON</h2></div>
          <div class=${`field${rawError ? ' invalid' : ''}`}>
            <label for="route-raw-json">sing-box <code>route</code> object</label>
            <textarea id="route-raw-json" class="input mono" rows="16" spellcheck=${false} value=${draft.raw_route}
              aria-invalid=${rawError ? 'true' : undefined} onInput=${e => update({ raw_route: e.currentTarget.value })}></textarea>
            ${rawError && html`<small class="field-error">${rawError}</small>`}
          </div>
          <div class="switch-row">
            <label for="route-raw-prevent">
              <span>Prevent modifications</span>
              <small>Use the JSON as it is. Otherwise Throne still adds the rules its features need.</small>
            </label>
            <input id="route-raw-prevent" type="checkbox" role="switch" class="toggle" checked=${draft.prevent_modifications}
              onChange=${e => update({ prevent_modifications: e.currentTarget.checked })} />
          </div>
        </section>`}

        <section class="card" aria-labelledby="route-dns">
          <div class="card-head"><h2 id="route-dns">DNS</h2></div>
          <div class="switch-row">
            <label for="route-dns-custom">
              <span>Own DNS settings</span>
              <small>Replace the global DNS servers while this routing profile is in use.</small>
            </label>
            <input id="route-dns-custom" type="checkbox" role="switch" class="toggle" checked=${draft.custom_dns}
              onChange=${e => setCustomDns(e.currentTarget.checked)} />
          </div>
          ${draft.custom_dns && draft.dns && html`<${DnsFields} dns=${draft.dns} onChange=${dns => update({ dns })} />`}
          <p class="muted small">Local override, the hosts file, predefined answers and the advanced DNS settings always come from Throne's routing settings.</p>
        </section>
      </fieldset>
    </form>
    ${warnings.length > 0 && html`<${Notice} kind="warn" icon="alert">
      <strong>Saved with ${plural(warnings.length, 'warning')}</strong>
      <ul class="problems">${warnings.map((w, i) => html`<li key=${i}>${w}</li>`)}</ul>
    <//>`}
    <div ref=${errorRef}><${ErrorBox} error=${error} /></div>
    ${dirty && html`<div class="savebar" role="region" aria-label="Unsaved changes">
      <span>${invalid ? 'Fix the marked fields' : 'Unsaved changes'}</span>
      <div class="savebar-actions">
        <button type="button" class="btn" onClick=${discard} disabled=${saving}>Discard</button>
        <button type="submit" form="route-form" class="btn primary" disabled=${invalid || saving || adminDenied || blocked}>
          ${saving ? html`<${Spinner} small /> Saving…` : 'Save'}
        </button>
      </div>
    </div>`}
    ${ruleEdit && html`<${RuleDialog} rule=${ruleEdit.rule} isNew=${ruleEdit.index < 0} schema=${schema} outbounds=${outbounds}
      onClose=${() => setRuleEdit(null)} onSave=${saveRule} />`}
  </div>`;
}

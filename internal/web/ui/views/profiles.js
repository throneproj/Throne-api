import { html, useState, useEffect, useMemo, useRef } from '../ui.js';
import { api } from '../client.js';
import {
  useStore, useBlocked, useResource, useNow, toast, toastError, confirmDialog, storageGet, storageSet,
} from '../store.js';
import { actions } from '../session.js';
import { plural, timeAgo, formatDate, formatDuration, escapeRegExp } from '../format.js';
import { Icon } from '../components/icons.js';
import { Loading, ErrorBox, Spinner, Menu } from '../components/common.js';
import { Latency, Country, Speed, Traffic } from '../components/profile-bits.js';
import { ImportDialog, GroupDialog } from './profile-dialogs.js';

const PAGE = 300;
const NO_PROFILES = [];
const NO_GROUPS = [];
const KIND_LABELS = { latency: 'Latency test', ip: 'IP test', speed: 'Speed test' };
const SORTS = [
  ['order', 'Throne order'],
  ['name', 'Name'],
  ['latency', 'Latency'],
  ['speed', 'Download speed'],
  ['traffic', 'Traffic'],
  ['type', 'Type'],
];
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

function matches(p, q) {
  return (p.name || '').toLowerCase().includes(q)
    || (p.address || '').toLowerCase().includes(q)
    || (p.type || '').toLowerCase() === q
    || (p.ip_out || '').includes(q)
    || (p.country || '').toLowerCase() === q;
}

// Profiles without a value (untested, failed) always go last.
function byMetric(list, value, dir) {
  const valued = [];
  const rest = [];
  for (const p of list) (value(p) === null ? rest : valued).push(p);
  valued.sort((a, b) => dir * (value(a) - value(b)));
  return valued.concat(rest);
}

function sortProfiles(list, sort, desc) {
  const dir = desc ? -1 : 1;
  switch (sort) {
    case 'name':
      return list.sort((a, b) => dir * collator.compare(a.name || '', b.name || ''));
    case 'type':
      return list.sort((a, b) => dir * collator.compare(a.type || '', b.type || '') || collator.compare(a.name || '', b.name || ''));
    case 'latency':
      return byMetric(list, p => (p.latency_ms > 0 ? p.latency_ms : null), dir);
    case 'speed':
      return byMetric(list, p => (p.download_bps > 0 ? p.download_bps : null), -dir);
    case 'traffic':
      return byMetric(list, p => (p.traffic_up + p.traffic_down > 0 ? p.traffic_up + p.traffic_down : null), -dir);
    default:
      return desc ? list.reverse() : list;
  }
}

function updateSummary(u) {
  return [`${u.added || 0} added`, `${u.updated || 0} updated`, `${u.removed || 0} removed`, u.kept ? `${u.kept} kept` : '']
    .filter(Boolean).join(', ');
}

function ProfileRow({ p, checked, running, pending, onToggle, onConnect }) {
  const onRow = e => {
    if (e.target.closest('input, button, a, label')) return;
    onConnect(p);
  };
  return html`<tr class=${`prow${running ? ' running' : ''}${checked ? ' checked' : ''}`} onClick=${onRow}>
    <td class="c-sel">
      <label class="cell-check">
        <input type="checkbox" checked=${checked} aria-label=${`Select ${p.name}`}
          onClick=${e => onToggle(p.id, e.shiftKey)} />
      </label>
    </td>
    <td class="c-name">
      <button type="button" class="pname" onClick=${() => onConnect(p)}
        title=${running ? `${p.name} is running` : `Connect to ${p.name}`}>
        ${running && html`<span class="run-dot" aria-hidden="true"></span><span class="sr-only">Running: </span>`}
        ${pending && html`<${Spinner} small />`}
        <span class="ellipsis">${p.name || '(no name)'}</span>
      </button>
    </td>
    <td class="c-type"><span class="badge">${p.type}</span></td>
    <td class="c-addr"><span class="mono ellipsis" title=${p.address}>${p.address}</span></td>
    <td class="c-lat"><${Latency} p=${p} /></td>
    <td class="c-speed"><${Speed} p=${p} compact /></td>
    <td class="c-geo" title=${p.ip_out || undefined}><${Country} code=${p.country} />${p.ip_out && html` <span class="ip mono muted">${p.ip_out}</span>`}</td>
    <td class="c-traffic"><${Traffic} p=${p} compact /></td>
  </tr>`;
}

function JobBar({ job, byId, onCancel, blocked }) {
  const now = useNow(1000);
  const ids = job.profile_ids || [];
  let done = null;
  if (job.kind === 'latency' && ids.length && ids.every(id => byId.has(id))) {
    done = ids.filter(id => (byId.get(id).latency_at || 0) >= job.started_at).length;
  }
  return html`<div class="jobbar" role="status">
    <${Spinner} small />
    <div class="jobbar-text">
      <strong>${KIND_LABELS[job.kind] || job.kind} running</strong>
      <span class="muted"> · ${done !== null ? `${done} of ${ids.length}` : plural(ids.length, 'profile')} · ${formatDuration(now / 1000 - job.started_at)}</span>
    </div>
    ${done !== null && html`<progress max=${ids.length} value=${done} aria-label="Test progress"></progress>`}
    <button type="button" class="btn small" disabled=${blocked} onClick=${() => onCancel(job)}>Cancel</button>
  </div>`;
}

function GroupBar({ groups, activeId, onSelect }) {
  const bar = useRef(null);
  useEffect(() => {
    const el = bar.current && bar.current.querySelector('[aria-pressed="true"]');
    if (el && el.scrollIntoView) el.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  }, [activeId]);
  return html`<div class="group-bar" ref=${bar} role="group" aria-label="Groups">
    ${groups.map(g => html`<button key=${g.id} type="button"
        class=${`group-tab${g.id === activeId ? ' active' : ''}${g.archived ? ' archived' : ''}`}
        aria-pressed=${g.id === activeId ? 'true' : 'false'} onClick=${() => onSelect(g.id)}
        title=${`${g.name}${g.subscription ? ' (subscription)' : ''}${g.archived ? ' (archived)' : ''}`}>
      ${g.subscription && html`<${Icon} name="rss" size=${14} />`}
      <span class="gname">${g.name}</span>
      <span class="gcount">${g.profile_count}</span>
    </button>`)}
  </div>`;
}

export function ProfilesView() {
  const blocked = useBlocked();
  const adminDenied = useStore(s => s.adminDenied);
  const busy = useStore(s => s.busyAction);
  const runningId = useStore(s => (s.status && s.status.profile ? s.status.profile.id : -1));
  const now = useNow(60000);

  const groupsRes = useResource(() => api.get('api/v1/groups'), { topics: ['groups'], throttle: 1000 });
  const groups = (groupsRes.data && groupsRes.data.groups) || NO_GROUPS;
  const [groupId, setGroupId] = useState(() => {
    const saved = storageGet('throne.group');
    return saved === null ? null : Number(saved);
  });
  const group = groups.find(g => g.id === groupId) || (groupsRes.data ? groups[0] || null : null);
  const gid = group ? group.id : null;

  const profilesRes = useResource(
    () => (gid === null ? Promise.resolve(null) : api.get('api/v1/profiles', { group: gid })),
    { topics: ['profiles'], deps: [gid], throttle: 1500 },
  );
  const profiles = (profilesRes.data && profilesRes.data.profiles) || NO_PROFILES;
  const tests = useResource(() => api.get('api/v1/tests'), { topics: ['tests'] });
  const runningJob = ((tests.data && tests.data.jobs) || []).find(j => j.state === 'running') || null;

  const [filter, setFilter] = useState('');
  const [sort, setSort] = useState(() => storageGet('throne.sort', 'order'));
  const [desc, setDesc] = useState(() => storageGet('throne.sortDesc') === '1');
  const [limit, setLimit] = useState(PAGE);
  const [selected, setSelected] = useState(() => new Set());
  const [connectingId, setConnectingId] = useState(null);
  const [updating, setUpdating] = useState(null);
  const [dialog, setDialog] = useState(null);
  const lastClicked = useRef(-1);

  const q = filter.trim().toLowerCase();
  const shown = useMemo(() => sortProfiles(q ? profiles.filter(p => matches(p, q)) : profiles.slice(), sort, desc), [profiles, q, sort, desc]);
  const byId = useMemo(() => new Map(profiles.map(p => [p.id, p])), [profiles]);
  const visible = shown.slice(0, limit);

  useEffect(() => {
    setSelected(new Set());
    setLimit(PAGE);
    lastClicked.current = -1;
  }, [gid]);

  useEffect(() => {
    setLimit(PAGE);
  }, [q, sort, desc]);

  useEffect(() => {
    if (!selected.size) return;
    const next = new Set([...selected].filter(id => byId.has(id)));
    if (next.size !== selected.size) setSelected(next);
  }, [byId]);

  const selectGroup = id => {
    setGroupId(id);
    storageSet('throne.group', id);
  };
  const changeSort = value => {
    setSort(value);
    storageSet('throne.sort', value);
  };
  const toggleDesc = () => {
    setDesc(!desc);
    storageSet('throne.sortDesc', desc ? null : '1');
  };

  const toggle = (id, shift) => {
    const idx = shown.findIndex(p => p.id === id);
    const from = lastClicked.current;
    setSelected(prev => {
      const next = new Set(prev);
      if (shift && from >= 0 && idx >= 0 && from < shown.length) {
        const on = !prev.has(id);
        for (let i = Math.min(from, idx); i <= Math.max(from, idx); i++) {
          if (on) next.add(shown[i].id);
          else next.delete(shown[i].id);
        }
      } else if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
    lastClicked.current = idx;
  };
  const allShown = shown.length > 0 && shown.every(p => selected.has(p.id));
  const someShown = !allShown && shown.some(p => selected.has(p.id));
  const toggleAll = () => {
    setSelected(prev => {
      const next = new Set(prev);
      for (const p of shown) {
        if (allShown) next.delete(p.id);
        else next.add(p.id);
      }
      return next;
    });
  };

  const connect = async p => {
    if (blocked || busy || p.id === runningId) return;
    setConnectingId(p.id);
    await actions.connect(p.id);
    setConnectingId(null);
  };

  // What the test and clear actions act on: the selection, else the filtered list, else the whole group.
  const target = () => {
    if (selected.size) return { body: { profiles: profiles.filter(p => selected.has(p.id)).map(p => p.id) }, count: selected.size, what: 'selected' };
    if (q) return { body: { profiles: shown.map(p => p.id) }, count: shown.length, what: 'shown' };
    return { body: { group: gid }, count: profiles.length, what: 'group' };
  };
  const targetHint = () => {
    if (selected.size) return `${selected.size} selected`;
    if (q) return `${shown.length} shown`;
    return `all ${profiles.length} in the group`;
  };

  const runTest = async kind => {
    const t = target();
    if (!t.count) return;
    if (kind === 'speed' && t.count > 10) {
      const ok = await confirmDialog({
        title: 'Speed test',
        message: `Run a speed test on ${plural(t.count, 'profile')}? It downloads data through each of them, one after another, and takes a while.`,
        confirmLabel: 'Start',
      });
      if (!ok) return;
    }
    try {
      const job = await api.post('api/v1/tests', { kind, ...t.body });
      toast(`${KIND_LABELS[kind]} started for ${plural((job.profile_ids || []).length || t.count, 'profile')}.`);
      tests.reload();
    } catch (e) {
      toastError(e, `${KIND_LABELS[kind]} not started`);
    }
  };

  const cancelJob = async job => {
    try {
      await api.del(`api/v1/tests/${job.id}`);
      tests.reload();
    } catch (e) {
      toastError(e, 'Cancel failed');
    }
  };

  const clearResults = async () => {
    const t = target();
    if (!t.count) return;
    if (t.what === 'group') {
      const ok = await confirmDialog({
        title: 'Clear test results',
        message: `Clear the latency, speed, country and exit IP results of all ${plural(t.count, 'profile')} in ${group.name}?`,
        confirmLabel: 'Clear results',
      });
      if (!ok) return;
    }
    try {
      const r = await api.del('api/v1/results', t.body);
      toast(`Cleared the results of ${plural(r.cleared ?? t.count, 'profile')}.`, { kind: 'success' });
      profilesRes.reload();
    } catch (e) {
      toastError(e, 'Clearing failed');
    }
  };

  const deleteSelected = async () => {
    const ids = profiles.filter(p => selected.has(p.id)).map(p => p.id);
    if (!ids.length) return;
    const runningNote = ids.includes(runningId) ? ' The running profile is among them: Throne stops it first.' : '';
    const ok = await confirmDialog({
      title: 'Delete profiles',
      message: `Delete ${plural(ids.length, 'profile')} from ${group.name}? This cannot be undone.${runningNote}`,
      confirmLabel: 'Delete',
      danger: true,
    });
    if (!ok) return;
    try {
      const r = await api.del('api/v1/profiles', { profiles: ids });
      toast(r.state === 'stopping' ? 'Throne stops the running profile, then deletes the profiles.' : `Deleted ${plural(r.deleted ?? ids.length, 'profile')}.`, { kind: 'success' });
      setSelected(new Set());
      profilesRes.reload();
      groupsRes.reload();
    } catch (e) {
      toastError(e, 'Delete failed');
    }
  };

  const connectBest = () => {
    if (gid === null) return;
    actions.connectBest(gid, q ? escapeRegExp(filter.trim()) : '');
  };

  const updateGroup = async g => {
    setUpdating(g.id);
    try {
      const r = await api.post(`api/v1/groups/${g.id}/update`, { timeout: 60 });
      const u = r.update;
      if (!u) toast(`${g.name}: the update carries on in Throne.`);
      else if (u.ok === false) toast(`${g.name}: the update failed.`, { kind: 'error', detail: u.error || '' });
      else toast(`${g.name} updated: ${updateSummary(u)}.`, { kind: 'success' });
      groupsRes.reload();
      profilesRes.reload();
    } catch (e) {
      toastError(e, `${g.name}: update failed`);
    } finally {
      setUpdating(null);
    }
  };

  const updateAll = async () => {
    setUpdating('all');
    try {
      const r = await api.post('api/v1/groups/update', { timeout: 120 });
      const results = r.results || [];
      const nameOf = id => (groups.find(g => g.id === id) || { name: `Group ${id}` }).name;
      const failed = results.filter(x => !x.ok);
      if (r.queued) toast('The subscription updates carry on in Throne.');
      else if (!results.length) toast('There is no subscription to update.');
      else if (failed.length) {
        toast(`${failed.length} of ${plural(results.length, 'subscription')} failed to update.`, {
          kind: 'error',
          detail: failed.map(f => `${nameOf(f.id)}: ${f.error || 'failed'}`).join('; '),
        });
      } else toast(`${plural(results.length, 'subscription')} updated.`, { kind: 'success' });
      groupsRes.reload();
      profilesRes.reload();
    } catch (e) {
      toastError(e, 'Update failed');
    } finally {
      setUpdating(null);
    }
  };

  const deleteGroup = async g => {
    const ok = await confirmDialog({
      title: 'Delete group',
      message: `Delete ${g.name} and its ${plural(g.profile_count, 'profile')}? This cannot be undone.`,
      confirmLabel: 'Delete group',
      danger: true,
    });
    if (!ok) return;
    try {
      const r = await api.del(`api/v1/groups/${g.id}`);
      toast(r.state === 'stopping' ? `Throne stops the running profile, then deletes ${g.name}.` : `Deleted ${g.name}.`, { kind: 'success' });
      groupsRes.reload();
    } catch (e) {
      toastError(e, 'Delete failed');
    }
  };

  if (groupsRes.data === undefined) {
    return html`<div class="view"><h1 class="view-title">Profiles</h1>
      ${groupsRes.error ? html`<${ErrorBox} error=${groupsRes.error} onRetry=${groupsRes.reload} />` : html`<${Loading} />`}</div>`;
  }

  const adminTitle = adminDenied ? 'Needs admin rights on Throne' : undefined;
  const hasSubs = groups.some(g => g.subscription && !g.archived);
  const groupMenu = group ? [
    { label: 'Add group', icon: 'plus', onSelect: () => setDialog('group'), disabled: blocked || adminDenied },
    { label: 'Add subscription', icon: 'rss', onSelect: () => setDialog('subscription'), disabled: blocked || adminDenied },
    { label: 'Update all subscriptions', icon: 'refresh', onSelect: updateAll, disabled: blocked || !hasSubs || updating !== null },
    { separator: true },
    { label: `Delete ${group.name}`, icon: 'trash', danger: true, onSelect: () => deleteGroup(group), disabled: blocked || adminDenied || groups.length < 2 },
  ] : [];
  const testItems = [
    { label: 'Latency test', icon: 'activity', hint: targetHint(), onSelect: () => runTest('latency') },
    { label: 'IP test', icon: 'globe', hint: targetHint(), onSelect: () => runTest('ip') },
    { label: 'Speed test', icon: 'download', hint: targetHint(), onSelect: () => runTest('speed') },
    { separator: true },
    { label: 'Clear results', icon: 'x', hint: targetHint(), onSelect: clearResults },
  ];
  const noTarget = selected.size === 0 && (q ? shown.length === 0 : profiles.length === 0);

  return html`<div class="view view-profiles">
    <h1 class="view-title sr-only">Profiles</h1>
    ${groupsRes.error && html`<${ErrorBox} error=${groupsRes.error} compact onRetry=${groupsRes.reload} />`}
    <${GroupBar} groups=${groups} activeId=${gid} onSelect=${selectGroup} />
    ${group && html`<div class="group-head">
      <div class="group-info">
        <h2>${group.name}</h2>
        ${group.subscription && html`<span class="badge accent">Subscription</span>`}
        ${group.archived && html`<span class="badge">Archived</span>`}
        <span class="muted small" title=${group.subscription ? formatDate(group.updated_at) : undefined}>
          ${plural(group.profile_count, 'profile')}${group.subscription ? ` · updated ${timeAgo(group.updated_at, now)}` : ''}
        </span>
      </div>
      <div class="group-actions">
        ${group.subscription && html`<button type="button" class="btn" disabled=${blocked || updating !== null || group.archived}
            onClick=${() => updateGroup(group)}>
          ${updating === group.id ? html`<${Spinner} small />` : html`<${Icon} name="refresh" />`}<span class="hide-xs">Update</span>
        </button>`}
        <button type="button" class="btn" disabled=${blocked || adminDenied} title=${adminTitle} onClick=${() => setDialog('import')}>
          <${Icon} name="plus" /><span>Import</span>
        </button>
        <${Menu} icon="more" ariaLabel="More group actions" buttonClass="icon-btn" items=${groupMenu} />
      </div>
    </div>`}
    ${runningJob && html`<${JobBar} job=${runningJob} byId=${byId} onCancel=${cancelJob} blocked=${blocked} />`}
    <div class="toolbar">
      <div class="search">
        <${Icon} name="search" size=${16} />
        <input type="search" class="input" placeholder="Filter by name, address, type…" aria-label="Filter profiles"
          value=${filter} onInput=${e => setFilter(e.currentTarget.value)} />
      </div>
      <select class="input sort" aria-label="Sort by" value=${sort} onChange=${e => changeSort(e.currentTarget.value)}>
        ${SORTS.map(([id, label]) => html`<option key=${id} value=${id}>${label}</option>`)}
      </select>
      <button type="button" class="icon-btn" aria-label="Reverse the order" aria-pressed=${desc ? 'true' : 'false'}
        title="Reverse the order" onClick=${toggleDesc}><${Icon} name=${desc ? 'arrowUp' : 'arrowDown'} /></button>
      <${Menu} label="Test" icon="activity" items=${testItems} disabled=${blocked || noTarget} />
      <button type="button" class="btn" disabled=${blocked || !!busy || gid === null || !profiles.length}
        title=${q ? 'Test the profiles whose name matches the filter, then start the fastest' : 'Test the group, then start the fastest profile'}
        onClick=${connectBest}><${Icon} name="zap" /><span class="hide-xs">Connect best</span></button>
    </div>
    ${selected.size > 0 && html`<div class="selbar" role="region" aria-label="Selected profiles">
      <span><strong>${selected.size}</strong> selected</span>
      <button type="button" class="btn small" disabled=${blocked} onClick=${() => runTest('latency')}>Latency</button>
      <button type="button" class="btn small" disabled=${blocked} onClick=${() => runTest('ip')}>IP</button>
      <button type="button" class="btn small" disabled=${blocked} onClick=${() => runTest('speed')}>Speed</button>
      <button type="button" class="btn small" disabled=${blocked} onClick=${clearResults}>Clear results</button>
      <button type="button" class="btn small danger" disabled=${blocked || adminDenied} title=${adminTitle} onClick=${deleteSelected}>
        <${Icon} name="trash" size=${16} />Delete
      </button>
      <button type="button" class="icon-btn small" aria-label="Clear the selection" onClick=${() => setSelected(new Set())}><${Icon} name="x" size=${16} /></button>
    </div>`}
    ${profilesRes.error && html`<${ErrorBox} error=${profilesRes.error} onRetry=${profilesRes.reload} />`}
    ${profilesRes.data === undefined && !profilesRes.error && html`<${Loading} />`}
    ${profilesRes.data !== undefined && html`
      <div class="list-head">
        <label class="check">
          <input type="checkbox" checked=${allShown} indeterminate=${someShown} disabled=${!shown.length} onChange=${toggleAll} />
          Select all${q ? ' shown' : ''}
        </label>
        <span class="muted small">${q ? `${shown.length} of ${profiles.length} shown` : plural(profiles.length, 'profile')}</span>
      </div>
      ${!profiles.length && html`<p class="empty">This group is empty. Import profiles into it${group && group.subscription ? ', or update its subscription' : ''}.</p>`}
      ${profiles.length > 0 && !shown.length && html`<p class="empty">No profile matches “${filter.trim()}”.</p>`}
      ${shown.length > 0 && html`<div class="table-wrap">
        <table class="ptable">
          <thead>
            <tr>
              <th class="c-sel"><span class="sr-only">Selected</span></th>
              <th class="c-name">Name</th>
              <th class="c-type">Type</th>
              <th class="c-addr">Address</th>
              <th class="c-lat">Latency</th>
              <th class="c-speed">Speed</th>
              <th class="c-geo">Exit</th>
              <th class="c-traffic">Traffic</th>
            </tr>
          </thead>
          <tbody>
            ${visible.map(p => html`<${ProfileRow} key=${p.id} p=${p} checked=${selected.has(p.id)}
              running=${p.id === runningId} pending=${p.id === connectingId} onToggle=${toggle} onConnect=${connect} />`)}
          </tbody>
        </table>
      </div>`}
      ${shown.length > limit && html`<button type="button" class="btn block more" onClick=${() => setLimit(l => l + PAGE)}>
        Show more (${shown.length - limit} more)
      </button>`}
    `}
    ${dialog === 'import' && html`<${ImportDialog} groups=${groups} groupId=${gid} onClose=${() => setDialog(null)}
      onDone=${() => { profilesRes.reload(); groupsRes.reload(); }} />`}
    ${(dialog === 'group' || dialog === 'subscription') && html`<${GroupDialog} subscription=${dialog === 'subscription'}
      onClose=${() => setDialog(null)} onCreated=${g => { setDialog(null); groupsRes.reload(); if (g) selectGroup(g.id); }} />`}
  </div>`;
}

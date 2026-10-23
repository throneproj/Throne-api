import { html, useState } from '../ui.js';
import { api } from '../client.js';
import { toast } from '../store.js';
import { plural, formatBytes } from '../format.js';
import { Dialog, ErrorBox, Spinner } from '../components/common.js';

const MAX_IMPORT_BYTES = 768 * 1024;

function eligible(g) {
  return !g.archived && !g.subscription;
}

function ImportResult({ result, groups }) {
  if (result.state === 'queued') {
    return html`<div class="notice info"><div class="notice-body"><strong>The import carries on in Throne.</strong><span>The profiles appear when it has finished.</span></div></div>`;
  }
  const group = groups.find(g => g.id === result.group);
  const issues = result.issues || [];
  const errors = result.errors || [];
  return html`<div class="import-result">
    <p><strong>${plural(result.imported || 0, 'profile')} imported</strong>${group ? ` into ${group.name}` : ''}.
      ${result.failed ? ` ${result.failed} could not be imported.` : ''}${result.warned ? ` ${result.warned} imported with a warning.` : ''}</p>
    ${errors.length > 0 && html`<ul class="issue-list">${errors.map((e, i) => html`<li key=${i} class="issue error">${e}</li>`)}</ul>`}
    ${issues.length > 0 && html`<ul class="issue-list">
      ${issues.map((it, i) => html`<li key=${i} class=${`issue ${it.severity}`}>
        <div><span class="badge">${it.severity === 'error' ? 'Not imported' : 'Warning'}</span>
          ${it.field && html` <code>${it.field}</code>`} ${it.message}${it.count > 1 ? ` (${it.count}×)` : ''}</div>
        ${(it.examples || []).length > 0 && html`<ul class="examples">${it.examples.map((ex, j) => html`<li key=${j} class="mono">${ex}</li>`)}</ul>`}
      </li>`)}
    </ul>`}
  </div>`;
}

export function ImportDialog({ groups, groupId, onClose, onDone }) {
  const targets = groups.filter(eligible);
  const initial = targets.some(g => g.id === groupId) ? groupId : targets.length ? targets[0].id : groupId;
  const [mode, setMode] = useState('text');
  const [text, setText] = useState('');
  const [url, setUrl] = useState('');
  const [file, setFile] = useState(null);
  const [target, setTarget] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const [result, setResult] = useState(null);

  const ready = mode === 'text' ? text.trim() !== '' : mode === 'url' ? /^https?:\/\/\S+$/i.test(url.trim()) : !!file;

  const submit = async e => {
    if (e) e.preventDefault();
    if (!ready || busy) return;
    setError(null);
    let body;
    if (mode === 'url') {
      body = { as: 'url', url: url.trim() };
    } else if (mode === 'file') {
      if (file.size > MAX_IMPORT_BYTES) {
        setError({ code: 'too_big', detail: `The file is ${formatBytes(file.size)}; the web UI sends at most ${formatBytes(MAX_IMPORT_BYTES)}. Import it with thronectl or in Throne's window.` });
        return;
      }
      let content;
      try {
        content = await file.text();
      } catch {
        setError({ code: 'read_failed', detail: 'The browser could not read the file.' });
        return;
      }
      body = { as: 'profiles', text: content };
    } else {
      body = { as: 'profiles', text };
    }
    if (target !== null && target !== undefined) body.group = target;
    body.timeout = 60;
    setBusy(true);
    try {
      const r = await api.post('api/v1/import', body);
      setResult(r);
      if (r.state !== 'queued') toast(`${plural(r.imported || 0, 'profile')} imported.`, { kind: 'success' });
      if (onDone) onDone(r);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const footer = result
    ? html`<button type="button" class="btn" onClick=${() => { setResult(null); setText(''); setUrl(''); setFile(null); }}>Import more</button>
        <button type="button" class="btn primary" onClick=${onClose}>Done</button>`
    : html`<button type="button" class="btn" onClick=${onClose} disabled=${busy}>Cancel</button>
        <button type="submit" form="import-form" class="btn primary" disabled=${!ready || busy}>
          ${busy ? html`<${Spinner} small /> Importing…` : 'Import'}
        </button>`;

  return html`<${Dialog} title="Import profiles" onClose=${onClose} footer=${footer} busy=${busy} wide>
    ${result
      ? html`<${ImportResult} result=${result} groups=${groups} />`
      : html`<form id="import-form" onSubmit=${submit}>
        <div class="segmented" role="radiogroup" aria-label="Import from">
          ${[['text', 'Text'], ['file', 'File'], ['url', 'URL']].map(([id, label]) => html`<label key=${id} class=${mode === id ? 'seg active' : 'seg'}>
            <input type="radio" name="import-mode" value=${id} checked=${mode === id} onChange=${() => setMode(id)} />${label}
          </label>`)}
        </div>
        ${mode === 'text' && html`<div class="field">
          <label for="import-text">Share links, a subscription body, or a Clash, sing-box, Xray, WireGuard or OpenVPN configuration</label>
          <textarea id="import-text" class="input mono" rows="8" spellcheck=${false} value=${text} data-autofocus
            onInput=${e => setText(e.currentTarget.value)} placeholder="vless://…"></textarea>
        </div>`}
        ${mode === 'file' && html`<div class="field">
          <label for="import-file">File</label>
          <input id="import-file" class="input" type="file" onChange=${e => setFile(e.currentTarget.files[0] || null)} />
          <small class="muted">Read in this browser and sent as text, up to ${formatBytes(MAX_IMPORT_BYTES)}.</small>
        </div>`}
        ${mode === 'url' && html`<div class="field">
          <label for="import-url">URL</label>
          <input id="import-url" class="input" type="url" inputmode="url" spellcheck=${false} placeholder="https://…"
            value=${url} onInput=${e => setUrl(e.currentTarget.value)} />
          <small class="muted">Throne downloads it once and imports what it returns. For a subscription, add a subscription group instead.</small>
        </div>`}
        <div class="field">
          <label for="import-group">Into group</label>
          <select id="import-group" class="input" value=${target === null || target === undefined ? '' : String(target)}
            onChange=${e => setTarget(Number(e.currentTarget.value))}>
            ${groups.map(g => html`<option key=${g.id} value=${String(g.id)} disabled=${!eligible(g)}>
              ${g.name}${g.subscription ? ' (subscription)' : ''}${g.archived ? ' (archived)' : ''}
            </option>`)}
          </select>
        </div>
        <${ErrorBox} error=${error} />
      </form>`}
  <//>`;
}

export function GroupDialog({ subscription, onClose, onCreated }) {
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const [autoUpdate, setAutoUpdate] = useState(true);
  const [updateNow, setUpdateNow] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const urlOk = !subscription || /^https?:\/\/\S+$/i.test(url.trim());
  const ready = name.trim() !== '' && urlOk;

  const submit = async e => {
    e.preventDefault();
    if (!ready || busy) return;
    const body = { name: name.trim() };
    if (subscription) Object.assign(body, { url: url.trim(), auto_update: autoUpdate, update: updateNow, timeout: 60 });
    setBusy(true);
    setError(null);
    try {
      const r = await api.post('api/v1/groups', body);
      const u = r.update;
      if (u && u.ok === false) toast(`Added ${body.name}, but its first update failed.`, { kind: 'error', detail: u.error || '' });
      else if (u) toast(`Added ${body.name}: ${plural(u.added || 0, 'profile')}.`, { kind: 'success' });
      else if (subscription && updateNow) toast(`Added ${body.name}. Its first update carries on in Throne.`);
      else toast(`Added ${body.name}.`, { kind: 'success' });
      onCreated(r.group);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  const footer = html`<button type="button" class="btn" onClick=${onClose} disabled=${busy}>Cancel</button>
    <button type="submit" form="group-form" class="btn primary" disabled=${!ready || busy}>
      ${busy ? html`<${Spinner} small /> Adding…` : subscription ? 'Add subscription' : 'Add group'}
    </button>`;

  return html`<${Dialog} title=${subscription ? 'Add subscription' : 'Add group'} onClose=${onClose} footer=${footer} busy=${busy}>
    <form id="group-form" onSubmit=${submit}>
      <div class="field">
        <label for="group-name">Name</label>
        <input id="group-name" class="input" value=${name} onInput=${e => setName(e.currentTarget.value)} required data-autofocus />
      </div>
      ${subscription && html`
        <div class="field">
          <label for="group-url">Subscription URL</label>
          <input id="group-url" class="input" type="url" inputmode="url" spellcheck=${false} placeholder="https://…"
            value=${url} onInput=${e => setUrl(e.currentTarget.value)} required />
          <small class="muted">An http or https URL. The web UI never shows it again.</small>
        </div>
        <label class="check"><input type="checkbox" checked=${autoUpdate} onChange=${e => setAutoUpdate(e.currentTarget.checked)} /> Take part in automatic updates</label>
        <label class="check"><input type="checkbox" checked=${updateNow} onChange=${e => setUpdateNow(e.currentTarget.checked)} /> Download it now</label>`}
      <${ErrorBox} error=${error} />
    </form>
  <//>`;
}

import { html, useState, useRef } from '../ui.js';
import { api, request, uploadFile, saveBlob, filenameFrom } from '../client.js';
import { useStore, useBlocked, getState, setState, toast, toastError, confirmDialog } from '../store.js';
import { waitForRestart } from '../session.js';
import { formatBytes, plural } from '../format.js';
import { Icon } from '../components/icons.js';
import { ErrorBox, Notice, Spinner } from '../components/common.js';

const PARTS = [
  { id: 'profiles', label: 'Groups and profiles' },
  { id: 'routes', label: 'Routing profiles and their rules' },
  { id: 'settings', label: 'Settings' },
  { id: 'otp', label: 'OTP accounts, secrets included' },
  { id: 'ip_lists', label: 'IP lists and IP scans' },
  { id: 'icons', label: 'Icon files' },
];
const PART_LABELS = Object.fromEntries(PARTS.map(p => [p.id, p.label]));
const PLATFORMS = { winnt: 'Windows', linux: 'Linux', darwin: 'macOS' };
const MAX_RESTORE_BYTES = 512 * 1024 * 1024;

let uploadHandle = null;
let pollToken = null;

function patchRestore(patch) {
  setState(s => ({ restore: { ...(s.restore || {}), ...patch } }));
}

function defaultParts(parts) {
  const picked = parts.filter(p => p !== 'settings' && p !== 'icons');
  return picked.length ? picked : parts.slice();
}

function takeUpload(up) {
  if (up.state === 'complete' && up.archive) {
    patchRestore({ phase: 'ready', upload: up, parts: defaultParts(up.archive.parts || []), error: null });
    return true;
  }
  if (up.state === 'failed') {
    patchRestore({ phase: 'failed', upload: up, error: { code: 'invalid_archive', detail: up.error || '' } });
    return true;
  }
  patchRestore({ phase: 'inspecting', upload: up });
  return false;
}

function pollUpload(id) {
  const token = {};
  pollToken = token;
  const tick = async () => {
    if (pollToken !== token) return;
    const r = getState().restore;
    if (!r || !r.upload || r.upload.id !== id) return;
    try {
      const up = await api.get(`api/v1/restores/${id}`);
      if (pollToken !== token) return;
      if (takeUpload(up)) return;
    } catch (e) {
      if (pollToken !== token) return;
      patchRestore({ phase: 'failed', error: e });
      return;
    }
    setTimeout(tick, 1500);
  };
  setTimeout(tick, 1000);
}

async function startUpload(file) {
  pollToken = null;
  if (!file.size || file.size > MAX_RESTORE_BYTES) {
    const detail = file.size
      ? `The web UI server takes archives up to ${formatBytes(MAX_RESTORE_BYTES)}; this one is ${formatBytes(file.size)}. Restore it in Throne's window.`
      : 'The file is empty.';
    const code = file.size ? 'too_large' : 'empty_file';
    setState({ restore: { phase: 'failed', fileName: file.name, size: file.size, error: { code, detail } } });
    return;
  }
  setState({ restore: { phase: 'uploading', fileName: file.name, size: file.size, progress: 0 } });
  uploadHandle = uploadFile('api/restore', file, p => patchRestore({ progress: p }));
  try {
    const up = await uploadHandle.promise;
    uploadHandle = null;
    if (!takeUpload(up)) pollUpload(up.id);
  } catch (e) {
    uploadHandle = null;
    if (e && e.name === 'AbortError') setState({ restore: null });
    else patchRestore({ phase: 'failed', error: e });
  }
}

async function discardRestore() {
  const r = getState().restore;
  pollToken = null;
  if (uploadHandle) {
    uploadHandle.abort();
    return;
  }
  setState({ restore: null });
  if (r && r.upload && r.upload.id) {
    try {
      await api.del(`api/v1/restores/${r.upload.id}`, undefined, { quiet: true });
    } catch {
      // an expired or refused upload is gone either way
    }
  }
}

async function applyRestore() {
  const r = getState().restore;
  if (!r || !r.upload || !r.parts || !r.parts.length) return;
  const names = r.parts.map(p => PART_LABELS[p] || p).join(', ');
  const ok = await confirmDialog({
    title: 'Restore from the archive',
    message: html`<div class="prose">
      <p>Throne replaces these parts of its data with the archive's: <strong>${names}</strong>.</p>
      <p>It stops the running profile first and restarts when it is done. This cannot be undone.</p>
      ${r.parts.includes('settings') && html`<p class="small muted">Settings that belong to this device stay as they are: the Throne API, the inbound and ports, Tun, the kill switch, remembering the last profile, NTP, logging and the system proxy.</p>`}
    </div>`,
    confirmLabel: 'Restore and restart',
    danger: true,
  });
  if (!ok) return;
  patchRestore({ phase: 'applying', error: null });
  try {
    const res = await api.post(`api/v1/restores/${r.upload.id}/apply`, { parts: r.parts, timeout: 60 });
    setState({ restore: null });
    if (res.skipped_rules) toast(`${plural(res.skipped_rules, 'routing rule')} of the archive could not be read and were skipped.`);
    if (res.restarting) waitForRestart('Throne is restarting after the restore…');
    else if (res.state === 'applying') waitForRestart('Throne is restoring, then restarts…', { upGraceMs: 180000 });
    else toast('Restored.', { kind: 'success' });
  } catch (e) {
    patchRestore({ phase: 'ready', error: e });
  }
}

function BackupCard({ disabled }) {
  const [parts, setParts] = useState(() => new Set(PARTS.map(p => p.id)));
  const [busy, setBusy] = useState(false);
  const toggle = id => setParts(prev => {
    const next = new Set(prev);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    return next;
  });
  const download = async () => {
    setBusy(true);
    try {
      const res = await request('POST', 'api/backup', { body: { parts: PARTS.map(p => p.id).filter(id => parts.has(id)) }, raw: true });
      const blob = await res.blob();
      saveBlob(blob, filenameFrom(res, 'throne-backup.thrbackup'));
      toast(`Backup saved (${formatBytes(blob.size)}).`, { kind: 'success' });
    } catch (e) {
      toastError(e, 'Backup failed');
    } finally {
      setBusy(false);
    }
  };
  return html`<section class="card" aria-labelledby="backup-title">
    <div class="card-head"><h2 id="backup-title">Back up</h2></div>
    <fieldset class="parts" disabled=${busy}>
      <legend>Parts</legend>
      ${PARTS.map(p => html`<label key=${p.id} class="check">
        <input type="checkbox" checked=${parts.has(p.id)} onChange=${() => toggle(p.id)} />${p.label}
      </label>`)}
    </fieldset>
    <div class="actions">
      <button type="button" class="btn primary" disabled=${disabled || busy || !parts.size} onClick=${download}>
        ${busy ? html`<${Spinner} small />` : html`<${Icon} name="download" />`}${busy ? 'Building the archive…' : 'Download backup'}
      </button>
    </div>
  </section>`;
}

function RestoreCard({ disabled }) {
  const restore = useStore(s => s.restore);
  const input = useRef(null);
  const [file, setFile] = useState(null);
  const phase = restore ? restore.phase : 'idle';
  const archive = restore && restore.upload && restore.upload.archive;

  const togglePart = id => {
    const cur = new Set(restore.parts || []);
    if (cur.has(id)) cur.delete(id);
    else cur.add(id);
    patchRestore({ parts: archive.parts.filter(p => cur.has(p)) });
  };

  const choose = () => {
    if (!file) return;
    startUpload(file);
    setFile(null);
    if (input.current) input.current.value = '';
  };

  return html`<section class="card" aria-labelledby="restore-title">
    <div class="card-head"><h2 id="restore-title">Restore</h2></div>
    ${(phase === 'idle' || phase === 'failed') && html`
      <div class="field">
        <label for="restore-file">Backup archive (.thrbackup)</label>
        <input id="restore-file" ref=${input} class="input" type="file" accept=".thrbackup,application/octet-stream"
          disabled=${disabled} onChange=${e => setFile(e.currentTarget.files[0] || null)} />
      </div>
      ${phase === 'failed' && html`<${ErrorBox} error=${restore.error} />`}
      <div class="actions">
        <button type="button" class="btn primary" disabled=${disabled || !file} onClick=${choose}><${Icon} name="upload" />Upload</button>
        ${phase === 'failed' && html`<button type="button" class="btn" onClick=${discardRestore}>Dismiss</button>`}
      </div>`}
    ${phase === 'uploading' && html`
      <p>Uploading <strong>${restore.fileName}</strong> (${formatBytes(restore.size)})…</p>
      <progress max="1" value=${restore.progress || 0} aria-label="Upload progress"></progress>
      <p class="muted small">${Math.round((restore.progress || 0) * 100)}%${restore.progress >= 1 ? ' — Throne checks the archive…' : ''}</p>
      <div class="actions"><button type="button" class="btn" onClick=${discardRestore}>Cancel</button></div>`}
    ${phase === 'inspecting' && html`<p><${Spinner} small /> Throne checks the archive…</p>
      <div class="actions"><button type="button" class="btn" onClick=${discardRestore}>Discard</button></div>`}
    ${(phase === 'ready' || phase === 'applying') && archive && html`
      <dl class="facts">
        <dt>File</dt><dd>${restore.fileName || '—'}</dd>
        <dt>Made</dt><dd>${archive.created_at || '—'}</dd>
        <dt>On</dt><dd>${PLATFORMS[archive.platform] || archive.platform || '—'}</dd>
        <dt>Size</dt><dd>${formatBytes(restore.upload.size)}</dd>
      </dl>
      <fieldset class="parts" disabled=${phase === 'applying'}>
        <legend>Restore these parts</legend>
        ${archive.parts.map(p => html`<label key=${p} class="check">
          <input type="checkbox" checked=${(restore.parts || []).includes(p)} onChange=${() => togglePart(p)} />${PART_LABELS[p] || p}
        </label>`)}
      </fieldset>
      ${(restore.parts || []).includes('settings') && html`<p class="muted small">Settings that belong to this device stay as they are: the Throne API, the inbound and ports, Tun, the kill switch, NTP, logging and the system proxy.</p>`}
      <${ErrorBox} error=${restore.error} />
      <div class="actions">
        <button type="button" class="btn danger" disabled=${disabled || phase === 'applying' || !(restore.parts || []).length} onClick=${applyRestore}>
          ${phase === 'applying' ? html`<${Spinner} small />` : html`<${Icon} name="restart" />`}${phase === 'applying' ? 'Restoring…' : 'Restore and restart'}
        </button>
        <button type="button" class="btn" disabled=${phase === 'applying'} onClick=${discardRestore}>Discard</button>
      </div>`}
  </section>`;
}

export function BackupView() {
  const adminDenied = useStore(s => s.adminDenied);
  const blocked = useBlocked();
  const disabled = adminDenied || blocked;
  return html`<div class="view">
    <h1 class="view-title">Backup and restore</h1>
    <${Notice} kind="warn" icon="alert">
      <strong>Archives hold credentials in plain text.</strong>
      <span>They contain the profiles' credentials and the OTP secrets. Keep them where only you can read them.</span>
    <//>
    ${adminDenied && html`<${Notice} kind="info" icon="shield">Backups and restores need admin rights on Throne.<//>`}
    <div class="grid two">
      <${BackupCard} disabled=${disabled} />
      <${RestoreCard} disabled=${disabled} />
    </div>
  </div>`;
}

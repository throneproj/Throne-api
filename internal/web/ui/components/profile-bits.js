import { html } from '../ui.js';
import { latencyInfo, speedText, flagEmoji, formatBytes, timeAgo } from '../format.js';

export function Latency({ p }) {
  const info = latencyInfo(p.latency_ms);
  const when = p.latency_at ? `measured ${timeAgo(p.latency_at)}` : '';
  const title = [info.title, when].filter(Boolean).join(', ');
  return html`<span class=${`lat ${info.cls}`} title=${title || undefined}>${info.text}</span>`;
}

export function Country({ code }) {
  if (!code) return null;
  return html`<span class="country" title=${code}><span aria-hidden="true">${flagEmoji(code)}</span> ${code}</span>`;
}

// compact shows one value (download, else upload) and puts both in the tooltip.
export function Speed({ p, compact = false }) {
  const down = speedText(p.download_bps);
  const up = speedText(p.upload_bps);
  if (!down && !up) return null;
  const title = [down && `Download ${down}`, up && `Upload ${up}`].filter(Boolean).join(', ');
  if (compact) return html`<span class="speed" title=${title}>${down ? `↓ ${down}` : `↑ ${up}`}</span>`;
  return html`<span class="speed">${down && html`<span title="Download">↓ ${down}</span>`}${up && html`<span title="Upload">↑ ${up}</span>`}</span>`;
}

export function Traffic({ p, compact = false }) {
  if (!p.traffic_up && !p.traffic_down) return null;
  const title = `Sent ${formatBytes(p.traffic_up)}, received ${formatBytes(p.traffic_down)}`;
  if (compact) return html`<span class="traffic" title=${title}>${formatBytes(p.traffic_up + p.traffic_down)}</span>`;
  return html`<span class="traffic" title=${title}>↑ ${formatBytes(p.traffic_up)} ↓ ${formatBytes(p.traffic_down)}</span>`;
}

export function ProfileSummary({ p }) {
  return html`<div class="profile-summary">
    <div class="ps-name">${p.name || '(no name)'}</div>
    <div class="ps-meta">
      <span class="badge">${p.type}</span>
      <span class="mono ellipsis">${p.address}</span>
    </div>
    <div class="ps-meta">
      <${Latency} p=${p} />
      <${Country} code=${p.country} />
      ${p.ip_out && html`<span class="mono">${p.ip_out}</span>`}
      <${Speed} p=${p} />
    </div>
  </div>`;
}

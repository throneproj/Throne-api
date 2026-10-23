import { html } from '../ui.js';
import { formatRate } from '../format.js';

const W = 600;

function segments(points, gapMs) {
  const out = [];
  let cur = [];
  for (const p of points) {
    if (cur.length && p.t - cur[cur.length - 1].t > gapMs) {
      out.push(cur);
      cur = [];
    }
    cur.push(p);
  }
  if (cur.length) out.push(cur);
  return out;
}

// samples: [{ t, [key]: bytesPerSecond }]; series: [{ key, label, cls, area }]
export function RateChart({ samples, series, height = 120, windowMs = 120000, label = 'Traffic rate' }) {
  const end = samples.length ? samples[samples.length - 1].t : Date.now();
  const start = end - windowMs;
  const points = samples.filter(s => s.t >= start);
  let max = 1024;
  for (const s of points) {
    for (const { key } of series) if (s[key] > max) max = s[key];
  }
  max *= 1.15;
  const H = height;
  const x = t => (((t - start) / windowMs) * W).toFixed(1);
  const y = v => (H - (Math.max(0, v) / max) * H).toFixed(1);
  const parts = segments(points, 5000).filter(seg => seg.length > 1);
  return html`<div class="chart">
    <svg class="chart-svg" viewBox=${`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-label=${label}>
      ${[0.25, 0.5, 0.75].map(f => html`<line key=${f} class="chart-grid" x1="0" x2=${W} y1=${H * f} y2=${H * f} />`)}
      ${series.map(({ key, cls, area }) => parts.map((seg, i) => {
        const line = seg.map(p => `${x(p.t)},${y(p[key])}`).join(' ');
        return html`<g key=${`${key}-${i}`} class=${`chart-series ${cls}`}>
          ${area && html`<polygon class="chart-area" points=${`${x(seg[0].t)},${H} ${line} ${x(seg[seg.length - 1].t)},${H}`} />`}
          <polyline class="chart-line" points=${line} vector-effect="non-scaling-stroke" />
        </g>`;
      }))}
    </svg>
    <div class="chart-scale" aria-hidden="true"><span>${formatRate(max)}</span><span>last ${Math.round(windowMs / 60000)} min</span></div>
  </div>`;
}

export function ChartLegend({ series, sample }) {
  return html`<ul class="chart-legend">
    ${series.map(({ key, label, cls }) => html`<li key=${key} class=${cls}>
      <span class="swatch" aria-hidden="true"></span>
      <span>${label}</span>
      <strong>${formatRate(sample ? sample[key] : 0)}</strong>
    </li>`)}
  </ul>`;
}

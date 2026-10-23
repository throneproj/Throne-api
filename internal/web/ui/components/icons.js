import { html } from '../ui.js';

// 24×24 stroke icons drawn with currentColor.
const PATHS = {
  activity: 'M3 12h4l3-8 4 16 3-8h4',
  list: 'M9 6h12M9 12h12M9 18h12M4 6h.01M4 12h.01M4 18h.01',
  chart: 'M4 20V10M10 20V4M16 20v-7M21 20H3',
  terminal: 'M4 17l6-5-6-5M12 19h8',
  sliders: 'M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6',
  archive: 'M21 8v13H3V8M1 3h22v5H1zM10 12h4',
  server: 'M3 4h18v7H3zM3 13h18v7H3zM7 7.5h.01M7 16.5h.01',
  power: 'M18.36 6.64a9 9 0 1 1-12.73 0M12 2v10',
  plus: 'M12 5v14M5 12h14',
  refresh: 'M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15',
  trash: 'M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v6M14 11v6',
  x: 'M18 6L6 18M6 6l12 12',
  more: 'M5 12h.01M12 12h.01M19 12h.01',
  search: 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16zM21 21l-4.35-4.35',
  download: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3',
  upload: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12',
  copy: 'M9 9h11v11H9zM5 15H4V4h11v1',
  logout: 'M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9',
  rss: 'M4 11a9 9 0 0 1 9 9M4 4a16 16 0 0 1 16 16M5 19h.01',
  alert: 'M12 9v4M12 17h.01M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z',
  info: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16v-4M12 8h.01',
  zap: 'M13 2L3 14h9l-1 8 10-12h-9l1-8z',
  play: 'M6 4l14 8-14 8z',
  stop: 'M6 6h12v12H6z',
  pause: 'M6 4h4v16H6zM14 4h4v16h-4z',
  check: 'M20 6L9 17l-5-5',
  chevron: 'M6 9l6 6 6-6',
  arrowUp: 'M12 19V5M5 12l7-7 7 7',
  arrowDown: 'M12 5v14M19 12l-7 7-7-7',
  toEnd: 'M7 13l5 5 5-5M7 6l5 5 5-5',
  shield: 'M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z',
  globe: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM2 12h20M12 2a15 15 0 0 1 0 20M12 2a15 15 0 0 0 0 20',
  key: 'M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.78 7.78 5.5 5.5 0 0 1 7.78-7.78zM15.5 7.5l3 3L22 7l-3-3',
  file: 'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zM14 2v6h6',
  link: 'M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71',
  restart: 'M1 4v6h6M3.51 15a9 9 0 1 0 2.13-9.36L1 10',
};

export function Icon({ name, size = 18, class: cls = '' }) {
  return html`<svg class=${`icon ${cls}`} width=${size} height=${size} viewBox="0 0 24 24" fill="none"
    stroke="currentColor" stroke-width=${name === 'more' ? '3.5' : '2'} stroke-linecap="round" stroke-linejoin="round"
    aria-hidden="true" focusable="false"><path d=${PATHS[name] || ''} /></svg>`;
}

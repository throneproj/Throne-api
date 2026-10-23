const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
const BIT_UNITS = ['bit/s', 'kbit/s', 'Mbit/s', 'Gbit/s', 'Tbit/s'];

function scaled(n, step, units) {
  let i = 0;
  while (n >= step && i < units.length - 1) {
    n /= step;
    i++;
  }
  return `${i === 0 || n >= 100 ? Math.round(n) : n.toFixed(1)} ${units[i]}`;
}

export function formatBytes(n) {
  n = Number(n) || 0;
  return n <= 0 ? '0 B' : scaled(n, 1024, BYTE_UNITS);
}

export function formatRate(n) {
  return `${formatBytes(n)}/s`;
}

export function formatBits(n) {
  n = Number(n) || 0;
  return n <= 0 ? '0 bit/s' : scaled(n, 1000, BIT_UNITS);
}

export function speedText(bps) {
  if (bps === -1) return 'failed';
  if (!bps || bps < 0) return '';
  return formatBits(bps);
}

export function latencyInfo(ms) {
  if (ms > 0) return { text: `${ms} ms`, cls: ms < 300 ? 'good' : ms < 800 ? 'warn' : 'bad', title: '' };
  if (ms === -1) return { text: 'failed', cls: 'bad', title: 'The latency test failed' };
  if (ms === -2) return { text: 'test failed', cls: 'bad', title: 'The tunnel came up but the test request failed' };
  return { text: '—', cls: 'none', title: 'Not tested' };
}

export function formatDate(sec) {
  return sec ? new Date(sec * 1000).toLocaleString() : '—';
}

const clockFormat = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
let clockCache = { sec: -1, text: '' };

export function formatClock(sec) {
  if (sec !== clockCache.sec) clockCache = { sec, text: clockFormat.format(new Date(sec * 1000)) };
  return clockCache.text;
}

export function timeAgo(sec, now = Date.now()) {
  if (!sec) return 'never';
  const d = Math.max(0, Math.round(now / 1000 - sec));
  if (d < 45) return 'just now';
  if (d < 3600) return `${Math.max(1, Math.round(d / 60))} min ago`;
  if (d < 86400) return `${Math.round(d / 3600)} h ago`;
  if (d < 86400 * 30) return `${Math.round(d / 86400)} d ago`;
  return formatDate(sec);
}

export function formatDuration(sec) {
  sec = Math.max(0, Math.floor(sec));
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = String(sec % 60).padStart(2, '0');
  return h ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`;
}

export function plural(n, one, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}

export function flagEmoji(cc) {
  if (!/^[A-Za-z]{2}$/.test(cc || '')) return '';
  const up = cc.toUpperCase();
  return String.fromCodePoint(0x1f1e6 + up.charCodeAt(0) - 65, 0x1f1e6 + up.charCodeAt(1) - 65);
}

export function escapeRegExp(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// CSI sequences, OSC sequences (BEL or ST terminated), then any other two-byte escape.
const ANSI = /\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]/g;
const CONTROL = /[\x00-\x08\x0b-\x1f\x7f]/g;

export function stripAnsi(s) {
  return String(s).replace(ANSI, '').replace(CONTROL, '');
}

const LEVEL_RULES = [
  ['fatal', /\[(?:fatal|panic)\]/i, /\b(?:FATAL|PANIC)\b/],
  ['error', /\[(?:error|err)\]/i, /\bERROR\b/],
  ['warn', /\[(?:warn|warning)\]/i, /\bWARN(?:ING)?\b/],
  ['debug', /\[(?:debug|trace)\]/i, /\b(?:DEBUG|TRACE)\b/],
];

export function logLevel(text) {
  for (const [level, bracketed, bare] of LEVEL_RULES) {
    if (bracketed.test(text) || bare.test(text)) return level;
  }
  return '';
}

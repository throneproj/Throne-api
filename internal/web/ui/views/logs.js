import { html, Component, useState, useEffect, useLayoutEffect, useRef } from '../ui.js';
import { openStream } from '../client.js';
import { toast, storageGet, storageSet } from '../store.js';
import { stripAnsi, logLevel, formatClock } from '../format.js';
import { ADMIN_HELP } from '../errors.js';
import { Icon } from '../components/icons.js';
import { Notice } from '../components/common.js';

const MAX_LINES = 5000;
const CHUNK = 200;
const THRONE_DOWN = {
  throne_unreachable: 'Throne is not reachable.',
  throne_timeout: 'Throne does not answer.',
  throne_rejected: 'Throne refuses the web UI.',
  restarting: 'Throne is restarting.',
  starting: 'Throne is starting.',
  exiting: 'Throne is shutting down.',
};

// The log is kept as chunks of up to CHUNK lines. Full chunks never change, so their components skip
// re-rendering and only the newest chunk is diffed when lines arrive.
class LogChunk extends Component {
  shouldComponentUpdate(next) {
    return next.lines !== this.props.lines || next.filter !== this.props.filter || next.showTime !== this.props.showTime;
  }

  render({ lines, filter, showTime }) {
    const shown = filter ? lines.filter(l => l.lower.includes(filter)) : lines;
    return html`<div class="log-chunk">
      ${shown.map(l => html`<div key=${l.id} class=${l.level ? `log-line lv-${l.level}` : 'log-line'}>${showTime && l.clock ? html`<span class="log-time">${l.clock} </span>` : null}${l.text}</div>`)}
    </div>`;
  }
}

function newModel() {
  return { chunks: [], total: 0, nextId: 1, lastSeq: 0, opened: false, reconnected: false, held: [] };
}

function appendLines(m, items) {
  if (!items.length) return;
  const chunks = m.chunks.slice();
  let last = chunks.length ? chunks[chunks.length - 1].slice() : null;
  if (last) chunks[chunks.length - 1] = last;
  for (const it of items) {
    if (!last || last.length >= CHUNK) {
      last = [];
      chunks.push(last);
    }
    last.push(it);
  }
  let total = m.total + items.length;
  while (total > MAX_LINES && chunks.length > 1) total -= chunks.shift().length;
  m.chunks = chunks;
  m.total = total;
}

function makeLine(m, text, time, seq, level) {
  const clean = stripAnsi(text);
  return {
    id: m.nextId++,
    seq,
    clock: time ? formatClock(time) : '',
    text: clean,
    lower: clean.toLowerCase(),
    level: level === undefined ? logLevel(clean) : level,
  };
}

function metaLine(m, text) {
  return makeLine(m, text, Math.floor(Date.now() / 1000), 0, 'meta');
}

export function LogsView() {
  const model = useRef(null);
  if (!model.current) model.current = newModel();
  const [, setVersion] = useState(0);
  const [paused, setPaused] = useState(false);
  const pausedRef = useRef(false);
  pausedRef.current = paused;
  const [filter, setFilter] = useState('');
  const [follow, setFollow] = useState(true);
  const [showTime, setShowTime] = useState(() => storageGet('throne.logTime') !== '0');
  const [streamError, setStreamError] = useState(null);
  const [throneDown, setThroneDown] = useState(null);
  const [heldCount, setHeldCount] = useState(0);
  const [attempt, setAttempt] = useState(0);
  const view = useRef(null);
  const frame = useRef(null);

  // One render per frame; the timer covers hidden tabs, where animation frames do not run.
  const refresh = () => {
    if (frame.current) return;
    const run = () => {
      if (!frame.current) return;
      cancelAnimationFrame(frame.current.raf);
      clearTimeout(frame.current.timer);
      frame.current = null;
      setVersion(v => v + 1);
    };
    frame.current = { raf: requestAnimationFrame(run), timer: setTimeout(run, 250) };
  };

  useEffect(() => {
    const m = model.current;
    m.opened = false;
    const take = items => {
      if (pausedRef.current) {
        m.held = m.held.concat(items);
        if (m.held.length > MAX_LINES) m.held = m.held.slice(-MAX_LINES);
        setHeldCount(m.held.length);
      } else {
        appendLines(m, items);
        refresh();
      }
    };
    let retry = 0;
    let es = null;
    const open = () => {
      es = openStream('api/logs', {
        open() {
          if (m.opened) m.reconnected = true;
          m.opened = true;
          setThroneDown(null);
          refresh();
        },
        failed(closed) {
          if (!closed) return;
          es.close();
          retry = setTimeout(open, 3000);
        },
        events: {
          lines(d) {
            setThroneDown(null);
            let lines = d.lines || [];
            const out = [];
            if (m.reconnected && lines.length) {
              m.reconnected = false;
              const newest = lines[lines.length - 1].seq;
              if (newest < m.lastSeq) out.push(metaLine(m, '— Throne restarted —'));
              else lines = lines.filter(l => l.seq > m.lastSeq);
            }
            if (d.dropped > 0) out.push(metaLine(m, `… ${d.dropped} lines were dropped (Throne keeps the newest ${MAX_LINES}).`));
            for (const l of lines) out.push(makeLine(m, l.text, l.time, l.seq));
            if (lines.length) m.lastSeq = lines[lines.length - 1].seq;
            take(out);
          },
          reset() {
            m.lastSeq = 0;
            take([metaLine(m, '— Throne restarted —')]);
          },
          error(d) {
            es.close();
            setStreamError(d && d.code ? d : { code: 'unknown', message: '' });
          },
          throne(d) {
            setThroneDown(d.reachable ? null : d);
          },
        },
      });
    };
    open();
    return () => {
      clearTimeout(retry);
      if (es) es.close();
      if (frame.current) {
        cancelAnimationFrame(frame.current.raf);
        clearTimeout(frame.current.timer);
        frame.current = null;
      }
    };
  }, [attempt]);

  useLayoutEffect(() => {
    const el = view.current;
    if (follow && el) el.scrollTop = el.scrollHeight;
  });

  const onScroll = () => {
    const el = view.current;
    if (!el) return;
    const atEnd = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    if (atEnd !== follow) setFollow(atEnd);
  };

  const togglePause = () => {
    const m = model.current;
    if (paused && m.held.length) {
      appendLines(m, m.held);
      m.held = [];
      setHeldCount(0);
    }
    pausedRef.current = !paused;
    setPaused(!paused);
  };

  const clear = () => {
    const m = model.current;
    m.chunks = [];
    m.total = 0;
    m.held = [];
    setHeldCount(0);
    setVersion(v => v + 1);
  };

  const jumpToEnd = () => {
    setFollow(true);
    const el = view.current;
    if (el) el.scrollTop = el.scrollHeight;
  };

  const toggleTime = () => {
    storageSet('throne.logTime', showTime ? '0' : null);
    setShowTime(!showTime);
  };

  const q = filter.trim().toLowerCase();
  const copy = async () => {
    const lines = [];
    for (const c of model.current.chunks) {
      for (const l of c) if (!q || l.lower.includes(q)) lines.push(showTime && l.clock ? `${l.clock} ${l.text}` : l.text);
    }
    try {
      await navigator.clipboard.writeText(lines.join('\n'));
      toast(`Copied ${lines.length} lines.`, { kind: 'success', timeout: 2500 });
    } catch {
      toast('Copying is not available here. Select the lines and copy them.', { kind: 'error' });
    }
  };

  if (streamError) {
    const code = streamError.code;
    return html`<div class="view">
      <h1 class="view-title">Logs</h1>
      ${code === 'forbidden'
        ? html`<${Notice} kind="info" icon="shield"><strong>The log needs admin rights.</strong><span>${ADMIN_HELP}</span><//>`
        : code === 'not_found' || code === 'method_not_allowed'
          ? html`<${Notice} kind="info">This Throne is too old to share its log with the web UI.<//>`
          : html`<${Notice} kind="bad" icon="alert"><strong>The log stream stopped.</strong><span>${streamError.message || code}</span><//>`}
      ${code !== 'forbidden' && code !== 'not_found' && html`<p><button type="button" class="btn" onClick=${() => { setStreamError(null); setAttempt(a => a + 1); }}>Try again</button></p>`}
    </div>`;
  }

  const m = model.current;
  return html`<div class="view view-logs">
    <h1 class="view-title sr-only">Logs</h1>
    <div class="toolbar">
      <div class="search">
        <${Icon} name="search" size=${16} />
        <input type="search" class="input" placeholder="Filter lines…" aria-label="Filter log lines" value=${filter}
          onInput=${e => setFilter(e.currentTarget.value)} />
      </div>
      <button type="button" class=${paused ? 'btn primary' : 'btn'} aria-pressed=${paused ? 'true' : 'false'} onClick=${togglePause}>
        <${Icon} name=${paused ? 'play' : 'pause'} /><span>${paused ? `Resume${heldCount ? ` (${heldCount} new)` : ''}` : 'Pause'}</span>
      </button>
      <label class="check"><input type="checkbox" checked=${showTime} onChange=${toggleTime} />Time</label>
      <button type="button" class="icon-btn" aria-label="Copy the shown lines" title="Copy the shown lines" onClick=${copy}><${Icon} name="copy" /></button>
      <button type="button" class="icon-btn" aria-label="Clear the view" title="Clear the view" onClick=${clear}><${Icon} name="trash" /></button>
    </div>
    ${throneDown && html`<p class="note warn">${THRONE_DOWN[throneDown.code] || THRONE_DOWN.throne_unreachable} The log carries on when it is back.</p>`}
    <div class="log-wrap">
      <div class="log-view" ref=${view} onScroll=${onScroll} tabindex="0" role="log" aria-label="Throne log" aria-live="off">
        ${m.total === 0 && html`<div class="log-empty muted">${m.opened ? 'No log lines yet.' : 'Connecting to the log…'}</div>`}
        ${m.chunks.map((c, i) => html`<${LogChunk} key=${c[0] ? c[0].id : i} lines=${c} filter=${q} showTime=${showTime} />`)}
      </div>
      ${!follow && html`<button type="button" class="btn jump" onClick=${jumpToEnd}><${Icon} name="toEnd" />Jump to end</button>`}
    </div>
    <p class="muted small">${m.total} lines${q ? ' (filtered view)' : ''}. The view keeps the newest ${MAX_LINES}.</p>
  </div>`;
}

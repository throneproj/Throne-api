import { useState, useEffect, useRef, useReducer } from './ui.js';
import { describeError } from './errors.js';

export const ALL_TOPICS = ['status', 'groups', 'profiles', 'tests', 'settings'];

export function storageGet(key, fallback = null) {
  try {
    const v = localStorage.getItem(key);
    return v === null ? fallback : v;
  } catch {
    return fallback;
  }
}

export function storageSet(key, value) {
  try {
    if (value === null || value === undefined) localStorage.removeItem(key);
    else localStorage.setItem(key, String(value));
  } catch {
    // storage may be unavailable (private mode); the setting just is not remembered
  }
}

export function sessionDefaults() {
  return {
    status: null,
    statusError: null,
    throne: { reachable: true, code: '', message: '' },
    gatewayLost: false,
    unavailable: null,
    restartWait: null,
    adminDenied: false,
    busyAction: null,
    rates: [],
    restore: null,
  };
}

let state = {
  boot: 'loading',
  bootError: null,
  auth: { authenticated: false, gateway: null },
  toasts: [],
  confirm: null,
  adminNoticeDismissed: storageGet('throne.adminNoticeDismissed') === '1',
  ...sessionDefaults(),
};
const subscribers = new Set();

export function getState() {
  return state;
}

export function setState(patch) {
  const next = typeof patch === 'function' ? patch(state) : patch;
  if (!next) return;
  state = { ...state, ...next };
  for (const fn of [...subscribers]) fn();
}

// Re-renders the component when selector(state) changes (Object.is).
export function useStore(selector) {
  const [, force] = useReducer(n => n + 1, 0);
  const sel = useRef(selector);
  sel.current = selector;
  const value = selector(state);
  const last = useRef(value);
  last.current = value;
  useEffect(() => {
    const check = () => {
      if (!Object.is(sel.current(state), last.current)) force();
    };
    subscribers.add(check);
    check();
    return () => subscribers.delete(check);
  }, []);
  return value;
}

export function useBlocked() {
  return useStore(s => s.gatewayLost || !s.throne.reachable || !!s.unavailable || !!s.restartWait);
}

// Topics from the events stream; listeners get a Set of topic names.
const topicSubscribers = new Set();

export function onTopics(fn) {
  topicSubscribers.add(fn);
  return () => {
    topicSubscribers.delete(fn);
  };
}

export function emitTopics(topics) {
  const set = topics instanceof Set ? topics : new Set(topics);
  if (!set.size) return;
  for (const fn of [...topicSubscribers]) fn(set);
}

// One loader per useResource: at most one request in flight, a request during it runs once after it,
// and topic-driven reloads are spaced by `throttle` ms.
class Loader {
  constructor(setResult) {
    this.setResult = setResult;
    this.fn = null;
    this.throttle = 0;
    this.generation = 0;
    this.busy = false;
    this.again = false;
    this.last = 0;
    this.timer = 0;
    this.alive = true;
    this.failed = false;
  }

  reset() {
    this.generation++;
    this.busy = false;
    this.again = false;
    clearTimeout(this.timer);
    this.timer = 0;
    this.setResult({ data: undefined, error: null, loading: true });
    this.start();
  }

  request() {
    if (this.busy) {
      this.again = true;
      return;
    }
    if (this.timer) return;
    const wait = Math.max(0, this.last + this.throttle - Date.now());
    this.timer = setTimeout(() => {
      this.timer = 0;
      this.start();
    }, wait);
  }

  start() {
    clearTimeout(this.timer);
    this.timer = 0;
    if (!this.alive) return;
    if (this.busy) {
      this.again = true;
      return;
    }
    this.busy = true;
    this.again = false;
    const gen = this.generation;
    this.setResult(r => (r.loading ? r : { ...r, loading: true }));
    Promise.resolve()
      .then(() => this.fn())
      .then(
        data => {
          if (!this.alive || gen !== this.generation) return;
          this.failed = false;
          this.setResult({ data, error: null, loading: false });
        },
        error => {
          if (!this.alive || gen !== this.generation) return;
          this.failed = true;
          this.setResult(r => ({ data: r.data, error, loading: false }));
        },
      )
      .finally(() => {
        if (gen !== this.generation) return;
        this.busy = false;
        this.last = Date.now();
        if (this.alive && this.again) this.request();
      });
  }

  stop() {
    this.alive = false;
    clearTimeout(this.timer);
  }
}

// Loads fn() on mount and when deps change; reloads when one of `topics` arrives, or any topic while
// the last load failed (so views recover when Throne is back).
export function useResource(fn, { topics = [], deps = [], throttle = 0 } = {}) {
  const [result, setResult] = useState({ data: undefined, error: null, loading: true });
  const ref = useRef(null);
  if (!ref.current) ref.current = new Loader(setResult);
  const loader = ref.current;
  loader.fn = fn;
  loader.throttle = throttle;
  useEffect(() => () => loader.stop(), []);
  useEffect(() => {
    loader.reset();
  }, deps);
  const topicKey = topics.join(',');
  useEffect(() => {
    const wanted = topicKey ? topicKey.split(',') : [];
    return onTopics(set => {
      if (loader.failed || wanted.some(t => set.has(t))) loader.request();
    });
  }, [topicKey]);
  return {
    ...result,
    reload: () => loader.start(),
    setData: update => setResult(r => ({ ...r, data: typeof update === 'function' ? update(r.data) : update })),
  };
}

export function useNow(ms = 1000, enabled = true) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return undefined;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(t);
  }, [ms, enabled]);
  return now;
}

export const ROUTES = ['status', 'profiles', 'traffic', 'logs', 'settings', 'backup', 'system'];

function readRoute() {
  const m = /^#\/([a-z]+)/.exec(location.hash);
  return m && ROUTES.includes(m[1]) ? m[1] : 'status';
}

export function useRoute() {
  const [route, setRoute] = useState(readRoute);
  useEffect(() => {
    const onHash = () => setRoute(readRoute());
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);
  return route;
}

let toastSeq = 0;

export function toast(message, { kind = 'info', detail = '', timeout } = {}) {
  const id = ++toastSeq;
  setState(s => ({ toasts: [...s.toasts.slice(-4), { id, kind, message, detail }] }));
  const ms = timeout ?? (kind === 'error' ? 10000 : 5000);
  if (ms > 0) setTimeout(() => dismissToast(id), ms);
  return id;
}

export function dismissToast(id) {
  setState(s => (s.toasts.some(t => t.id === id) ? { toasts: s.toasts.filter(t => t.id !== id) } : null));
}

export function toastError(err, context = '') {
  if (!err || err.name === 'AbortError' || err.code === 'unauthenticated') return;
  const { title, detail } = describeError(err);
  toast(context ? `${context}: ${title}` : title, { kind: 'error', detail });
}

// Resolves true when the user confirms. opts: title, message, confirmLabel, danger.
export function confirmDialog(opts) {
  return new Promise(resolve => {
    const prev = state.confirm;
    if (prev) prev.resolve(false);
    setState({ confirm: { ...opts, resolve } });
  });
}

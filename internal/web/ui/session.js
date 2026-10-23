import { api, getToken, setToken, setApiHooks, openStream } from './client.js';
import {
  getState, setState, emitTopics, onTopics, ALL_TOPICS, sessionDefaults, toast, toastError, storageSet,
} from './store.js';

const UNAVAILABLE = new Set(['restarting', 'starting', 'exiting']);
let session = null;

export async function boot() {
  setApiHooks({ onError: handleError });
  if (!getToken()) {
    setState({ boot: 'ready', bootError: null, auth: { authenticated: false, gateway: null } });
    return;
  }
  setState({ boot: 'loading', bootError: null });
  try {
    const st = await api.get('auth/state', undefined, { quiet: true });
    if (st.authenticated) begin(st);
    else {
      setToken('');
      setState({ boot: 'ready', auth: { authenticated: false, gateway: null } });
    }
  } catch (e) {
    if (e.code === 'unauthenticated') {
      setToken('');
      setState({ boot: 'ready', auth: { authenticated: false, gateway: null } });
    } else setState({ boot: 'offline', bootError: e });
  }
}

// auth: the body of POST /auth/login (which carries the token) or of GET /auth/state.
export function begin(auth) {
  if (auth.csrf) setToken(auth.csrf);
  setState({ boot: 'ready', auth: { authenticated: true, gateway: auth.gateway || null }, ...sessionDefaults() });
  startSession();
}

export async function logout() {
  try {
    await api.post('auth/logout', undefined, { quiet: true });
  } catch {
    // the session is dropped locally either way
  }
  endSession();
}

export function sessionExpired() {
  if (!session) return;
  endSession();
  toast('Your session has ended. Sign in again.');
}

function endSession() {
  stopSession();
  setToken('');
  const { confirm } = getState();
  if (confirm) confirm.resolve(false);
  setState({ auth: { authenticated: false, gateway: null }, confirm: null, ...sessionDefaults() });
}

export function dismissAdminNotice() {
  storageSet('throne.adminNoticeDismissed', '1');
  setState({ adminNoticeDismissed: true });
}

function startSession() {
  stopSession();
  const s = { alive: true, timers: new Set(), cleanups: [], recovering: false, restartToken: null };
  session = s;
  s.cleanups.push(onTopics(set => {
    if (set.has('status')) loadStatus();
  }));
  connectEvents(s);
  pollRates(s);
  probeAdmin(s);
  loadStatus();
}

function stopSession() {
  if (!session) return;
  const s = session;
  session = null;
  s.alive = false;
  for (const t of s.timers) clearTimeout(t);
  s.timers.clear();
  for (const fn of s.cleanups) fn();
}

function later(s, fn, ms) {
  const t = setTimeout(() => {
    s.timers.delete(t);
    if (s.alive) fn();
  }, ms);
  s.timers.add(t);
  return t;
}

function cancel(s, t) {
  clearTimeout(t);
  s.timers.delete(t);
}

function handleError(err) {
  if (!session) return;
  if (err.code === 'unauthenticated') sessionExpired();
  else if (err.code === 'forbidden') {
    if (!getState().adminDenied) setState({ adminDenied: true });
  } else if (UNAVAILABLE.has(err.code)) markUnavailable(err);
}

let statusBusy = false;
let statusAgain = false;

export async function loadStatus() {
  if (!session) return;
  if (statusBusy) {
    statusAgain = true;
    return;
  }
  statusBusy = true;
  const s = session;
  try {
    const st = await api.get('api/v1/status');
    if (s.alive) setState({ status: st, statusError: null });
  } catch (e) {
    if (s.alive) setState({ statusError: e });
  } finally {
    statusBusy = false;
    if (statusAgain) {
      statusAgain = false;
      loadStatus();
    }
  }
}

function connectEvents(s) {
  let es = null;
  let pending = new Set();
  let flushTimer = 0;
  let lostTimer = 0;
  const flush = () => {
    flushTimer = 0;
    const set = pending;
    pending = new Set();
    emitTopics(set);
  };
  const open = () => {
    es = openStream('api/events', {
      open() {
        if (lostTimer) cancel(s, lostTimer);
        lostTimer = 0;
        if (getState().gatewayLost) setState({ gatewayLost: false });
      },
      failed(closed) {
        if (!lostTimer && !getState().gatewayLost) {
          lostTimer = later(s, () => {
            lostTimer = 0;
            setState({ gatewayLost: true });
          }, 4000);
        }
        if (!closed) return;
        // An HTTP error (such as 401) ends EventSource for good: check the session, then reopen.
        es.close();
        es = null;
        recheckAuth().then(ok => {
          if (ok && s.alive) later(s, open, 3000);
        });
      },
      events: {
        changes(d) {
          for (const t of d.changes || []) pending.add(t);
          if (!flushTimer) flushTimer = later(s, flush, 300);
        },
        throne(d) {
          const was = getState().throne.reachable;
          if (d.reachable) {
            setState({ throne: { reachable: true, code: '', message: '' } });
            if (!was) probeAdmin(s);
          } else {
            setState({ throne: { reachable: false, code: d.code || 'throne_unreachable', message: d.message || '' } });
          }
        },
      },
    });
  };
  open();
  s.cleanups.push(() => {
    if (es) es.close();
  });
}

async function recheckAuth() {
  try {
    const st = await api.get('auth/state', undefined, { quiet: true });
    if (!st.authenticated) {
      sessionExpired();
      return false;
    }
  } catch (e) {
    if (e.code === 'unauthenticated') {
      sessionExpired();
      return false;
    }
    // gateway unreachable: keep retrying
  }
  return true;
}

function pollRates(s) {
  const tick = async () => {
    if (document.visibilityState === 'visible') {
      const st = getState();
      let sample = { t: Date.now(), pu: 0, pd: 0, du: 0, dd: 0 };
      if (st.status && st.status.state === 'running' && st.throne.reachable && !st.unavailable && !st.restartWait) {
        try {
          const r = await api.get('api/v1/stats/live', undefined, { quiet: true });
          const proxy = r.proxy || {};
          const direct = r.direct || {};
          sample = { t: Date.now(), pu: proxy.up || 0, pd: proxy.down || 0, du: direct.up || 0, dd: direct.down || 0 };
        } catch {
          sample = null;
        }
      }
      if (sample && s.alive) {
        setState(p => ({ rates: [...p.rates.filter(x => x.t > sample.t - 130000), sample] }));
      }
    }
    if (s.alive) later(s, tick, 2000);
  };
  tick();
}

async function probeAdmin(s) {
  try {
    await api.get('api/v1/logs', { limit: 1 }, { quiet: true });
    if (s.alive && getState().adminDenied) setState({ adminDenied: false });
  } catch (e) {
    if (s.alive && e.code === 'forbidden') setState({ adminDenied: true });
  }
}

function markUnavailable(err) {
  const s = session;
  setState({ unavailable: { code: err.code, message: err.detail || '' } });
  if (s.recovering) return;
  s.recovering = true;
  const check = async () => {
    try {
      const st = await api.get('api/v1/status', undefined, { quiet: true });
      if (!s.alive) return;
      s.recovering = false;
      setState({ unavailable: null, status: st, statusError: null });
      emitTopics(ALL_TOPICS);
    } catch (e) {
      if (!s.alive) return;
      if (e.code === 'unauthenticated') {
        sessionExpired();
        return;
      }
      if (UNAVAILABLE.has(e.code)) setState({ unavailable: { code: e.code, message: e.detail || '' } });
      later(s, check, 2000);
    }
  };
  later(s, check, 2000);
}

// Shows the restart overlay until Throne has gone away and answers again. upGraceMs: how long Throne
// may keep answering before the wait ends anyway (a restart too quick to notice).
export function waitForRestart(title, { upGraceMs = 20000 } = {}) {
  const s = session;
  if (!s) return;
  const token = {};
  s.restartToken = token;
  const started = Date.now();
  let sawDown = false;
  setState({ restartWait: { title, started } });
  const check = async () => {
    if (s.restartToken !== token) return;
    try {
      const st = await api.get('api/v1/status', undefined, { quiet: true });
      if (!s.alive || s.restartToken !== token) return;
      if (sawDown || Date.now() - started > upGraceMs) {
        s.restartToken = null;
        setState({ restartWait: null, unavailable: null, status: st, statusError: null });
        emitTopics(ALL_TOPICS);
        probeAdmin(s);
        toast('Throne is back.', { kind: 'success' });
        return;
      }
    } catch (e) {
      if (!s.alive || s.restartToken !== token) return;
      if (e.code === 'unauthenticated') {
        sessionExpired();
        return;
      }
      sawDown = true;
    }
    later(s, check, 1500);
  };
  later(s, check, 1000);
}

export function dismissRestartWait() {
  if (session) session.restartToken = null;
  setState({ restartWait: null });
  loadStatus();
}

async function connectionAction(kind, run, success) {
  if (getState().busyAction) return null;
  setState({ busyAction: kind });
  try {
    const st = await run();
    if (st && typeof st.state === 'string' && st.capabilities) setState({ status: st });
    if (success) {
      const text = typeof success === 'function' ? success(st) : success;
      if (text) toast(text, { kind: 'success' });
    }
    return st;
  } catch (e) {
    toastError(e);
    return null;
  } finally {
    setState({ busyAction: null });
    loadStatus();
  }
}

export const actions = {
  connect: id => connectionAction('connect', () => api.put('api/v1/connection', { profile: id })),
  connectLast: () => connectionAction('connect', () => api.put('api/v1/connection', { select: 'last' })),
  connectBest: (group, name) => connectionAction(
    'connect',
    () => api.put('api/v1/connection', { select: 'lowest_latency', group, ...(name ? { name } : {}), timeout: 120 }),
    st => (st && st.profile ? `Connected to ${st.profile.name}.` : ''),
  ),
  disconnect: () => connectionAction('disconnect', () => api.del('api/v1/connection')),
  restartProfile: () => connectionAction('restart', () => api.put('api/v1/connection', { select: 'last', restart: true }), 'The profile restarted.'),
  setModes: patch => connectionAction('modes', () => api.put('api/v1/modes', patch)),
  setRoute: id => connectionAction('route', () => api.put('api/v1/routes/active', { id })),
};

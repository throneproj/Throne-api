// The session token from POST /auth/login. Cookies are not port-scoped, so the token is the second secret:
// it lives in this origin's storage only, and every request (EventSource via ?token=) carries it.
const TOKEN_KEY = 'throne-web-token';
let token = '';
try {
  token = localStorage.getItem(TOKEN_KEY) || '';
} catch {
  token = '';
}
const hooks = { onError: null };

export class ApiError extends Error {
  constructor(status, code, detail = '', retryAfter = 0) {
    super(detail || code);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.detail = detail;
    this.retryAfter = retryAfter;
  }
}

export function getToken() {
  return token;
}

export function setToken(value) {
  token = value || '';
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    // storage unavailable: the token stays in memory, so a reload means signing in again
  }
}

export function setApiHooks(next) {
  Object.assign(hooks, next);
}

function buildUrl(path, query) {
  if (!query) return path;
  const qs = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== null && value !== '') qs.set(key, String(value));
  }
  const s = qs.toString();
  return s ? `${path}?${s}` : path;
}

function errorFromBody(status, text, retryHeader) {
  let code = '';
  let detail = '';
  let retryAfter = 0;
  try {
    const body = JSON.parse(text);
    if (body && body.error) {
      code = body.error.code || '';
      detail = body.error.message || '';
      retryAfter = Number(body.error.retry_after) || 0;
    }
  } catch {
    detail = String(text || '').trim().slice(0, 300);
  }
  if (!retryAfter) retryAfter = Number(retryHeader) || 0;
  if (!code) code = status === 401 ? 'unauthenticated' : `http_${status}`;
  return new ApiError(status, code, detail, retryAfter);
}

function report(err, opts) {
  if (!opts.quiet && hooks.onError) hooks.onError(err);
}

// opts: query, body (object → JSON, Blob → raw), raw (return the Response), quiet (skip the global error hook), signal.
export async function request(method, path, opts = {}) {
  const headers = { Accept: 'application/json' };
  const init = { method, headers, credentials: 'same-origin', cache: 'no-store', signal: opts.signal };
  if (token) headers['X-CSRF-Token'] = token;
  if (opts.body !== undefined) {
    if (opts.body instanceof Blob) {
      init.body = opts.body;
      headers['Content-Type'] = 'application/octet-stream';
    } else {
      init.body = JSON.stringify(opts.body);
      headers['Content-Type'] = 'application/json';
    }
  }
  let res;
  try {
    res = await fetch(buildUrl(path, opts.query), init);
  } catch (e) {
    if (e && e.name === 'AbortError') throw e;
    const err = new ApiError(0, 'network');
    report(err, opts);
    throw err;
  }
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    const err = errorFromBody(res.status, text, res.headers.get('Retry-After'));
    report(err, opts);
    throw err;
  }
  if (opts.raw) return res;
  if (res.status === 204) return {};
  const text = await res.text();
  if (!text) return {};
  let value;
  try {
    value = JSON.parse(text);
  } catch {
    throw new ApiError(res.status, 'bad_response');
  }
  return value === null ? {} : value;
}

export const api = {
  get: (path, query, opts) => request('GET', path, { ...opts, query }),
  post: (path, body, opts) => request('POST', path, { ...opts, body }),
  put: (path, body, opts) => request('PUT', path, { ...opts, body }),
  del: (path, body, opts) => request('DELETE', path, { ...opts, body }),
};

// Uploads a file with progress (fetch has no upload progress). Returns { promise, abort }.
export function uploadFile(path, file, onProgress) {
  const xhr = new XMLHttpRequest();
  const promise = new Promise((resolve, reject) => {
    xhr.open('POST', path);
    if (token) xhr.setRequestHeader('X-CSRF-Token', token);
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');
    xhr.setRequestHeader('Accept', 'application/json');
    xhr.upload.onprogress = e => {
      if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total);
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText || '{}') || {});
        } catch {
          reject(new ApiError(xhr.status, 'bad_response'));
        }
        return;
      }
      const err = errorFromBody(xhr.status, xhr.responseText, xhr.getResponseHeader('Retry-After'));
      report(err, {});
      reject(err);
    };
    xhr.onerror = () => reject(new ApiError(0, 'network'));
    xhr.onabort = () => reject(new DOMException('The upload was cancelled.', 'AbortError'));
    xhr.send(file);
  });
  return { promise, abort: () => xhr.abort() };
}

export function saveBlob(blob, filename) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.rel = 'noopener';
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60000);
}

export function filenameFrom(res, fallback) {
  const cd = res.headers.get('Content-Disposition') || '';
  const m = /filename\*?=(?:UTF-8'')?"?([^";]+)"?/i.exec(cd);
  if (!m) return fallback;
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return m[1];
  }
}

// handlers: { open(), failed(closed), events: { name(data) } }. The server's own "error" event shares
// its name with the transport error, so the two are told apart by MessageEvent.
export function openStream(path, handlers) {
  const es = new EventSource(token ? `${path}?token=${encodeURIComponent(token)}` : path);
  for (const [name, fn] of Object.entries(handlers.events || {})) {
    es.addEventListener(name, e => {
      if (!(e instanceof MessageEvent)) return;
      let data = {};
      try {
        data = e.data ? JSON.parse(e.data) : {};
      } catch {
        return;
      }
      fn(data);
    });
  }
  es.addEventListener('open', () => handlers.open && handlers.open());
  es.addEventListener('error', e => {
    if (e instanceof MessageEvent) return;
    if (handlers.failed) handlers.failed(es.readyState === EventSource.CLOSED);
  });
  return es;
}

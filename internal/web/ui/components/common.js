import { html, useState, useEffect, useLayoutEffect, useRef } from '../ui.js';
import { useStore, setState, getState, dismissToast, toast } from '../store.js';
import { describeError } from '../errors.js';
import { Icon } from './icons.js';

let idSeq = 0;

export function useUid(prefix = 'id') {
  const ref = useRef('');
  if (!ref.current) ref.current = `${prefix}-${++idSeq}`;
  return ref.current;
}

export function Spinner({ small = false, label = '' }) {
  return html`<span class=${small ? 'spinner sm' : 'spinner'} role=${label ? 'status' : undefined}
    aria-label=${label || undefined} aria-hidden=${label ? undefined : 'true'}></span>`;
}

export function Loading({ text = 'Loading…' }) {
  return html`<div class="loading"><${Spinner} /><span>${text}</span></div>`;
}

export function ErrorBox({ error, onRetry, compact = false }) {
  if (!error) return null;
  const { title, detail } = describeError(error);
  return html`<div class=${compact ? 'notice bad compact' : 'notice bad'} role="alert">
    <${Icon} name="alert" />
    <div class="notice-body">
      <strong>${title}</strong>
      ${detail && html`<span>${detail}</span>`}
    </div>
    ${onRetry && html`<button type="button" class="btn small" onClick=${() => onRetry()}>Retry</button>`}
  </div>`;
}

export function Notice({ kind = 'info', icon = 'info', children, actions }) {
  return html`<div class=${`notice ${kind}`}>
    <${Icon} name=${icon} />
    <div class="notice-body">${children}</div>
    ${actions}
  </div>`;
}

// Modal dialog on <dialog>: focus stays inside, Esc and a backdrop click call onClose, focus returns
// to the opener on unmount.
export function Dialog({ title, onClose, children, footer, wide = false, busy = false }) {
  const ref = useRef(null);
  const titleId = useUid('dlg');
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const busyRef = useRef(busy);
  busyRef.current = busy;
  const mounted = useRef(true);
  useLayoutEffect(() => {
    const d = ref.current;
    const opener = document.activeElement;
    mounted.current = true;
    if (d && !d.open) {
      try {
        d.showModal();
      } catch {
        d.setAttribute('open', '');
      }
    }
    const target = d && (d.querySelector('[data-autofocus]') || d.querySelector('input:not([type=hidden]):not([disabled]), select, textarea'));
    if (target) target.focus();
    return () => {
      mounted.current = false;
      if (d && d.open) d.close();
      if (opener && typeof opener.focus === 'function' && document.contains(opener)) opener.focus();
    };
  }, []);
  const requestClose = () => {
    if (!busyRef.current && closeRef.current) closeRef.current();
  };
  const onCancel = e => {
    e.preventDefault();
    requestClose();
  };
  // The browser may close the dialog itself (repeated Esc); reopen it while busy, else let the owner unmount it.
  const onNativeClose = () => {
    if (!mounted.current) return;
    if (busyRef.current && ref.current) {
      try {
        ref.current.showModal();
      } catch {
        ref.current.setAttribute('open', '');
      }
    } else if (closeRef.current) closeRef.current();
  };
  const downOnBackdrop = useRef(false);
  const onPointerDown = e => {
    downOnBackdrop.current = e.target === ref.current;
  };
  const onBackdrop = e => {
    if (e.target === ref.current && downOnBackdrop.current) requestClose();
  };
  return html`<dialog ref=${ref} class=${wide ? 'dialog wide' : 'dialog'} aria-labelledby=${titleId}
      onCancel=${onCancel} onClose=${onNativeClose} onPointerDown=${onPointerDown} onClick=${onBackdrop}>
    <div class="dialog-inner">
      <div class="dialog-head">
        <h2 id=${titleId}>${title}</h2>
        <button type="button" class="icon-btn" aria-label="Close" onClick=${requestClose} disabled=${busy}>
          <${Icon} name="x" />
        </button>
      </div>
      <div class="dialog-body">${children}</div>
      ${footer && html`<div class="dialog-foot">${footer}</div>`}
    </div>
  </dialog>`;
}

export function ConfirmHost() {
  const c = useStore(s => s.confirm);
  if (!c) return null;
  const answer = value => {
    if (getState().confirm !== c) return;
    setState({ confirm: null });
    c.resolve(value);
  };
  const footer = html`
    <button type="button" class="btn" onClick=${() => answer(false)} data-autofocus=${c.danger ? true : undefined}>
      ${c.cancelLabel || 'Cancel'}
    </button>
    <button type="button" class=${c.danger ? 'btn danger' : 'btn primary'} onClick=${() => answer(true)}
      data-autofocus=${c.danger ? undefined : true}>${c.confirmLabel || 'OK'}</button>`;
  return html`<${Dialog} title=${c.title} onClose=${() => answer(false)} footer=${footer}>
    ${typeof c.message === 'string' ? html`<p>${c.message}</p>` : c.message}
  <//>`;
}

export function Toasts() {
  const toasts = useStore(s => s.toasts);
  return html`<div class="toasts" role="status" aria-live="polite">
    ${toasts.map(t => html`<div key=${t.id} class=${`toast ${t.kind}`}>
      <${Icon} name=${t.kind === 'error' ? 'alert' : t.kind === 'success' ? 'check' : 'info'} />
      <div class="toast-body">
        <strong>${t.message}</strong>
        ${t.detail && html`<span>${t.detail}</span>`}
      </div>
      <button type="button" class="icon-btn small" aria-label="Dismiss" onClick=${() => dismissToast(t.id)}>
        <${Icon} name="x" size=${16} />
      </button>
    </div>`)}
  </div>`;
}

// Dropdown menu. items: [{ label, onSelect, icon, hint, danger, disabled } | { separator: true } | falsy]
export function Menu({ label, icon, items, disabled = false, ariaLabel, buttonClass = 'btn', align = 'end' }) {
  const [open, setOpen] = useState(false);
  const root = useRef(null);
  const listId = useUid('menu');
  useEffect(() => {
    if (!open) return undefined;
    const el = root.current;
    const first = el && el.querySelector('[role=menuitem]:not([disabled])');
    if (first) first.focus();
    const onPointer = e => {
      if (el && !el.contains(e.target)) setOpen(false);
    };
    const onKey = e => {
      if (e.key === 'Escape') {
        setOpen(false);
        const btn = el && el.querySelector('button');
        if (btn) btn.focus();
      }
    };
    document.addEventListener('pointerdown', onPointer);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('pointerdown', onPointer);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);
  const onListKey = e => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp' && e.key !== 'Home' && e.key !== 'End') return;
    e.preventDefault();
    const list = [...e.currentTarget.querySelectorAll('[role=menuitem]:not([disabled])')];
    if (!list.length) return;
    const i = list.indexOf(document.activeElement);
    let next = 0;
    if (e.key === 'ArrowDown') next = (i + 1) % list.length;
    else if (e.key === 'ArrowUp') next = (i - 1 + list.length) % list.length;
    else if (e.key === 'End') next = list.length - 1;
    list[next].focus();
  };
  const shown = items.filter(Boolean);
  return html`<div class="menu" ref=${root}>
    <button type="button" class=${buttonClass} aria-haspopup="menu" aria-expanded=${open ? 'true' : 'false'}
      aria-controls=${open ? listId : undefined} aria-label=${ariaLabel} disabled=${disabled}
      onClick=${() => setOpen(o => !o)}>
      ${icon && html`<${Icon} name=${icon} />`}
      ${label && html`<span>${label}</span>`}
      ${label && html`<${Icon} name="chevron" size=${14} />`}
    </button>
    ${open && html`<div class=${`menu-list ${align}`} role="menu" id=${listId} onKeyDown=${onListKey}>
      ${shown.map((it, i) => (it.separator
        ? html`<div key=${`sep${i}`} class="menu-sep" role="separator"></div>`
        : html`<button key=${it.label} type="button" role="menuitem" class=${it.danger ? 'menu-item danger' : 'menu-item'}
            disabled=${!!it.disabled} onClick=${() => { setOpen(false); it.onSelect(); }}>
            ${it.icon ? html`<${Icon} name=${it.icon} size=${16} />` : html`<span class="icon-space"></span>`}
            <span class="menu-label">${it.label}${it.hint && html`<small>${it.hint}</small>`}</span>
          </button>`))}
    </div>`}
  </div>`;
}

export function CopyButton({ text, label = 'Copy' }) {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      toast('Copied.', { kind: 'success', timeout: 2000 });
    } catch {
      toast('Copying is not available here. Select the text and copy it.', { kind: 'error' });
    }
  };
  return html`<button type="button" class="icon-btn small" aria-label=${label} title=${label} onClick=${copy}>
    <${Icon} name="copy" size=${16} />
  </button>`;
}

export function AdminHint({ show }) {
  if (!show) return null;
  return html`<p class="muted small admin-hint"><${Icon} name="shield" size=${14} /> Needs admin rights on Throne (see the notice at the top).</p>`;
}

const SELECTOR = "[data-selection-id]";

export function selectionForRange(initial, rows, start, end, mode) {
  const selected = new Set(initial);
  const low = Math.min(start, end);
  const high = Math.max(start, end);
  for (const [id, row] of rows) {
    if (row.disabled || row.bottom < low || row.top > high) continue;
    if (mode === "remove" || (mode === "invert" && initial.has(id))) selected.delete(id);
    else selected.add(id);
  }
  return [...selected];
}

// Selection is based on the pointer-down snapshot, never repeatedly toggled
// by animation frames. Remember measured rows as a virtual table recycles DOM.
export function createCheckboxDragSelection({ getContainer, getSelected, isDisabled = () => false, onChange, onActiveChange = () => {} }) {
  let drag = null;
  let clearClick = null;

  function stop() {
    const state = drag;
    if (!state) return;
    drag = null;
    state.win.cancelAnimationFrame(state.frame);
    state.doc.removeEventListener("pointermove", move, true);
    state.doc.removeEventListener("pointerup", finish, true);
    state.doc.removeEventListener("pointercancel", cancel, true);
    state.doc.removeEventListener("visibilitychange", visibility);
    state.win.removeEventListener("blur", stop);
    state.box?.remove();
    if (state.active) {
      state.doc.documentElement.style.userSelect = state.oldUserSelect;
      onActiveChange(false);
    }
  }

  function visibility() { if (drag?.doc.hidden) stop(); }
  function cancel(event) { if (event.pointerId === drag?.pointerId) stop(); }

  function finish(event) {
    if (event.pointerId !== drag?.pointerId) return;
    if (drag.active) {
      event.preventDefault();
      event.stopPropagation();
      const { doc, win, container } = drag;
      clearClick?.();
      const suppress = (click) => {
        if (container.contains(click.target)) {
          click.preventDefault();
          click.stopImmediatePropagation();
        }
        clearClick?.();
      };
      const timer = win.setTimeout(() => clearClick?.(), 0);
      clearClick = () => {
        doc.removeEventListener("click", suppress, true);
        win.clearTimeout(timer);
        clearClick = null;
      };
      doc.addEventListener("click", suppress, true);
    }
    stop();
  }

  function viewport(state) {
    if (state.host === state.doc.scrollingElement) {
      return { top: 0, bottom: state.win.innerHeight };
    }
    const rect = state.host.getBoundingClientRect();
    return { top: Math.max(0, rect.top), bottom: Math.min(state.win.innerHeight, rect.bottom) };
  }

  function origin(state) {
    return state.host === state.doc.scrollingElement ? 0 : state.host.getBoundingClientRect().top;
  }

  function update(state) {
    const offset = state.host.scrollTop - origin(state);
    for (const node of state.container.querySelectorAll(SELECTOR)) {
      const rect = node.getBoundingClientRect();
      if (!rect.width || !rect.height) continue;
      const id = Number(node.dataset.selectionId);
      if (!Number.isSafeInteger(id) || id <= 0) continue;
      state.rows.set(id, {
        top: rect.top + offset, bottom: rect.bottom + offset,
        disabled: node.dataset.selectionDisabled === "true" || !!node.querySelector("input:disabled"),
      });
    }
    const end = state.y + offset;
    const ids = selectionForRange(state.initial, state.rows, state.start, end, state.mode);
    const key = ids.join(",");
    if (key !== state.lastKey) {
      state.lastKey = key;
      onChange(ids);
    }
    const bounds = viewport(state);
    const top = Math.max(bounds.top, Math.min(state.start - offset, state.y));
    const bottom = Math.min(bounds.bottom, Math.max(state.start - offset, state.y));
    Object.assign(state.box.style, { top: `${top}px`, height: `${Math.max(0, bottom - top)}px` });
  }

  function frame(time) {
    const state = drag;
    if (!state?.active) return;
    if (isDisabled() || !state.container.isConnected) { stop(); return; }
    const bounds = viewport(state);
    const edge = Math.min(40, (bounds.bottom - bounds.top) / 4);
    const distance = state.y < bounds.top + edge ? state.y - bounds.top - edge
      : state.y > bounds.bottom - edge ? state.y - bounds.bottom + edge : 0;
    const elapsed = Math.min(32, Math.max(0, time - (state.lastTime ?? time)));
    state.lastTime = time;
    if (distance) state.host.scrollTop += Math.sign(distance) * Math.min(900, 120 + Math.abs(distance) * 12) * elapsed / 1000;
    update(state);
    state.frame = state.win.requestAnimationFrame(frame);
  }

  function move(event) {
    const state = drag;
    if (!state || event.pointerId !== state.pointerId) return;
    if ((event.buttons & 1) === 0 || isDisabled()) { stop(); return; }
    state.y = event.clientY;
    if (!state.active && Math.hypot(event.clientX - state.x, state.y - state.initialY) < 4) return;
    event.preventDefault();
    if (!state.active) {
      state.active = true;
      state.oldUserSelect = state.doc.documentElement.style.userSelect;
      state.doc.documentElement.style.userSelect = "none";
      state.box = state.doc.createElement("div");
      state.box.className = "checkbox-drag-selection";
      Object.assign(state.box.style, {
        position: "fixed", pointerEvents: "none", zIndex: "5000",
        border: "1px solid #409eff", background: "rgb(64 158 255 / 12%)",
        borderRadius: "3px", left: `${state.left}px`, width: `${state.width}px`,
      });
      state.doc.body.append(state.box);
      onActiveChange(true);
      state.frame = state.win.requestAnimationFrame(frame);
    }
    update(state);
  }

  function pointerDown(event) {
    stop();
    clearClick?.();
    if (event.button !== 0 || event.isPrimary === false || isDisabled()) return;
    const container = getContainer();
    if (!container) return;
    const cell = event.target.closest?.(".el-table-v2__row-cell, td.el-table__cell, .virtual-data-table__cell");
    const node = event.target.closest?.(SELECTOR) || cell?.querySelector(SELECTOR);
    if (!node || !container.contains(node) || node.dataset.selectionDisabled === "true" || node.querySelector("input:disabled")) return;
    const id = Number(node.dataset.selectionId);
    if (!Number.isSafeInteger(id) || id < 1) return;
    const doc = container.ownerDocument;
    const win = doc.defaultView;
    let host = node.parentElement;
    while (host && host !== doc.body) {
      const overflow = win.getComputedStyle(host).overflowY;
      // Element Plus virtual grids intentionally hide native scrollbars, but
      // their scroll event still synchronizes the main and pinned columns.
      const virtualWindow = overflow === "hidden" && host.clientHeight > 80 && host.closest(".el-table-v2");
      if (host.scrollHeight > host.clientHeight + 1 && (/auto|scroll/.test(overflow) || virtualWindow)) break;
      host = host.parentElement;
    }
    host = host && host !== doc.body ? host : doc.scrollingElement;
    const initial = new Set(getSelected());
    const rect = node.getBoundingClientRect();
    drag = { container, doc, win, host, initial, rows: new Map(),
      pointerId: event.pointerId, x: event.clientX, y: event.clientY, initialY: event.clientY,
      mode: event.ctrlKey || event.metaKey || event.altKey ? "invert" : initial.has(id) ? "remove" : "add",
      active: false, frame: 0, left: rect.left - 4, width: rect.width + 8,
      lastKey: [...initial].join(","),
    };
    drag.start = event.clientY + host.scrollTop - origin(drag);
    doc.addEventListener("pointermove", move, { capture: true, passive: false });
    doc.addEventListener("pointerup", finish, true);
    doc.addEventListener("pointercancel", cancel, true);
    doc.addEventListener("visibilitychange", visibility);
    win.addEventListener("blur", stop);
  }

  return { pointerDown, stop };
}

export const LIVE_REFRESH_INTERVAL_MS = 30_000;
export const LIVE_REFRESH_ACTIVE_INTERVAL_MS = 5_000;
export const LIVE_REFRESH_MAX_INTERVAL_MS = 120_000;

function globalTarget(name) {
  return typeof globalThis[name] === "undefined" ? null : globalThis[name];
}

export function createLiveRefresh(refresh, options = {}) {
  if (typeof refresh !== "function") {
    throw new TypeError("refresh must be a function");
  }

  const intervalMs = options.intervalMs ?? LIVE_REFRESH_INTERVAL_MS;
  if (!Number.isFinite(intervalMs) || intervalMs < 0) {
    throw new RangeError("intervalMs must be a non-negative finite number");
  }

  const documentTarget =
    options.documentTarget === undefined
      ? globalTarget("document")
      : options.documentTarget;
  const windowTarget =
    options.windowTarget === undefined
      ? globalTarget("window")
      : options.windowTarget;
  const navigatorTarget = options.navigatorTarget === undefined
    ? globalTarget("navigator")
    : options.navigatorTarget;
  const setTimeoutFn = options.setTimeoutFn || globalThis.setTimeout;
  const clearTimeoutFn = options.clearTimeoutFn || globalThis.clearTimeout;
  const onError =
    typeof options.onError === "function" ? options.onError : () => {};
  const getIntervalMs = typeof options.getIntervalMs === "function" ? options.getIntervalMs : null;

  let active = false;
  let timer = null;
  let inFlight = null;
  let failures = 0;

  function isHidden() {
    return Boolean(documentTarget?.hidden);
  }

  function clearScheduled() {
    if (timer === null) return;
    clearTimeoutFn(timer);
    timer = null;
  }

  function schedule() {
    clearScheduled();
    if (!active || isHidden() || navigatorTarget?.onLine === false || inFlight) return;
    let intervalMs = intervalMsDefault();
    if (failures && intervalMs <= LIVE_REFRESH_MAX_INTERVAL_MS) {
      intervalMs = Math.min(LIVE_REFRESH_MAX_INTERVAL_MS, intervalMs * 2 ** failures);
    }
    timer = setTimeoutFn(() => {
      timer = null;
      void runRefresh();
    }, intervalMs);
  }

  function intervalMsDefault() {
    let value = intervalMs;
    if (getIntervalMs) {
      try {
        const raw = getIntervalMs();
        const candidate = Number(raw);
        if (typeof raw === "number" && Number.isFinite(candidate) && candidate > 0) value = candidate;
      } catch { /* use default */ }
    }
    return value;
  }

  function reportError(error) {
    try {
      onError(error);
    } catch {
      // Error reporting must not stop later refreshes.
    }
  }

  function runRefresh() {
    clearScheduled();
    if (!active || isHidden() || navigatorTarget?.onLine === false) return Promise.resolve(false);
    if (inFlight) return inFlight;

    const request = Promise.resolve()
      .then(refresh)
      .then(
        (result) => {
          if (result === false) {
            failures += 1;
            return false;
          }
          failures = 0;
          return true;
        },
        (error) => {
          failures += 1;
          reportError(error);
          return false;
        },
      )
      .finally(() => {
        if (inFlight === request) {
          inFlight = null;
        }
        schedule();
      });
    inFlight = request;
    return request;
  }

  function handleVisibilityChange() {
    if (isHidden()) {
      clearScheduled();
      return;
    }
    void runRefresh();
  }

  function handleFocus() {
    if (!isHidden()) {
      void runRefresh();
    }
  }

  function handleOnline() { if (active) void runRefresh(); }
  function handleOffline() { clearScheduled(); }

  function addListeners() {
    documentTarget?.addEventListener?.(
      "visibilitychange",
      handleVisibilityChange,
    );
    windowTarget?.addEventListener?.("focus", handleFocus);
    windowTarget?.addEventListener?.("online", handleOnline);
    windowTarget?.addEventListener?.("offline", handleOffline);
  }

  function removeListeners() {
    documentTarget?.removeEventListener?.(
      "visibilitychange",
      handleVisibilityChange,
    );
    windowTarget?.removeEventListener?.("focus", handleFocus);
    windowTarget?.removeEventListener?.("online", handleOnline);
    windowTarget?.removeEventListener?.("offline", handleOffline);
  }

  return {
    start({ immediate = true } = {}) {
      if (active) return inFlight || Promise.resolve(false);
      active = true;
      addListeners();
      if (immediate) return runRefresh();
      schedule();
      return Promise.resolve(false);
    },

    refreshNow() {
      return runRefresh();
    },

    stop() {
      if (!active) return;
      active = false;
      clearScheduled();
      removeListeners();
    },

    isRunning() {
      return active;
    },
  };
}

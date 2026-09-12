import { isAliasDeletionJobActive } from "./aliasDeletionJob.js";

export function createAliasDeletionVisibility({
  onChange,
  setTimeoutFn = globalThis.setTimeout,
  clearTimeoutFn = globalThis.clearTimeout,
  delayMs = 1800,
}) {
  let state = {};
  let lastJobId = "";
  let activeJobId = "";
  let completedJobId = "";
  let dismissedJobId = "";
  let timer = null;
  let previousVisibility;
  let stopped = false;

  function clearTimer() {
    if (timer !== null) clearTimeoutFn(timer);
    timer = null;
  }

  function publish() {
    const job = state.job;
    const visible = Boolean(
      state.submitting || state.uncertain || isAliasDeletionJobActive(job) ||
      (job?.jobId && job.jobId !== dismissedJobId &&
        (job.status === "interrupted" ||
          (job.status === "completed" && job.jobId === completedJobId))),
    );
    if (!stopped && visible !== previousVisibility) {
      previousVisibility = visible;
      onChange(visible);
    }
  }

  return {
    update(next) {
      if (stopped) return;
      state = next || {};
      const id = state.job?.jobId || "";
      if (id !== lastJobId) {
        clearTimer();
        lastJobId = id;
        activeJobId = "";
        completedJobId = "";
        dismissedJobId = "";
      }
      if (isAliasDeletionJobActive(state.job)) {
        clearTimer();
        activeJobId = id;
        completedJobId = "";
        dismissedJobId = "";
      } else if (id && state.job?.status === "completed" &&
          activeJobId === id && completedJobId !== id) {
        // Only a completion observed on this page gets a brief result panel.
        // Restoring historical results must not bring the panel back.
        completedJobId = id;
        timer = setTimeoutFn(() => {
          timer = null;
          dismissedJobId = id;
          publish();
        }, delayMs);
      }
      publish();
    },
    dismiss() {
      if (stopped || !["completed", "interrupted"].includes(state.job?.status)) return;
      clearTimer();
      dismissedJobId = state.job.jobId;
      publish();
    },
    stop() {
      stopped = true;
      clearTimer();
    },
  };
}

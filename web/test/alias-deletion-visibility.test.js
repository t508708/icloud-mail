import assert from "node:assert/strict";
import test from "node:test";

import { createAliasDeletionVisibility } from "../src/utils/aliasDeletionVisibility.js";

function fakeTimers() {
  let nextId = 0;
  const timers = new Map();
  return {
    setTimeoutFn(callback, delay) {
      const id = ++nextId;
      timers.set(id, { callback, delay });
      return id;
    },
    clearTimeoutFn(id) { timers.delete(id); },
    fireNext() {
      const entry = timers.entries().next().value;
      assert.ok(entry, "expected a scheduled timer");
      const [id, timer] = entry;
      timers.delete(id);
      timer.callback();
      return timer.delay;
    },
    count() { return timers.size; },
    delays() { return [...timers.values()].map(({ delay }) => delay); },
  };
}

function harness(options = {}) {
  const timers = fakeTimers();
  const changes = [];
  const visibility = createAliasDeletionVisibility({
    onChange: (value) => changes.push(value),
    ...timers,
    ...options,
  });
  return { visibility, timers, changes };
}

const state = (status, jobId = "job-1", patch = {}) => ({
  job: { status, jobId },
  recovering: false,
  uncertain: false,
  ...patch,
});

test("initial recovery states hide completed jobs and missing jobs", () => {
  const { visibility, changes } = harness();
  visibility.update(state("completed"));
  visibility.update({ job: null, recovering: true, uncertain: false });
  assert.deepEqual(changes, [false]);
});

test("active queued/running/submitting/uncertain states remain visible", () => {
  const { visibility, changes } = harness();
  visibility.update(state("queued"));
  visibility.update(state("running"));
  visibility.update({ ...state("completed"), submitting: true });
  visibility.update(state("completed", "job-1", { uncertain: true }));
  assert.deepEqual(changes, [true]);
});

test("a completed job seen active stays visible, then hides after the delay", () => {
  const { visibility, timers, changes } = harness();
  visibility.update(state("running"));
  visibility.update(state("completed"));
  assert.deepEqual(changes, [true]);
  assert.deepEqual(timers.delays(), [1800]);
  timers.fireNext();
  assert.deepEqual(changes, [true, false]);
});

test("repeated completed polling does not reset the hide timer", () => {
  const { visibility, timers, changes } = harness();
  visibility.update(state("running"));
  visibility.update(state("completed"));
  visibility.update(state("completed"));
  assert.deepEqual(timers.delays(), [1800]);
  timers.fireNext();
  assert.deepEqual(changes, [true, false]);
});

test("a new task restores visibility and uncertain restores a hidden view", () => {
  const { visibility, timers, changes } = harness();
  visibility.update(state("running", "old"));
  visibility.update(state("completed", "old"));
  timers.fireNext();
  visibility.update(state("running", "new"));
  assert.equal(changes.at(-1), true);
  visibility.update(state("completed", "new"));
  timers.fireNext();
  visibility.update(state("completed", "new", { uncertain: true }));
  assert.equal(changes.at(-1), true);
});

test("interrupted remains visible until dismiss, and stop cancels timers", () => {
  const { visibility, timers, changes } = harness();
  visibility.update(state("interrupted"));
  assert.equal(changes.at(-1), true);
  assert.equal(timers.count(), 0);
  visibility.dismiss();
  assert.equal(changes.at(-1), false);
  visibility.update(state("running"));
  visibility.update(state("completed"));
  assert.equal(timers.count(), 1);
  visibility.stop();
  assert.equal(timers.count(), 0);
});

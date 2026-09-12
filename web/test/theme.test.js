import test from "node:test";
import assert from "node:assert/strict";

const listeners = {};
let stored = null;
const html = {
  dataset: {},
  classList: { toggle(name, value) { this[name] = value; } },
  style: {},
};
globalThis.document = { documentElement: html, createElement() { return {}; } };
globalThis.window = {
  localStorage: {
    getItem() { return stored; },
    setItem(_, value) { stored = value; },
  },
  matchMedia() {
    return { matches: false, addEventListener(_, callback) { listeners.media = callback; } };
  },
  addEventListener(name, callback) { listeners[name] = callback; },
};
const theme = await import("../src/stores/theme.js");

test("theme initializes from storage and applies the resolved html state", () => {
  theme.setTheme("dark");
  assert.equal(theme.useTheme().preference.value, "dark");
  assert.equal(html.dataset.theme, "dark");
  assert.equal(html.classList.dark, true);
  assert.equal(html.style.colorScheme, "dark");
});

test("system preference and storage events update the actual theme refs", () => {
  theme.setTheme("system");
  listeners.media({ matches: true });
  assert.equal(theme.useTheme().resolvedTheme.value, "dark");
  listeners.storage({ key: theme.THEME_STORAGE_KEY, newValue: "light" });
  assert.equal(theme.useTheme().preference.value, "light");
  assert.equal(html.dataset.theme, "light");
  listeners.storage({ key: null, newValue: null });
  assert.equal(theme.useTheme().preference.value, "system");
});

test("invalid values and storage failures do not prevent switching", () => {
  theme.setTheme("invalid");
  assert.equal(theme.useTheme().preference.value, "system");
  window.localStorage.setItem = () => { throw new Error("quota"); };
  theme.setTheme("dark");
  assert.equal(theme.useTheme().preference.value, "dark");
  assert.equal(html.dataset.theme, "dark");
});

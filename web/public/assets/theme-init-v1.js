// Apply appearance before the app stylesheet paints; all resources stay same-origin.
(function () {
  var preference = "system";
  try { preference = localStorage.getItem("icloud-admin-theme") || "system"; } catch (_) {}
  var dark = preference === "dark" || (preference !== "light" && preference !== "dark" && window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
  var root = document.documentElement;
  root.classList.toggle("dark", dark);
  root.dataset.theme = dark ? "dark" : "light";
  root.style.colorScheme = dark ? "dark" : "light";
  root.style.backgroundColor = dark ? "#11151d" : "#edf1f7";
})();

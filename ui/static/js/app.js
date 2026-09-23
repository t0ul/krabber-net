// Small behaviours that would otherwise be inline scripts, which the Content
// Security Policy blocks.

// Forms marked data-reset-on-success clear after a successful htmx post.
document.addEventListener("htmx:afterRequest", function (event) {
  var form = event.target.closest && event.target.closest("form[data-reset-on-success]");
  if (form && event.detail.successful) {
    form.reset();
  }
});

// A molt inserted into an empty list replaces the "nothing here" message.
document.addEventListener("htmx:afterSwap", function (event) {
  if (event.target.id === "molt-list" || event.target.id === "reply-list") {
    var empty = document.getElementById("empty-list");
    if (empty) empty.remove();
  }
  localizeTimes(event.target);
});

// A deleted molt leaves the page: an undone remolt removes just that entry,
// a deleted molt removes every entry showing it.
document.addEventListener("moltDeleted", function (event) {
  var d = event.detail || {};
  if (!d.id) return;
  if (d.remolt) {
    var entry = document.getElementById("molt-" + d.id);
    if (entry) entry.remove();
    return;
  }
  document.querySelectorAll('.mini-molt[data-molt-id="' + CSS.escape(d.id) + '"]').forEach(function (el) {
    el.remove();
  });
});

// Only one "…" menu is open at a time, and clicking elsewhere closes it.
document.addEventListener("click", function (event) {
  document.querySelectorAll("details.kb-menu[open]").forEach(function (menu) {
    if (!menu.contains(event.target)) menu.removeAttribute("open");
  });
});

// Relative times ("5m") show the exact moment in the reader's own timezone on hover.
function localizeTimes(root) {
  (root || document).querySelectorAll("time[datetime]").forEach(function (el) {
    var d = new Date(el.getAttribute("datetime"));
    if (!isNaN(d)) {
      el.title = d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
    }
  });
}

document.addEventListener("DOMContentLoaded", function () {
  localizeTimes(document);
  var filter = document.getElementById("crab-filter");
  if (!filter) return;
  var rows = document.querySelectorAll(".crab-row[data-name]");
  var count = document.getElementById("crab-count");
  filter.addEventListener("input", function () {
    var q = filter.value.trim().toLowerCase();
    var shown = 0;
    rows.forEach(function (row) {
      var match = !q || (row.getAttribute("data-name") || "").toLowerCase().indexOf(q) !== -1;
      row.hidden = !match;
      if (match) shown += 1;
    });
    if (count) count.textContent = shown + (shown === 1 ? " crab" : " crabs");
  });
});

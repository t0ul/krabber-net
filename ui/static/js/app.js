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
  if (event.target.id === "molt-list") {
    var empty = document.getElementById("empty-list");
    if (empty) empty.remove();
  }
  localizeTimes(event.target);
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

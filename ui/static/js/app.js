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
  if (event.target.id === "molt-modal-content") {
    openModal(document.getElementById("molt-modal"));
  }
  localizeTimes(event.target);
  initCounters(document);
});

// A rejected quote or edit (422) swaps in the re-filled form with its error.
document.addEventListener("htmx:beforeSwap", function (event) {
  var elt = event.detail.elt;
  if (event.detail.xhr.status === 422 && elt.closest && elt.closest("[data-swap-errors]")) {
    event.detail.shouldSwap = true;
    event.detail.isError = false;
  }
});

// Modals. The sidebar's Molt button opens the compose modal (its link leads
// to the Trench's compose box without JavaScript); Quote and Edit load their
// forms into the molt modal over htmx.
function openModal(dialog) {
  if (!dialog) return;
  document.querySelectorAll("details.kb-menu[open]").forEach(function (menu) {
    menu.removeAttribute("open");
  });
  if (!dialog.open) dialog.showModal();
  var textarea = dialog.querySelector("textarea");
  if (textarea) {
    textarea.focus();
    textarea.setSelectionRange(textarea.value.length, textarea.value.length);
  }
  initCounters(dialog);
}

// A click that starts and ends on the backdrop (the dialog itself) closes it,
// so a text selection dragged out of the form doesn't.
var pressedOn = null;
document.addEventListener("mousedown", function (event) {
  pressedOn = event.target;
});

document.addEventListener("click", function (event) {
  var target = event.target;
  var opener = target.closest && target.closest("[data-open-modal]");
  if (opener) {
    var dialog = document.getElementById(opener.getAttribute("data-open-modal"));
    if (dialog) {
      event.preventDefault();
      openModal(dialog);
    }
    return;
  }
  if (target.closest && target.closest("[data-close-modal]")) {
    target.closest("dialog").close();
  } else if (target.tagName === "DIALOG" && pressedOn === target) {
    target.close();
  }
});

// Compose counters count down from 280 and turn red past it; Molt stays
// disabled while the text is blank or too long, as in Crabber.
var moltLimit = 280;

function updateCounter(form) {
  var textarea = form.querySelector("textarea[name=content]");
  var counter = form.querySelector(".mini-character-counter");
  if (!textarea || !counter) return;
  var length = Array.from(textarea.value).length;
  counter.classList.remove("d-none");
  counter.textContent = moltLimit - length;
  counter.classList.toggle("text-primary", length > moltLimit);
  var button = form.querySelector("button[type=submit]");
  if (button) button.disabled = !textarea.value.trim() || length > moltLimit;
}

function initCounters(root) {
  root.querySelectorAll("form .mini-character-counter").forEach(function (counter) {
    updateCounter(counter.closest("form"));
  });
}

document.addEventListener("input", function (event) {
  var form = event.target.form;
  if (form && event.target.name === "content") updateCounter(form);
});

document.addEventListener("reset", function (event) {
  var form = event.target;
  setTimeout(function () { updateCounter(form); }, 0);
});

// Escape closes an open modal, including one opened after an htmx response
// (without a user gesture), which the browser may not close on its own.
// Ctrl+Enter (Cmd+Enter on a Mac) sends the molt being written.
document.addEventListener("keydown", function (event) {
  if (event.key === "Escape") {
    var open = document.querySelector("dialog.kb-modal[open]");
    if (open) {
      event.preventDefault();
      open.close();
    }
    return;
  }
  if (event.key !== "Enter" || !(event.ctrlKey || event.metaKey)) return;
  var form = event.target.tagName === "TEXTAREA" && event.target.form;
  if (!form) return;
  event.preventDefault();
  var button = form.querySelector("button[type=submit]");
  if (button && !button.disabled) form.requestSubmit(button);
});

// Molt text sits above the molt's own link so its mentions and crabtags are
// clickable; a click elsewhere in the text (not a text selection) still opens
// the molt, as in Crabber.
document.addEventListener("click", function (event) {
  var text = event.target.closest && event.target.closest(".molt-text[data-href]");
  if (!text || event.target.closest("a") || String(window.getSelection())) return;
  window.location.href = text.getAttribute("data-href");
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

// Plain forms marked data-confirm ask before submitting (htmx forms use hx-confirm).
document.addEventListener("submit", function (event) {
  var form = event.target;
  var message = form.getAttribute && form.getAttribute("data-confirm");
  if (message && !window.confirm(message)) {
    event.preventDefault();
  }
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
  initCounters(document);
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

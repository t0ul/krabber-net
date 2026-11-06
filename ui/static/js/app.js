// Small behaviours that would otherwise be inline scripts, which the Content
// Security Policy blocks.

// The "new molts" banner and the notification badge poll on a kb:poll event,
// sent once a minute while the tab is visible and someone has used the page
// in the last five minutes. A tab left open in the background or on a
// forgotten screen makes no requests; coming back polls right away.
(function () {
  var every = 60 * 1000;
  var idleAfter = 5 * 60 * 1000;
  var lastActive = Date.now();
  var lastPoll = Date.now();
  ["pointerdown", "pointermove", "keydown", "scroll", "touchstart"].forEach(function (name) {
    document.addEventListener(name, function () { lastActive = Date.now(); }, { passive: true, capture: true });
  });
  function poll() {
    lastPoll = Date.now();
    document.body.dispatchEvent(new CustomEvent("kb:poll"));
  }
  setInterval(function () {
    var now = Date.now();
    if (document.visibilityState === "visible" && now - lastActive < idleAfter && now - lastPoll >= every) poll();
  }, 5000);
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState !== "visible") return;
    lastActive = Date.now();
    if (Date.now() - lastPoll >= every) poll();
  });
})();

// The service worker makes Krabber installable and shows an offline page.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch(function () {});
}

// Forms marked data-reset-on-success clear after a successful htmx post. Only
// the form's own post counts: the feedback box inside it makes its own request
// (the live check), and since its answer swaps the box out, htmx reports that
// request's end on the form itself. The request's path tells the two apart.
document.addEventListener("htmx:afterRequest", function (event) {
  var form = event.target;
  var path = event.detail.pathInfo && event.detail.pathInfo.requestPath;
  if (form.matches && form.matches("form[data-reset-on-success]") && event.detail.successful && path === form.getAttribute("hx-post")) {
    form.reset();
    var feedback = form.querySelector(".kb-feedback");
    if (feedback) {
      feedback.replaceChildren();
      feedback.classList.remove("kb-feedback-refused");
    }
    buildShape(form);
    if (form.hasAttribute("data-molt-modal")) moltFromModal(form, event.detail.xhr);
  }
});

// The compose modal posts over htmx without a target of its own: the molt it
// gets back goes to the top of the feed when there is one on the page, and
// the modal closes; on a page without a feed the Trench shows it.
function moltFromModal(form, xhr) {
  var dialog = form.closest("dialog");
  if (dialog && dialog.open) dialog.close();
  var list = document.getElementById("molt-list");
  if (!list || !xhr || !xhr.responseText) {
    window.location.href = "/trench";
    return;
  }
  list.insertAdjacentHTML("afterbegin", xhr.responseText);
  if (window.htmx && list.firstElementChild) htmx.process(list.firstElementChild);
  var empty = document.getElementById("empty-list");
  if (empty) empty.remove();
  localizeTimes(list);
  syncNewMoltsSince(list);
  fitDropped(list);
}

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
  var list = document.getElementById("molt-list");
  if (list) localizeTimes(list);
  if (event.target.id === "molt-list") {
    syncNewMoltsSince(list);
  }
  initComposers(document);
  paintMeasures(document);
  fitDropped(event.target);
});

// A molt prepended to the feed (compose) moves the "new molts" cursor so
// the banner doesn't count the one you just wrote.
function syncNewMoltsSince(list) {
  var box = document.getElementById("new-molts");
  var first = list && list.querySelector("[data-molt-id]");
  if (!box || !first) return;
  var path = box.getAttribute("hx-get") || "";
  var q = path.indexOf("?");
  var base = q === -1 ? path : path.slice(0, q);
  box.setAttribute("hx-get", base + "?since=" + encodeURIComponent(first.getAttribute("data-molt-id")));
}

// A write refused for going too fast (429) says so instead of doing nothing.
document.addEventListener("htmx:responseError", function (event) {
  var xhr = event.detail.xhr;
  if (xhr.status === 429) window.alert(xhr.responseText.trim());
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
  initComposers(dialog);
  var field = dialog.querySelector(".kb-shape:not([hidden]) .kb-field") || dialog.querySelector("textarea[name=content]");
  if (field) {
    field.focus();
    if (field.setSelectionRange) field.setSelectionRange(field.value.length, field.value.length);
  }
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

// Compose counters count down from the chosen writing form's limit (280 for
// a plain molt; a sonnet or an essay gets more) and turn red past it; Molt
// stays disabled while the text is blank or too long, as in Crabber.
function moltLimit(form) {
  var select = form.querySelector("select[name=form]");
  var option = select && select.options[select.selectedIndex];
  var max = option ? parseInt(option.getAttribute("data-max"), 10) : 0;
  return max > 0 ? max : 280;
}

function updateCounter(form) {
  var textarea = form.querySelector("textarea[name=content]");
  var counter = form.querySelector(".mini-character-counter");
  if (!textarea || !counter) return;
  var limit = moltLimit(form);
  var length = Array.from(textarea.value).length;
  counter.classList.remove("d-none");
  counter.textContent = limit - length;
  counter.classList.toggle("text-primary", length > limit);
  var button = submitButton(form);
  if (button) button.disabled = !textarea.value.trim() || length > limit;
}

// The form's own Molt / Reply / Save button, not the feedback box's "post it
// anyway", which is a submit button too.
function submitButton(form) {
  return form.querySelector("button[type=submit]:not([name=override])");
}

// initComposers gives every composer under root its counter and, for a form
// with a shape, its boxes; a composer already built for its form is left as
// it is (a check's answer swapping the feedback box must not rebuild it).
function initComposers(root) {
  root.querySelectorAll("form .mini-character-counter").forEach(function (counter) {
    var form = counter.closest("form");
    var box = form.querySelector(".kb-shape");
    var select = form.querySelector("select[name=form]");
    if (box && select && box.getAttribute("data-shape") !== select.value) buildShape(form);
    applyLayout(form);
    updateCounter(form);
  });
}

// Shaped composers. A form with a shape (three lines for a haiku; setup,
// build and punchline for a joke) is written in one box per line or part.
// The boxes carry no name: they write the molt's text into the form's
// textarea, which is what's posted, so the server sees one text whether or
// not JavaScript ran. After each check the server's measure of every line
// (its syllables against the count wanted, the letter of its rhyme) shows
// beside the line.
var shapes = {};
(function () {
  var el = document.getElementById("kb-shapes");
  try { shapes = el ? JSON.parse(el.textContent) : {}; } catch (e) { shapes = {}; }
})();

function shapeOf(form) {
  var select = form.querySelector("select[name=form]");
  var shape = select && shapes[select.value];
  return shape && (shape.lines || shape.parts) ? shape : null;
}

function lineSpec(shape, index) {
  if (index < shape.lines.length) return shape.lines[index];
  return shape.grow ? shape.lines[shape.lines.length - 1] : {};
}

function syllableTarget(spec) {
  var s = spec && spec.syllables;
  if (!s || !s[1]) return "";
  return s[0] === s[1] ? String(s[0]) : s[0] + "\u2013" + s[1];
}

function lineRow(shape, index, value) {
  var row = document.createElement("div");
  row.className = "kb-line";
  var rhyme = document.createElement("span");
  rhyme.className = "kb-line-rhyme";
  rhyme.setAttribute("aria-hidden", "true");
  var input = document.createElement("input");
  input.type = "text";
  input.className = "kb-field kb-line-input";
  input.autocomplete = "off";
  input.placeholder = "Line " + (index + 1);
  input.setAttribute("aria-label", "Line " + (index + 1));
  input.value = value || "";
  var meter = document.createElement("span");
  meter.className = "kb-line-meter";
  var target = syllableTarget(lineSpec(shape, index));
  meter.textContent = target ? "\u00b7/" + target : "";
  meter.title = target ? "syllables heard / wanted" : "syllables heard";
  row.append(rhyme, input, meter);
  return row;
}

function partBox(spec, index, value) {
  var wrap = document.createElement("label");
  wrap.className = "kb-part";
  var name = document.createElement("span");
  name.className = "kb-part-label";
  name.textContent = spec.label;
  var field = document.createElement(spec.multi ? "textarea" : "input");
  if (spec.multi) {
    field.rows = index === 0 ? 2 : 3;
  } else {
    field.type = "text";
    field.autocomplete = "off";
  }
  field.className = "kb-field kb-part-field";
  field.placeholder = spec.hint || "";
  field.value = value || "";
  wrap.append(name, field);
  return wrap;
}

// assignParts hands the paragraphs of a text to a form's parts: the ones
// before the part that takes several (a letter's body, an essay) in order,
// the ones after it from the end, and the rest to that part.
function assignParts(specs, pieces) {
  var values = specs.map(function () { return ""; });
  var m = specs.findIndex(function (spec) { return spec.multi; });
  if (m === -1) {
    specs.forEach(function (_, i) { values[i] = pieces[i] || ""; });
    return values;
  }
  var trail = specs.length - m - 1;
  for (var i = 0; i < m; i++) values[i] = pieces[i] || "";
  var rest = pieces.slice(m);
  var tail = rest.length > 1 ? rest.splice(Math.max(1, rest.length - trail), trail) : [];
  values[m] = rest.join("\n\n");
  tail.forEach(function (piece, i) { values[m + 1 + i] = piece; });
  return values;
}

// sealLines makes an envelope's last line a mirror of its first; a line
// that was the mirror and no longer is (a line was added after it) starts
// empty, for the writer's own words.
function sealLines(box, shape) {
  var inputs = box.querySelectorAll(".kb-line-input");
  inputs.forEach(function (input, i) {
    var sealed = Boolean(shape.seal && inputs.length > 1 && i === inputs.length - 1);
    if (input.classList.contains("kb-line-sealed") && !sealed) input.value = "";
    input.readOnly = sealed;
    input.classList.toggle("kb-line-sealed", sealed);
    if (sealed) {
      input.placeholder = "The first line, again";
      input.tabIndex = -1;
    } else {
      input.placeholder = "Line " + (i + 1);
      input.tabIndex = 0;
    }
  });
}

// addLine adds a line at the end, or, in an envelope, just before the
// mirror of the first line, which stays last.
function addLine(form) {
  var box = form.querySelector(".kb-shape");
  var shape = shapeOf(form);
  if (!box || !shape || !shape.lines) return null;
  var rows = box.querySelectorAll(".kb-line");
  var row = lineRow(shape, rows.length, "");
  var before = shape.seal && rows.length ? rows[rows.length - 1] : box.querySelector(".kb-line-add");
  box.insertBefore(row, before);
  sealLines(box, shape);
  syncShape(form, false);
  return row.querySelector(".kb-line-input");
}

// buildShape lays the form's boxes out over the textarea's text, or shows
// the textarea itself for a form without a shape.
function buildShape(form) {
  var textarea = form.querySelector("textarea[name=content]");
  var box = form.querySelector(".kb-shape");
  var select = form.querySelector("select[name=form]");
  if (!textarea || !box) return;
  var shape = shapeOf(form);
  box.replaceChildren();
  box.setAttribute("data-shape", select ? select.value : "");
  if (!shape) {
    box.hidden = true;
    textarea.hidden = false;
    return;
  }
  var text = textarea.value;
  if (shape.lines) {
    var lines = text.split("\n").map(function (l) { return l.trim(); }).filter(Boolean);
    var count = shape.lines.length;
    if (shape.grow) {
      count = Math.max(count, lines.length);
    } else if (lines.length > count) { // keep every word: the overflow joins the last line
      lines = lines.slice(0, count - 1).concat(lines.slice(count - 1).join(" "));
    }
    for (var i = 0; i < count; i++) box.append(lineRow(shape, i, lines[i]));
    if (shape.grow) {
      var add = document.createElement("button");
      add.type = "button";
      add.className = "btn btn-link btn-sm p-0 kb-line-add";
      add.textContent = "Add a line";
      box.append(add);
    }
    sealLines(box, shape);
  } else {
    var pieces = text.split(/\n[ \t]*\n/).map(function (piece) { return piece.trim(); }).filter(Boolean);
    var values = assignParts(shape.parts, pieces);
    shape.parts.forEach(function (spec, i) { box.append(partBox(spec, i, values[i])); });
  }
  box.hidden = false;
  textarea.hidden = true;
  syncShape(form, false);
  applyLayout(form);
}

// syncShape writes the boxes into the textarea: lines joined by a line
// break, parts by a blank line.
function syncShape(form, check) {
  var textarea = form.querySelector("textarea[name=content]");
  var box = form.querySelector(".kb-shape");
  var shape = shapeOf(form);
  if (!textarea || !box || box.hidden || !shape) return;
  var fields = Array.from(box.querySelectorAll(".kb-field"));
  var text;
  if (shape.lines) {
    if (shape.seal && fields.length > 1) fields[fields.length - 1].value = fields[0].value;
    text = fields.map(function (f) { return f.value.replace(/\n/g, " "); }).join("\n");
  } else {
    text = fields.map(function (f) { return f.value; }).join("\n\n");
  }
  textarea.value = text.replace(/\s+$/, "");
  updateCounter(form);
  if (check !== false) scheduleCheck(form, 250);
}

// paintMeasures shows what the server heard in each line, from the feedback
// box's last answer: the syllables against the count wanted, and the rhyme
// letter, each coloured by whether it's what the form asks.
function paintMeasures(root) {
  root.querySelectorAll(".kb-shape:not([hidden])").forEach(function (box) {
    var form = box.closest("form");
    var feedback = form.querySelector(".kb-feedback");
    var shape = shapeOf(form);
    if (!feedback || !shape || !shape.lines) return;
    var measures;
    try { measures = JSON.parse(feedback.getAttribute("data-lines") || "null"); } catch (e) { measures = null; }
    var heard = Array.isArray(measures) ? measures : [];
    var next = 0;
    box.querySelectorAll(".kb-line").forEach(function (row, index) {
      var input = row.querySelector(".kb-line-input");
      var meter = row.querySelector(".kb-line-meter");
      var rhyme = row.querySelector(".kb-line-rhyme");
      var spec = lineSpec(shape, index);
      var target = syllableTarget(spec);
      var m = input.value.trim() ? heard[next++] : null;
      row.setAttribute("data-rhyme", m ? m.rhyme : "");
      if (!m) {
        meter.textContent = target ? "\u00b7/" + target : "";
        meter.className = "kb-line-meter";
        rhyme.textContent = "";
        rhyme.className = "kb-line-rhyme";
        return;
      }
      meter.textContent = target ? m.syllables + "/" + target : String(m.syllables);
      meter.className = "kb-line-meter " + (m.syllables_ok ? "kb-meter-ok" : "kb-meter-off");
      rhyme.textContent = spec.rhyme ? m.rhyme : "";
      rhyme.className = "kb-line-rhyme " + (m.rhyme_ok ? "kb-rhyme-ok" : "kb-rhyme-off");
    });
    applyLayout(form);
  });
}

// A dropped line needs the width of the whole poem: each line starts where
// the one above ended. Where a line would wrap in the column it's shown in,
// the poem steps instead (the CSS does the same on phones without this).
function fitDropped(root) {
  (root || document).querySelectorAll(".kb-layout-dropped").forEach(function (poem) {
    poem.classList.remove("kb-dropped-narrow");
    var lines = poem.querySelectorAll(".kb-vl");
    if (!lines.length) return;
    var one = lines[0].getBoundingClientRect().height;
    for (var i = 1; i < lines.length; i++) {
      if (lines[i].getBoundingClientRect().height > one * 1.5) {
        poem.classList.add("kb-dropped-narrow");
        return;
      }
    }
  });
}

var fitTimer = null;
window.addEventListener("resize", function () {
  clearTimeout(fitTimer);
  fitTimer = setTimeout(function () { fitDropped(document); }, 150);
});
window.addEventListener("load", function () { fitDropped(document); });

// Layouts. Beside the form sits a list of the chosen form's layouts (they
// come with its shape), starting on the krab's own default for that form.
// The line boxes are set the way the layout sets the poem, using the same
// rule the page does (indents, stanza breaks, the frame of an envelope, the
// rhyme letters the last check heard), so a stepped haiku steps as it's
// written. A dropped line steps here; the page sets it properly.
var myLayouts = {};
(function () {
  var el = document.getElementById("kb-my-layouts");
  try { myLayouts = el ? JSON.parse(el.textContent) : {}; } catch (e) { myLayouts = {}; }
})();

function layoutsOf(formId) {
  var s = shapes[formId];
  return (s && s.layouts) || [];
}

// fillLayouts lists the chosen form's layouts, starting on the krab's default.
function fillLayouts(form) {
  var formSelect = form.querySelector("select[name=form]");
  var select = form.querySelector("select[name=layout]");
  if (!formSelect || !select) return;
  var list = layoutsOf(formSelect.value);
  var want = myLayouts[formSelect.value];
  select.replaceChildren();
  list.forEach(function (l, i) {
    var option = document.createElement("option");
    option.value = l.id;
    option.textContent = l.name;
    option.title = l.note;
    option.selected = l.id === want || (!want && i === 0);
    option.defaultSelected = option.selected;
    select.appendChild(option);
  });
  select.hidden = list.length === 0;
}

function currentLayout(form) {
  var formSelect = form.querySelector("select[name=form]");
  var select = form.querySelector("select[name=layout]");
  if (!formSelect || !select) return null;
  var list = layoutsOf(formSelect.value);
  for (var i = 0; i < list.length; i++) {
    if (list[i].id === select.value) return list[i];
  }
  return list[0] || null;
}

// applyLayout sets the composer's line boxes as the chosen layout sets the poem.
function applyLayout(form) {
  var box = form.querySelector(".kb-shape");
  if (!box) return;
  var layout = currentLayout(form);
  box.setAttribute("data-mode", layout ? layout.mode : "");
  var rows = box.querySelectorAll(".kb-line");
  var order = {};
  rows.forEach(function (row, i) {
    var indent = 0;
    var gap = false;
    if (layout) {
      if (layout.indents && i < layout.indents.length) {
        indent = layout.indents[i];
      } else if (layout.frame) {
        indent = i > 0 && i < rows.length - 1 ? 1 : 0;
      } else if (layout.by_rhyme) {
        var letter = row.getAttribute("data-rhyme");
        if (letter) {
          if (!(letter in order)) order[letter] = Object.keys(order).length;
          indent = order[letter] % 2;
        }
      }
      gap = !!layout.gaps && layout.gaps.indexOf(i) !== -1 && i < rows.length - 1;
    }
    for (var n = 1; n <= 3; n++) row.classList.toggle("kb-in-" + n, n === indent);
    row.classList.toggle("kb-gap", gap);
  });
}

// Choosing a writing form shows its rules in the form's feedback box, and the
// text is checked again a moment after typing pauses, while a form is chosen
// (a plain molt has no rules to check). The box asks the server over htmx.
var checkTimers = new WeakMap();

// Each answer replaces the box, so the box is looked up when the timer fires:
// one found earlier may already have been swapped out by a check in flight.
function scheduleCheck(form, delay) {
  if (!window.htmx) return;
  clearTimeout(checkTimers.get(form));
  checkTimers.set(form, setTimeout(function () {
    var feedback = form.querySelector(".kb-feedback");
    if (feedback) htmx.trigger(feedback, "kb:check");
  }, delay));
}

document.addEventListener("input", function (event) {
  var form = event.target.form;
  if (!form) return;
  if (event.target.classList.contains("kb-field")) {
    syncShape(form);
    return;
  }
  if (event.target.name !== "content") return;
  updateCounter(form);
  var select = form.querySelector("select[name=form]");
  if (select && select.value) scheduleCheck(form, 400);
});

document.addEventListener("change", function (event) {
  var form = event.target.form;
  if (!form) return;
  if (event.target.name === "layout") {
    applyLayout(form);
    return;
  }
  if (event.target.name !== "form") return;
  fillLayouts(form);
  buildShape(form);
  updateCounter(form);
  scheduleCheck(form, 0);
});

document.addEventListener("reset", function (event) {
  var form = event.target;
  setTimeout(function () {
    fillLayouts(form);
    buildShape(form);
    updateCounter(form);
  }, 0);
});

// Enter in a line box moves to the next line (adding one where the form
// grows); a paste with line breaks in it fills the lines that follow.
document.addEventListener("keydown", function (event) {
  var input = event.target;
  if (event.key !== "Enter" || event.shiftKey || event.ctrlKey || event.metaKey || !input.classList || !input.classList.contains("kb-line-input")) return;
  event.preventDefault();
  var rows = Array.from(input.form.querySelectorAll(".kb-line-input"));
  var next = rows[rows.indexOf(input) + 1];
  if (next && next.readOnly) next = null;
  if (!next) {
    var shape = shapeOf(input.form);
    if (shape && shape.grow) next = addLine(input.form);
  }
  if (next) next.focus();
});

document.addEventListener("paste", function (event) {
  var input = event.target;
  if (!input.classList || !input.classList.contains("kb-line-input") || !event.clipboardData) return;
  var text = event.clipboardData.getData("text/plain");
  if (text.indexOf("\n") === -1) return;
  event.preventDefault();
  var pieces = text.split("\n").map(function (l) { return l.trim(); }).filter(Boolean);
  var form = input.form;
  var shape = shapeOf(form);
  var rows = Array.from(form.querySelectorAll(".kb-line-input"));
  var at = rows.indexOf(input);
  pieces.forEach(function (piece, i) {
    var target = rows[at + i];
    if (!target && shape && shape.grow) {
      target = addLine(form);
      rows.push(target);
    }
    if (target && !target.readOnly) target.value = piece;
  });
  syncShape(form);
});

document.addEventListener("click", function (event) {
  var add = event.target.closest && event.target.closest(".kb-line-add");
  if (!add) return;
  var input = addLine(add.form);
  if (input) input.focus();
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
  var form = event.target.form;
  if (!form || !form.querySelector("textarea[name=content]")) return;
  event.preventDefault();
  var button = submitButton(form);
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

// A YouTube placeholder becomes the no-cookie player only when clicked, so
// nothing loads from YouTube until the reader asks.
document.addEventListener("click", function (event) {
  var play = event.target.closest && event.target.closest(".kb-youtube-play");
  if (!play) return;
  var box = play.closest("[data-youtube]");
  var id = box && box.getAttribute("data-youtube");
  if (!id || !/^[A-Za-z0-9_-]{11}$/.test(id)) return;
  event.preventDefault();
  var frame = document.createElement("iframe");
  frame.src = "https://www.youtube-nocookie.com/embed/" + id + "?autoplay=1";
  frame.title = "YouTube video";
  frame.allow = "autoplay; encrypted-media; picture-in-picture; fullscreen";
  frame.allowFullscreen = true;
  frame.referrerPolicy = "strict-origin-when-cross-origin";
  frame.setAttribute("sandbox", "allow-scripts allow-same-origin allow-presentation allow-popups");
  box.replaceChildren(frame);
  box.classList.add("playing");
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

// A wheel over the side columns scrolls the middle feed, as in Crabber.
document.addEventListener("wheel", function (event) {
  var body = document.getElementById("content-body");
  if (!body || body.contains(event.target) || event.target.closest("dialog")) return;
  body.scrollTop += event.deltaY;
}, { passive: true });

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
  initComposers(document);
  fitDropped(document);
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
    if (count) count.textContent = shown + (shown === 1 ? " krab" : " krabs");
  });
});

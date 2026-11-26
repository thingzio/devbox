// Keeps the queue and the header status live from server events, and copies handoff lines.
(function () {
  "use strict";
  function refresh(id) {
    var el = document.getElementById(id);
    if (el) {
      window.htmx.trigger(el, "refresh");
    }
  }
  if (window.EventSource && window.htmx) {
    var source = new EventSource("/events");
    source.addEventListener("change", function () {
      refresh("queue");
      refresh("sync");
    });
    source.addEventListener("poll", function () {
      refresh("sync");
    });
  }
  // Keyboard navigation over queue rows: j/k select, o opens on GitHub,
  // Enter opens the explanation, s opens the snooze menu, Esc clears.
  var selectedKey = null;
  function rows() {
    return Array.prototype.filter.call(document.querySelectorAll("[data-nav]"), function (el) {
      return el.offsetParent !== null;
    });
  }
  function selected() {
    return rows().find(function (el) {
      return el.getAttribute("data-key") === selectedKey;
    });
  }
  function select(el) {
    var previous = selected();
    if (previous) {
      previous.removeAttribute("aria-selected");
    }
    selectedKey = el ? el.getAttribute("data-key") : null;
    if (el) {
      el.setAttribute("aria-selected", "true");
      el.scrollIntoView({ block: "nearest" });
    }
  }
  function move(step) {
    var list = rows();
    if (list.length === 0) {
      return;
    }
    var at = list.indexOf(selected());
    var next = at < 0 ? (step > 0 ? 0 : list.length - 1) : Math.min(list.length - 1, Math.max(0, at + step));
    select(list[next]);
  }
  function typing(target) {
    return target.isContentEditable || /^(INPUT|SELECT|TEXTAREA)$/.test(target.tagName);
  }
  document.addEventListener("keydown", function (event) {
    if (event.ctrlKey || event.metaKey || event.altKey || typing(event.target)) {
      return;
    }
    var row = selected();
    switch (event.key) {
      case "j":
        move(1);
        break;
      case "k":
        move(-1);
        break;
      case "o":
        if (!row || !row.getAttribute("data-open")) {
          return;
        }
        window.open(row.getAttribute("data-open"), "_blank", "noopener");
        break;
      case "Enter":
        // A focused link, button, or menu keeps its own Enter.
        if (!row || /^(A|BUTTON|SUMMARY)$/.test(event.target.tagName)) {
          return;
        }
        window.location.assign(row.getAttribute("data-explain"));
        break;
      case "s":
        var menu = row && row.querySelector("details.snooze");
        if (!menu) {
          return;
        }
        menu.open = !menu.open;
        break;
      case "Escape":
        if (!row) {
          return;
        }
        select(null);
        break;
      default:
        return;
    }
    event.preventDefault();
  });
  // Live refreshes replace the rows; keep the same pull request selected.
  document.addEventListener("htmx:afterSwap", function () {
    var row = selected();
    if (row) {
      row.setAttribute("aria-selected", "true");
    }
  });

  document.addEventListener("click", function (event) {
    var button = event.target.closest("button.copy");
    if (!button || !navigator.clipboard) {
      return;
    }
    navigator.clipboard.writeText(button.getAttribute("data-copy")).then(function () {
      button.textContent = "Copied";
      setTimeout(function () {
        button.textContent = "Copy";
      }, 2000);
    });
  });
})();

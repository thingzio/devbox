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

// Refreshes the queue on server change events and copies handoff lines.
(function () {
  "use strict";
  var queue = document.getElementById("queue");
  if (queue && window.EventSource && window.htmx) {
    var source = new EventSource("/events");
    source.addEventListener("change", function () {
      window.htmx.trigger(queue, "refresh");
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

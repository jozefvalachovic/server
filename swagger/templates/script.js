(function () {
  "use strict";

  var filter = document.getElementById("filter");
  var btnExpand = document.getElementById("btn-expand");
  var btnCollapse = document.getElementById("btn-collapse");
  var noResults = document.getElementById("no-results");
  var endpoints = Array.from(document.querySelectorAll("details[data-method]"));

  /* ── Filter ─────────────────────────────────────────────────────────────── */

  function applyFilter() {
    var q = filter.value.trim().toLowerCase();
    var visible = 0;

    endpoints.forEach(function (el) {
      var method = (el.dataset.method || "").toLowerCase();
      var path = (el.dataset.path || "").toLowerCase();
      var sum = (el.dataset.summary || "").toLowerCase();
      var tags = (el.dataset.tags || "").toLowerCase();

      var match =
        !q ||
        method.indexOf(q) !== -1 ||
        path.indexOf(q) !== -1 ||
        sum.indexOf(q) !== -1 ||
        tags.indexOf(q) !== -1;

      el.classList.toggle("hidden", !match);
      if (match) visible++;
    });

    noResults.style.display = visible === 0 ? "block" : "none";
  }

  if (filter) {
    filter.addEventListener("input", applyFilter);

    /* Clear filter on Escape */
    filter.addEventListener("keydown", function (e) {
      if (e.key === "Escape") {
        filter.value = "";
        applyFilter();
      }
    });
  }

  /* ── Expand / Collapse all ───────────────────────────────────────────────── */

  if (btnExpand) {
    btnExpand.addEventListener("click", function () {
      endpoints.forEach(function (el) {
        if (!el.classList.contains("hidden")) {
          el.open = true;
        }
      });
    });
  }

  if (btnCollapse) {
    btnCollapse.addEventListener("click", function () {
      endpoints.forEach(function (el) {
        el.open = false;
      });
    });
  }

  /* ── Keyboard shortcut: "/" focuses the filter ──────────────────────────── */

  document.addEventListener("keydown", function (e) {
    if (e.key === "/" && document.activeElement !== filter) {
      e.preventDefault();
      if (filter) filter.focus();
    }
  });
})();

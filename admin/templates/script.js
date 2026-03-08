(function () {
  "use strict";

  /* ── Filter ─────────────────────────────────────────────────────────────── */

  var filterEl = document.getElementById("filter");
  var noResults = document.getElementById("no-results");

  function applyFilter() {
    if (!filterEl) return;
    var q = filterEl.value.trim().toLowerCase();
    var rows = Array.from(
      document.querySelectorAll("#routes-table tbody tr, #cache-table tbody tr"),
    );
    var visible = 0;
    rows.forEach(function (row) {
      var text = row.textContent.toLowerCase();
      var show = !q || text.indexOf(q) !== -1;
      row.style.display = show ? "" : "none";
      if (show) visible++;
    });
    if (noResults) {
      noResults.style.display = rows.length > 0 && visible === 0 ? "block" : "none";
    }
  }

  if (filterEl) {
    filterEl.addEventListener("input", applyFilter);
    filterEl.addEventListener("keydown", function (e) {
      if (e.key === "Escape") {
        filterEl.value = "";
        applyFilter();
      }
    });
    applyFilter();
  }

  /* ── Keyboard shortcut: "/" focuses the filter ──────────────────────────── */

  document.addEventListener("keydown", function (e) {
    if (e.key === "/" && document.activeElement !== filterEl) {
      e.preventDefault();
      if (filterEl) filterEl.focus();
    }
  });

  /* ── Table sort ─────────────────────────────────────────────────────────── */

  document.querySelectorAll(".data-table th[data-col]").forEach(function (th) {
    var asc = true;
    th.addEventListener("click", function () {
      var table = th.closest("table");
      var tbody = table ? table.querySelector("tbody") : null;
      if (!tbody) return;

      // Clear other headers
      table.querySelectorAll("th").forEach(function (h) {
        h.classList.remove("sort-asc", "sort-desc");
      });
      th.classList.add(asc ? "sort-asc" : "sort-desc");

      var col = th.dataset.col;
      var rows = Array.from(tbody.querySelectorAll("tr"));

      rows.sort(function (a, b) {
        var av = cellValue(a, col);
        var bv = cellValue(b, col);
        var an = parseFloat(av);
        var bn = parseFloat(bv);
        if (!isNaN(an) && !isNaN(bn)) {
          return asc ? an - bn : bn - an;
        }
        return asc ? av.localeCompare(bv) : bv.localeCompare(av);
      });

      rows.forEach(function (row) {
        tbody.appendChild(row);
      });
      asc = !asc;
    });
  });

  function cellValue(row, col) {
    // Prefer data attribute (numeric sort key) over text content.
    if (row.dataset[col] !== undefined) return row.dataset[col];
    // Fall back: find td by column index matching th position.
    var table = row.closest("table");
    if (!table) return "";
    var headers = Array.from(table.querySelectorAll("th"));
    var idx = headers.findIndex(function (h) {
      return h.dataset.col === col;
    });
    if (idx < 0) return "";
    var td = row.querySelectorAll("td")[idx];
    return td ? td.textContent.trim() : "";
  }

  /* ── Toast notification helper (also used by inline cache scripts) ──────── */

  window.showToast = function (msg, type) {
    var t = document.getElementById("toast");
    if (!t) return;
    t.textContent = msg;
    t.className = "toast " + (type || "ok") + " show";
    setTimeout(function () {
      t.className = "toast";
    }, 3000);
  };

  /* ── Progress bar widths (cache page) ──────────────────────────────────── */

  document.querySelectorAll(".progress-bar[data-pct]").forEach(function (el) {
    el.style.width = parseFloat(el.dataset.pct) + "%";
  });

  /* ── Cache: delete single key ───────────────────────────────────────────── */

  document.querySelectorAll(".btn-delete").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var key = btn.dataset.key;
      if (!confirm("Delete cache key:\n" + key + "?")) return;
      fetch("/cache/entry/" + encodeURIComponent(key), { method: "DELETE" })
        .then(function (r) {
          if (r.ok) {
            var row = btn.closest("tr");
            if (row) row.remove();
            window.showToast("Key deleted", "ok");
          } else {
            window.showToast("Delete failed", "err");
          }
        })
        .catch(function () {
          window.showToast("Request failed", "err");
        });
    });
  });

  /* ── Cache: flush all ───────────────────────────────────────────────────── */

  var flushBtn = document.getElementById("btn-flush");
  var flushModal = document.getElementById("flush-modal");
  if (flushBtn && flushModal) {
    flushBtn.addEventListener("click", function () {
      flushModal.style.display = "flex";
    });
    document.getElementById("flush-cancel").addEventListener("click", function () {
      flushModal.style.display = "none";
    });
    document.getElementById("flush-confirm").addEventListener("click", function () {
      flushModal.style.display = "none";
      fetch("/cache/flush", { method: "POST" })
        .then(function (r) {
          if (r.ok) {
            document.querySelectorAll("#cache-table tbody tr").forEach(function (row) {
              row.remove();
            });
            window.showToast("Cache flushed", "ok");
          } else {
            window.showToast("Flush failed", "err");
          }
        })
        .catch(function () {
          window.showToast("Request failed", "err");
        });
    });
  }
})();

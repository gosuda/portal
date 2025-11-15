function filterCards(q) {
  q = (q || "").toLowerCase().trim();
  const cards = document.querySelectorAll(".grid .card");
  cards.forEach((card) => {
    const name = (card.getAttribute("data-name") || "").toLowerCase();
    const show = !q || name.includes(q);
    card.style.display = show ? "" : "none";
  });
}

function setupSearch() {
  const input = document.getElementById("search");
  if (!input) return;
  input.addEventListener("input", (event) => {
    filterCards(event.target.value);
  });
}

(function () {
  setupSearch();
  const opts = { dateStyle: "medium", timeStyle: "short" };
  document.querySelectorAll("time.lastseen").forEach((el) => {
    const iso = el.getAttribute("datetime");
    if (!iso) return;
    const d = new Date(iso);
    if (isNaN(d.getTime())) return;
    el.textContent = new Intl.DateTimeFormat(undefined, opts).format(d);
    el.title = d.toString();
  });
})();

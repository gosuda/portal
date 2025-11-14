document.addEventListener("DOMContentLoaded", fetchAndRenderLeases);

async function fetchAndRenderLeases() {
  try {
    const res = await fetch("/api/leases");
    const data = await res.json();
    renderLeaseList(data);
  } catch (err) {
    console.error("Failed to fetch leases:", err);
  }
}

function renderLeaseList(leases) {
  const list = document.getElementById("lease-list");
  if (!list) return;
  list.innerHTML = "";
  leases.forEach((lease) => {
    const item = document.createElement("div");
    item.className = "lease-item";
    item.innerHTML = `
      <span class="lease-name">${lease.metadata?.description || lease.id}</span>
      <span id="gosuda-badge-${lease.id}" class="gosuda-badge">고수다</span>`;
    if (lease.metadata?.hide) {
      item.querySelector(`#gosuda-badge-${lease.id}`).style.display = "none";
    }
    list.appendChild(item);
  });
}

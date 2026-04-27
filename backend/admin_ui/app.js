"use strict";

const view = document.getElementById("view");
const meta = document.getElementById("meta");

function el(html) {
  const t = document.createElement("template");
  t.innerHTML = html.trim();
  return t.content.firstChild;
}

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[c]));
}

function fmtDate(s) {
  const d = new Date(s);
  return isNaN(d) ? s : d.toLocaleString();
}

async function api(path, opts = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { Accept: "application/json", ...(opts.headers || {}) },
    ...opts,
  });
  if (!res.ok) {
    throw new Error(`${res.status} ${res.statusText}`);
  }
  if (res.status === 204) return null;
  return res.json();
}

function showLoading() {
  view.innerHTML = `<div class="loading">Loading…</div>`;
}

function showError(msg) {
  view.innerHTML = `<div class="error">${escapeHTML(msg)}</div>`;
}

// ---- List view ----

async function renderList() {
  const params = new URLSearchParams(window.location.hash.split("?")[1] || "");
  const reason = params.get("reason") || "";
  const page = Math.max(1, parseInt(params.get("page") || "1", 10));
  const limit = Math.min(100, Math.max(1, parseInt(params.get("limit") || "20", 10)));

  showLoading();
  let data;
  try {
    const q = new URLSearchParams({ page, limit });
    if (reason) q.set("reason", reason);
    data = await api(`/admin/api/reports?${q.toString()}`);
  } catch (err) {
    showError(`Failed to load reports: ${err.message}`);
    return;
  }

  const tpl = document.getElementById("tpl-list").content.cloneNode(true);
  const root = tpl.querySelector(".list-view");
  const grid = root.querySelector("#grid");
  const filters = root.querySelector("#filters");
  const pagination = root.querySelector("#pagination");

  filters.reason.value = reason;
  filters.limit.value = String(limit);
  filters.addEventListener("submit", (e) => {
    e.preventDefault();
    const next = new URLSearchParams();
    if (filters.reason.value.trim()) next.set("reason", filters.reason.value.trim());
    next.set("limit", filters.limit.value);
    next.set("page", "1");
    window.location.hash = `#/?${next.toString()}`;
  });

  if (!data.items || data.items.length === 0) {
    grid.appendChild(el(`<div class="empty">No reports match.</div>`));
  } else {
    for (const it of data.items) {
      const card = el(`
        <div class="report-card" data-id="${it.id}">
          <div class="thumb"></div>
          <div class="body">
            <div class="reason">${escapeHTML(it.reason)}</div>
            <div class="meta-line">
              <span>#${it.id}</span>
              <span>${fmtDate(it.created_at)}</span>
            </div>
            <div class="meta-line">
              <span>Reports: ${it.reported_reports_count}</span>
              <span class="badge ${it.reported_banned_at ? "banned" : "ok"}">
                ${it.reported_banned_at ? "BANNED" : "active"}
              </span>
            </div>
          </div>
        </div>
      `);
      const thumb = card.querySelector(".thumb");
      if (it.thumbnail_url) {
        const img = document.createElement("img");
        img.src = it.thumbnail_url;
        img.alt = "screenshot";
        img.loading = "lazy";
        thumb.appendChild(img);
      }
      card.addEventListener("click", () => {
        window.location.hash = `#/r/${it.id}`;
      });
      grid.appendChild(card);
    }
  }

  // pagination
  const totalPages = Math.max(1, data.total_pages || 1);
  const prevBtn = el(`<button>&larr; Prev</button>`);
  const nextBtn = el(`<button>Next &rarr;</button>`);
  prevBtn.disabled = page <= 1;
  nextBtn.disabled = page >= totalPages;
  prevBtn.addEventListener("click", () => goPage(page - 1, reason, limit));
  nextBtn.addEventListener("click", () => goPage(page + 1, reason, limit));
  pagination.appendChild(prevBtn);
  pagination.appendChild(el(`<span class="page-info">Page ${page} / ${totalPages} — ${data.total} reports</span>`));
  pagination.appendChild(nextBtn);

  view.innerHTML = "";
  view.appendChild(tpl);
  meta.textContent = `${data.total} report${data.total === 1 ? "" : "s"}`;
}

function goPage(page, reason, limit) {
  const next = new URLSearchParams();
  if (reason) next.set("reason", reason);
  next.set("limit", String(limit));
  next.set("page", String(page));
  window.location.hash = `#/?${next.toString()}`;
}

// ---- Detail view ----

async function renderDetail(id) {
  showLoading();
  let data;
  try {
    data = await api(`/admin/api/reports/${encodeURIComponent(id)}`);
  } catch (err) {
    showError(`Failed to load report: ${err.message}`);
    return;
  }

  const tpl = document.getElementById("tpl-detail").content.cloneNode(true);
  const root = tpl.querySelector(".detail-view");
  const detail = root.querySelector("#detail");
  const r = data.report;
  const url = data.signed_screenshot_url || r.screenshot_url;

  detail.innerHTML = `
    <div class="screenshot">
      ${url ? `<img src="${escapeHTML(url)}" alt="screenshot" />` : `<div class="empty">no screenshot</div>`}
    </div>
    <div class="info">
      <h2 style="margin:0">Report #${r.id}</h2>
      <dl>
        <dt>Reason</dt><dd>${escapeHTML(r.reason)}</dd>
        <dt>Created</dt><dd>${fmtDate(r.created_at)}</dd>
        <dt>Reported user</dt><dd>${escapeHTML(r.reported_sub)} (id ${r.reported_id})</dd>
        <dt>Reporter</dt><dd>${escapeHTML(r.reporter_sub)} (id ${r.reporter_id})</dd>
        <dt>Total reports vs reported user</dt><dd>${r.reported_reports_count}</dd>
        <dt>Status</dt><dd>${
          r.reported_banned_at
            ? `<span class="badge banned">BANNED ${fmtDate(r.reported_banned_at)}</span>`
            : `<span class="badge ok">active</span>`
        }</dd>
        <dt>Screenshot URL</dt><dd>${escapeHTML(r.screenshot_url || "—")}</dd>
      </dl>
      <div class="actions">
        <button class="ban" ${r.reported_banned_at ? "disabled" : ""}>Ban user</button>
        <button class="unban" ${r.reported_banned_at ? "" : "disabled"}>Unban user</button>
      </div>
    </div>
  `;

  detail.querySelector(".ban").addEventListener("click", () => userAction(r.reported_id, "ban", id));
  detail.querySelector(".unban").addEventListener("click", () => userAction(r.reported_id, "unban", id));

  view.innerHTML = "";
  view.appendChild(tpl);
  meta.textContent = `Report #${r.id}`;
}

async function userAction(userID, action, reportID) {
  if (!confirm(`Confirm ${action} for user ${userID}?`)) return;
  try {
    await api(`/admin/api/users/${userID}/${action}`, { method: "POST" });
  } catch (err) {
    alert(`${action} failed: ${err.message}`);
    return;
  }
  // Refresh
  renderDetail(reportID);
}

// ---- Router ----

function route() {
  const hash = window.location.hash || "#/";
  const [path] = hash.split("?");
  if (path === "#/" || path === "#" || path === "") {
    renderList();
    return;
  }
  const m = path.match(/^#\/r\/(\d+)$/);
  if (m) {
    renderDetail(m[1]);
    return;
  }
  renderList();
}

window.addEventListener("hashchange", route);
window.addEventListener("DOMContentLoaded", route);

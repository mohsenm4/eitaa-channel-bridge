// Loads posts.json (written by the bridge's HTML publisher) and
// renders it as a filterable card grid.

const DATA_URL = "posts.json";

const state = {
  site: null,
  messages: [],
  filter: "__all__",
};

document.addEventListener("DOMContentLoaded", init);

async function init() {
  try {
    const res = await fetch(DATA_URL, { cache: "no-store" });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const payload = await res.json();
    state.site = payload.site || {};
    state.messages = (payload.posts || []).slice().sort((a, b) => b.id - a.id);
  } catch (err) {
    showError(
      `نتوانستم <code>${DATA_URL}</code> را بخوانم: ${err.message}. ` +
        `این فایل وقتی بریج پست منتشر کنه تولید می‌شه. ` +
        `با یک سرور ساده باز کن — مثلاً: <code>python3 -m http.server 8000</code>.`,
    );
    return;
  }

  applySiteMeta();
  buildFilters();
  render();
}

function applySiteMeta() {
  const title = state.site.title || document.title;
  for (const el of document.querySelectorAll("[data-site-title]")) el.textContent = title;
  const sub = document.getElementById("site-subtitle");
  if (state.site.subtitle) sub.textContent = state.site.subtitle;
}

function buildFilters() {
  const counts = new Map();
  counts.set("__all__", state.messages.length);
  for (const m of state.messages) {
    const key = m.category_fa || "بدون دسته";
    counts.set(key, (counts.get(key) || 0) + 1);
  }

  const filters = document.getElementById("filters");
  filters.hidden = false;
  filters.innerHTML = "";

  appendFilter(filters, "__all__", "همه", counts.get("__all__"));
  for (const [label, count] of counts) {
    if (label === "__all__") continue;
    appendFilter(filters, label, label, count);
  }

  const updated = state.site.generated_at
    ? new Date(state.site.generated_at).toLocaleString("fa-IR")
    : "—";
  document.getElementById("meta").textContent =
    `${state.messages.length} پست منتشر شده · به‌روزرسانی: ${updated}`;
}

function appendFilter(parent, key, label, count) {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.dataset.key = key;
  if (key === state.filter) btn.classList.add("active");
  btn.innerHTML = `${label}<span class="count">${count}</span>`;
  btn.addEventListener("click", () => {
    state.filter = key;
    for (const b of parent.querySelectorAll("button")) b.classList.remove("active");
    btn.classList.add("active");
    render();
  });
  parent.appendChild(btn);
}

function render() {
  const grid = document.getElementById("grid");
  grid.innerHTML = "";

  const visible = state.messages.filter((m) => {
    if (state.filter === "__all__") return true;
    return (m.category_fa || "بدون دسته") === state.filter;
  });

  if (visible.length === 0) {
    const p = document.createElement("p");
    p.className = "empty";
    p.textContent = "هیچ پستی در این دسته نیست.";
    grid.appendChild(p);
    return;
  }

  for (const m of visible) grid.appendChild(buildCard(m));
}

function buildCard(m) {
  const tpl = document.getElementById("card-template");
  const node = tpl.content.firstElementChild.cloneNode(true);

  node.querySelector(".badge").textContent = m.category_fa || "بدون دسته";

  const permalink = node.querySelector(".permalink");
  permalink.href = m.link || "#";

  node.querySelector(".card-title").textContent = m.title || `پیام #${m.id}`;

  const subtitle = node.querySelector(".card-subtitle");
  if (m.subtitle) subtitle.textContent = m.subtitle;
  else subtitle.remove();

  const eventDate = node.querySelector(".card-event-date");
  if (m.event_date) eventDate.textContent = m.event_date;
  else eventDate.remove();

  const photoWrap = node.querySelector(".card-photos");
  const photos = m.photos || [];
  if (photos.length === 0) {
    photoWrap.remove();
  } else {
    photoWrap.classList.add(`count-${Math.min(photos.length, 4)}`);
    for (const url of photos.slice(0, 6)) {
      const img = document.createElement("img");
      img.loading = "lazy";
      img.src = url;
      img.alt = "";
      img.onerror = () => img.remove();
      photoWrap.appendChild(img);
    }
  }

  const textEl = node.querySelector(".card-text");
  const cleanText = (m.text || "").trim();
  if (cleanText) {
    textEl.textContent = cleanText;
    if (cleanText.split("\n").length > 6 || cleanText.length > 320) {
      const toggle = document.createElement("button");
      toggle.type = "button";
      toggle.className = "card-text-toggle";
      toggle.textContent = "نمایش کامل";
      toggle.addEventListener("click", () => {
        const expanded = textEl.classList.toggle("expanded");
        toggle.textContent = expanded ? "بستن" : "نمایش کامل";
      });
      textEl.after(toggle);
    }
  } else {
    textEl.remove();
  }

  node.querySelector(".card-date").textContent = formatDate(m.date);

  const tags = node.querySelector(".card-hashtags");
  for (const tag of (m.hashtags || []).slice(0, 4)) {
    const span = document.createElement("span");
    span.className = "tag";
    span.textContent = `#${tag}`;
    tags.appendChild(span);
  }
  if (!tags.childElementCount) tags.remove();

  return node;
}

function formatDate(iso) {
  if (!iso) return "";
  try {
    return new Intl.DateTimeFormat("fa-IR", {
      year: "numeric",
      month: "long",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    }).format(new Date(iso));
  } catch {
    return iso;
  }
}

function showError(html) {
  const grid = document.getElementById("grid");
  grid.innerHTML = `<div class="error">${html}</div>`;
  document.getElementById("meta").textContent = "خطا";
}

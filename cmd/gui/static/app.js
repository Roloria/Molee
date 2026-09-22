/* Molee dashboard — vanilla JS over the local read-only API. */
"use strict";

/* ---------- tiny helpers ---------- */

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

function fmtBytes(bytes) {
  if (bytes === null || bytes === undefined || isNaN(bytes)) return "—";
  if (bytes >= 1e12) return (bytes / 1e12).toFixed(2) + " TB";
  if (bytes >= 1e9) return (bytes / 1e9).toFixed(2) + " GB";
  if (bytes >= 1e6) return (bytes / 1e6).toFixed(1) + " MB";
  if (bytes >= 1e3) return Math.round(bytes / 1e3) + " KB";
  return bytes + " B";
}

function parseHumanSize(str) {
  if (typeof str !== "string") return 0;
  const m = str.trim().match(/^([\d.]+)\s*(B|KB|MB|GB|TB)$/i);
  if (!m) return 0;
  const mult = { b: 1, kb: 1e3, mb: 1e6, gb: 1e9, tb: 1e12 }[m[2].toLowerCase()];
  return Math.round(parseFloat(m[1]) * mult);
}

function el(tag, attrs, ...children) {
  const node = document.createElement(tag);
  if (attrs) for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const child of children) {
    if (child === null || child === undefined) continue;
    node.append(child);
  }
  return node;
}

function truncateMiddle(s, max = 64) {
  if (!s || s.length <= max) return s || "";
  const half = Math.floor((max - 1) / 2);
  return s.slice(0, half) + "…" + s.slice(s.length - half);
}

async function fetchJSON(url, options) {
  options = options || {};
  // Bearer auth is primary: some browsers do not apply a Set-Cookie from the
  // very navigation that loaded the page, so cookies alone would race.
  if (TOKEN) options.headers = Object.assign({ Authorization: "Bearer " + TOKEN }, options.headers || {});
  const res = await fetch(url, options);
  if (res.status === 401) {
    clearToken();
    showAuth();
    throw new Error("unauthorized");
  }
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || res.statusText);
  return body;
}

/* ---------- auth ---------- */

let TOKEN = null;

function initToken() {
  const fromURL = new URLSearchParams(location.search).get("token");
  if (fromURL) {
    TOKEN = fromURL;
    localStorage.setItem("molee_token", fromURL);
    history.replaceState(null, "", location.hash || "/");
  } else {
    TOKEN = localStorage.getItem("molee_token");
  }
}

function clearToken() {
  TOKEN = null;
  localStorage.removeItem("molee_token");
}

function showAuth() {
  $("#auth-overlay").hidden = false;
}

$("#auth-submit").addEventListener("click", () => {
  const token = $("#auth-token").value.trim();
  if (!token) return;
  localStorage.setItem("molee_token", token);
  window.location.href = "/";
});
$("#auth-token").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("#auth-submit").click();
});

/* ---------- chart factory ---------- */

const AXIS_STYLE = {
  axisLine: { show: false },
  axisTick: { show: false },
  axisLabel: { show: false },
  splitLine: { show: false },
};

function makeChart(elId) {
  const chart = echarts.init($(elId), null, { renderer: "canvas" });
  charts.push(chart);
  return chart;
}
const charts = [];
window.addEventListener("resize", () => charts.forEach((c) => c.resize()));

function sparkOption(color, areaOpacity = 0.18) {
  return {
    animation: false,
    grid: { left: 2, right: 2, top: 4, bottom: 2 },
    xAxis: { type: "category", show: false, boundaryGap: false },
    yAxis: { type: "value", show: false, min: 0, max: 100 },
    series: [{
      type: "line", data: [], symbol: "none", smooth: true,
      lineStyle: { color, width: 1.6 },
      areaStyle: { color, opacity: areaOpacity },
    }],
  };
}

/* ---------- routing ---------- */

const views = ["dashboard", "disk", "clean", "uninstall", "history"];
const viewInit = {};
let currentView = "dashboard";

$$(".nav-item").forEach((btn) => {
  btn.addEventListener("click", () => {
    switchView(btn.dataset.view);
    history.replaceState(null, "", "#" + btn.dataset.view);
  });
});

window.addEventListener("hashchange", () => {
  const v = location.hash.slice(1);
  if (views.includes(v)) switchView(v);
});

function switchView(name) {
  currentView = name;
  $$(".nav-item").forEach((b) => b.classList.toggle("active", b.dataset.view === name));
  $$(".view").forEach((v) => v.classList.toggle("active", v.id === "view-" + name));
  if (!viewInit[name]) {
    viewInit[name] = true;
    VIEW_SETUP[name]();
  }
  charts.forEach((c) => c.resize());
}

/* ---------- meta / bootstrap ---------- */

let META = null;

(async function boot() {
  initToken();
  try {
    META = await fetchJSON("/api/meta");
    $("#meta-version").textContent = "v" + META.version;
    $("#dash-subtitle").textContent = "Live system health · " + (META.interval || "2s") + " refresh";
  } catch (e) {
    if (e.message !== "unauthorized") console.error(e);
    return; // auth overlay is already up on 401
  }
  VIEW_SETUP.dashboard();
  viewInit.dashboard = true;
  // Deep links: /#disk, /#clean, …
  const fromHash = location.hash.slice(1);
  if (views.includes(fromHash) && fromHash !== "dashboard") switchView(fromHash);
})();

const VIEW_SETUP = {
  dashboard: setupDashboard,
  disk: setupDisk,
  clean: setupClean,
  uninstall: setupUninstall,
  history: setupHistory,
};

/* =========================================================
   Dashboard
   ========================================================= */

const dash = {
  es: null,
  cpuBuf: [],
  memBuf: [],
  gauge: null, cpu: null, mem: null, net: null,
  lastNet: { rx: [], tx: [] },
};

function setupDashboard() {
  dash.gauge = makeChart("#chart-health");
  dash.cpu = makeChart("#chart-cpu");
  dash.mem = makeChart("#chart-mem");
  dash.net = makeChart("#chart-net");
  dash.cpu.setOption(sparkOption("#5b9dff"));
  dash.mem.setOption(sparkOption("#a78bfa"));
  dash.net.setOption({
    animation: false,
    grid: { left: 2, right: 2, top: 4, bottom: 2 },
    xAxis: { type: "category", show: false, boundaryGap: false },
    yAxis: { type: "value", show: false },
    series: [
      { type: "line", data: [], symbol: "none", smooth: true, lineStyle: { color: "#34d399", width: 1.6 }, areaStyle: { color: "#34d399", opacity: 0.15 } },
      { type: "line", data: [], symbol: "none", smooth: true, lineStyle: { color: "#fbbf24", width: 1.4 }, areaStyle: { color: "#fbbf24", opacity: 0.12 } },
    ],
  });
  dash.gauge.setOption(healthGaugeOption(0));

  connectStream();
}

function healthGaugeOption(score) {
  return {
    series: [{
      type: "gauge",
      startAngle: 210, endAngle: -30,
      min: 0, max: 100,
      radius: "98%", center: ["50%", "58%"],
      progress: { show: true, width: 12, roundCap: true },
      axisLine: { lineStyle: { width: 12, color: [[1, "#1e2433"]] } },
      axisTick: { show: false },
      splitLine: { show: false },
      axisLabel: { show: false },
      pointer: { show: false },
      anchor: { show: false },
      detail: {
        valueAnimation: true, offsetCenter: [0, "4%"],
        fontSize: 34, fontWeight: 700, color: "#e7eaf1", formatter: "{value}",
      },
      data: [{ value: score }],
    }],
  };
}

function connectStream() {
  if (dash.es) dash.es.close();
  setConn("connecting…", "");
  // Query-token fallback: EventSource cannot send an Authorization header.
  const url = TOKEN ? "/api/status/stream?token=" + encodeURIComponent(TOKEN) : "/api/status/stream";
  const es = new EventSource(url);
  dash.es = es;
  es.onopen = () => setConn("live", "live");
  es.onmessage = (ev) => {
    let snap;
    try { snap = JSON.parse(ev.data); } catch { return; }
    setConn("live", "live");
    renderSnapshot(snap);
  };
  es.addEventListener("end", () => { setConn("reconnecting…", "down"); es.close(); setTimeout(connectStream, 3000); });
  es.addEventListener("collector-error", (ev) => {
    setConn("collector error", "down");
    $("#dash-subtitle").textContent = "status collector failed — see server log";
    try { const d = JSON.parse(ev.data); console.error(d); } catch {}
  });
  es.onerror = () => setConn("reconnecting…", "down");
}

function setConn(text, cls) {
  $("#conn-text").textContent = text;
  $("#conn-pill").className = "conn-pill " + cls;
}

function renderSnapshot(s) {
  /* health gauge */
  dash.gauge.setOption({ series: [{ data: [{ value: s.health_score }] }] });
  $("#health-msg").textContent = s.health_score_msg || "";

  /* host */
  const hw = s.hardware || {};
  $("#hw-model").textContent = hw.model || "—";
  $("#hw-cpu").textContent = hw.cpu_model || "—";
  $("#hw-ram").textContent = hw.total_ram || "—";
  $("#hw-os").textContent = hw.os_version || "—";
  $("#sys-uptime").textContent = s.uptime || "—";
  $("#sys-procs").textContent = s.procs ? s.procs + " running" : "—";

  /* chips: battery, thermal, zombie, proxy, trash */
  const chips = [];
  const bat = (s.batteries && s.batteries[0]) || null;
  if (bat) {
    const cls = bat.percent <= 20 && bat.status !== "Charged" && !/charg/i.test(bat.status || "") ? "bad" : "good";
    chips.push(el("span", { class: "chip " + cls }, "🔋 ", `${Math.round(bat.percent)}% · ${bat.status || "?"}`, bat.cycle_count ? ` · ${bat.cycle_count} cycles` : ""));
  }
  const th = s.thermal || {};
  if (th.cpu_temp > 0) {
    const cls = th.cpu_temp >= 85 ? "bad" : th.cpu_temp >= 70 ? "warn" : "good";
    chips.push(el("span", { class: "chip " + cls }, "🌡 ", `${Math.round(th.cpu_temp)}°C`, th.fan_speed ? ` · ${Math.round(th.fan_speed)} rpm` : ""));
  }
  if (typeof s.zombie_count === "number" && s.zombie_count > 0) {
    chips.push(el("span", { class: "chip warn" }, "🧟 ", s.zombie_count + " zombies"));
  }
  if (s.proxy && s.proxy.enabled) {
    chips.push(el("span", { class: "chip" }, "🛰 ", (s.proxy.type || "proxy") + (s.proxy.host ? " · " + s.proxy.host : "")));
  }
  if (s.trash_size > 0) {
    chips.push(el("span", { class: "chip" }, "🗑 ", fmtBytes(s.trash_size) + " in Trash"));
  }
  const memPressure = s.memory && s.memory.pressure;
  if (memPressure && memPressure !== "Normal") {
    chips.push(el("span", { class: "chip warn" }, "⚠ ", "memory pressure: " + memPressure));
  }
  const chipRow = $("#dash-chips");
  chipRow.replaceChildren(...chips);

  /* cpu */
  const cpuPct = s.cpu ? s.cpu.usage : 0;
  $("#stat-cpu").textContent = cpuPct.toFixed(1) + "%";
  pushBuf(dash.cpuBuf, cpuPct);
  dash.cpu.setOption({ series: [{ data: dash.cpuBuf.slice() }] });
  if (s.cpu) {
    $("#stat-cpu-load").textContent = `load ${s.cpu.load1?.toFixed(2)} / ${s.cpu.load5?.toFixed(2)} / ${s.cpu.load15?.toFixed(2)} · ${s.cpu.logical_cpu || s.cpu.core_count || "?"} cores`;
  }

  /* memory */
  const mem = s.memory || {};
  const memPct = mem.used_percent || 0;
  $("#stat-mem").textContent = memPct.toFixed(1) + "%";
  pushBuf(dash.memBuf, memPct);
  dash.mem.setOption({ series: [{ data: dash.memBuf.slice() }] });
  $("#stat-mem-detail").textContent = mem.total
    ? `${fmtBytes(mem.used)} / ${fmtBytes(mem.total)} · avail ${fmtBytes(mem.available)}`
    : "—";

  /* disk */
  const disk = (s.disks || []).find((d) => !d.external) || (s.disks || [])[0];
  if (disk) {
    const pct = disk.used_percent || 0;
    $("#stat-disk").textContent = pct.toFixed(1) + "%";
    const fill = $("#disk-bar-fill");
    fill.style.width = Math.min(100, pct) + "%";
    fill.className = "disk-bar-fill " + (pct >= 90 ? "bad" : pct >= 75 ? "warn" : "");
    $("#stat-disk-detail").textContent = `${fmtBytes(disk.used)} / ${fmtBytes(disk.total)} used${disk.smart_status ? " · SMART " + disk.smart_status : ""}`;
  }

  /* network */
  const nets = s.network || [];
  const rx = nets.reduce((a, n) => a + (n.rx_rate_mbs || 0), 0);
  const tx = nets.reduce((a, n) => a + (n.tx_rate_mbs || 0), 0);
  const fmtRate = (v) => v >= 1 ? v.toFixed(2) + " MB/s" : (v * 1000).toFixed(0) + " KB/s";
  $("#stat-net").textContent = "↓ " + fmtRate(rx);
  $("#stat-net-detail").textContent = "↑ " + fmtRate(tx) + (nets[0] && nets[0].ip ? " · " + nets[0].ip : "");
  const hist = s.network_history || {};
  const scale = (arr) => (arr || []).map((v) => {
    const max = Math.max(1, ...(hist.rx_history || [1]), ...(hist.tx_history || [1]));
    return Math.min(100, (v / max) * 100);
  });
  dash.net.setOption({
    series: [
      { data: scale(hist.rx_history) },
      { data: scale(hist.tx_history) },
    ],
  });

  /* processes */
  const tbody = $("#proc-table tbody");
  const procs = (s.top_processes || []).slice(0, 8);
  tbody.replaceChildren(...procs.map((p) => el("tr", null,
    el("td", { class: "cell-path", text: p.name || p.command || "—" }),
    el("td", { class: "num", text: String(p.pid ?? "") }),
    el("td", { class: "num", text: (p.cpu || 0).toFixed(1) + "%" }),
    el("td", { class: "num", text: p.memory_bytes ? fmtBytes(p.memory_bytes) : (p.memory || 0).toFixed(1) + "%" }),
  )));
}

function pushBuf(buf, value) {
  buf.push(value);
  if (buf.length > 120) buf.shift();
}

/* =========================================================
   Disk
   ========================================================= */

const diskState = { chart: null, path: "", scanning: false };

function setupDisk() {
  diskState.chart = makeChart("#chart-treemap");
  $("#disk-scan").addEventListener("click", () => scanDisk($("#disk-path").value.trim()));
  $("#disk-path").addEventListener("keydown", (e) => {
    if (e.key === "Enter") scanDisk($("#disk-path").value.trim());
  });
  diskState.chart.on("click", (params) => {
    const d = params.data || {};
    if (d.path && d.is_dir) scanDisk(d.path);
  });
  scanDisk("");
}

async function scanDisk(path) {
  if (diskState.scanning) return;
  diskState.scanning = true;
  $("#disk-scan").disabled = true;
  $("#disk-status").textContent = "Scanning… (large trees take a moment)";
  $("#disk-status").className = "hint";
  $("#disk-path").value = path;

  try {
    const url = path ? "/api/analyze?path=" + encodeURIComponent(path) : "/api/analyze";
    const data = await fetchJSON(url);
    renderDisk(data);
    $("#disk-status").textContent = "";
  } catch (e) {
    if (e.message !== "unauthorized") {
      $("#disk-status").textContent = "Scan failed: " + e.message;
      $("#disk-status").className = "hint error";
    }
  } finally {
    diskState.scanning = false;
    $("#disk-scan").disabled = false;
  }
}

function renderDisk(data) {
  diskState.path = data.path || "";
  $("#disk-title").textContent = data.overview ? "Machine overview" : (data.path || "");
  $("#disk-path").value = data.overview ? "" : (data.path || "");

  const chips = [];
  chips.push(el("span", { class: "chip" }, "Σ ", fmtBytes(data.total_size)));
  if (data.total_files) chips.push(el("span", { class: "chip" }, " {} ", data.total_files.toLocaleString() + " files"));
  const cleanables = (data.entries || []).filter((e) => e.cleanable);
  if (cleanables.length) {
    chips.push(el("span", { class: "chip good" }, "✦ ", cleanables.length + " cleanable here"));
  }
  $("#disk-chips").replaceChildren(...chips);

  const entries = (data.entries || []).slice();
  entries.sort((a, b) => b.size - a.size);
  diskState.chart.setOption({
    animation: true,
    tooltip: {
      formatter: (p) => `${p.data.path || p.name}<br>${fmtBytes(p.value)} (${p.value && data.total_size ? ((p.value / data.total_size) * 100).toFixed(1) + "%" : ""})`,
    },
    series: [{
      type: "treemap",
      data: entries.map((e) => ({
        name: e.name,
        value: e.size,
        path: e.path,
        is_dir: e.is_dir,
        itemStyle: e.cleanable
          ? { color: "rgba(52, 211, 153, .55)" }
          : e.insight
            ? { color: "rgba(251, 191, 36, .5)" }
            : undefined,
      })),
      roam: false,
      nodeClick: false,
      breadcrumb: { show: true, bottom: 0, itemStyle: { borderColor: "#1e2433", textStyle: { color: "#8b93a7" } } },
      label: { show: true, formatter: "{b}\n{c}" },
      upperLabel: { show: true, height: 22, color: "#e7eaf1" },
      itemStyle: { borderColor: "#0a0d13", borderWidth: 2, gapWidth: 2 },
      levels: [{ color: ["#2f5fd0", "#3b74e8", "#4c88f5", "#5b9dff", "#7fb0ff"] }],
    }],
  }, true);
  // human-readable labels after initial set
  diskState.chart.setOption({
    series: [{
      label: { formatter: (p) => `${p.name}\n${fmtBytes(p.value)}` },
      upperLabel: {},
    }],
  });

  /* large files */
  const lfTbody = $("#large-table tbody");
  const lf = (data.large_files || []).slice(0, 12);
  lfTbody.replaceChildren(...lf.map((f) => el("tr", null,
    el("td", { class: "cell-path mono", title: f.path, text: truncateMiddle(f.path, 58) }),
    el("td", { class: "num", text: fmtBytes(f.size) }),
  )));
  if (!lf.length) lfTbody.replaceChildren(el("tr", null, el("td", { colspan: "2", class: "hint", text: "No large files found here." })));

  /* cleanable */
  const clTbody = $("#cleanable-table tbody");
  const cl = cleanables.slice(0, 12);
  clTbody.replaceChildren(...cl.map((c) => el("tr", null,
    el("td", { class: "cell-path mono", title: c.path, text: truncateMiddle(c.path, 58) }),
    el("td", { class: "num", text: fmtBytes(c.size) }),
  )));
  if (!cl.length) clTbody.replaceChildren(el("tr", null, el("td", { colspan: "2", class: "hint", text: "No known-safe cleanable directories here." })));
}

/* =========================================================
   Clean
   ========================================================= */

let cleanScanning = false;

function setupClean() {
  $("#clean-scan").addEventListener("click", runCleanPreview);
}

async function runCleanPreview() {
  if (cleanScanning) return;
  cleanScanning = true;
  const btn = $("#clean-scan");
  btn.disabled = true;
  const startedAt = Date.now();
  const timer = setInterval(() => {
    $("#clean-status").innerHTML = `<span class="spin"></span>Scanning — ${Math.round((Date.now() - startedAt) / 1000)}s elapsed`;
  }, 500);

  try {
    const preview = await fetchJSON("/api/clean/preview", { method: "POST" });
    renderClean(preview);
    $("#clean-status").textContent = "";
  } catch (e) {
    if (e.message !== "unauthorized") {
      $("#clean-status").textContent = "Preview failed: " + e.message;
      $("#clean-status").className = "hint error";
    }
  } finally {
    clearInterval(timer);
    btn.disabled = false;
    cleanScanning = false;
  }
}

function renderClean(p) {
  const summary = $("#clean-summary");
  summary.hidden = false;
  $("#clean-total").textContent = p.total_known ? fmtBytes(p.total_bytes) + (p.partial ? "+" : "") : "partial";
  $("#clean-rows").textContent = p.rows.toLocaleString();
  $("#clean-items").textContent = p.items.toLocaleString();
  $("#clean-generated").textContent = p.generated_at ? "scanned " + p.generated_at : "";

  const wrap = $("#clean-sections");
  wrap.replaceChildren(...(p.sections || []).map((section) => {
    const card = el("div", { class: "section-card open" });
    const head = el("button", {
      class: "section-head",
      onclick: () => card.classList.toggle("open"),
    },
      el("span", { class: "caret", text: "▶" }),
      el("span", { text: section.name }),
      el("span", { class: "section-count", text: section.rows + " items" }),
      el("span", { class: "section-total", text: fmtBytes(section.total_bytes) + (section.total_bytes ? "" : "") }),
    );
    const body = el("div", { class: "section-body" });
    const table = el("table", { class: "table" },
      el("thead", null, el("tr", null, el("th", { text: "Path" }), el("th", { class: "num", text: "Size" }))),
    );
    const tbody = el("tbody");
    tbody.replaceChildren(...section.items.map((item) => el("tr", null,
      el("td", { class: "cell-path mono", title: item.path },
        document.createTextNode(truncateMiddle(item.path, 70)),
        item.covered_by ? el("span", { class: "hint", text: "  · counted under " + truncateMiddle(item.coveredBy, 40) }) : null,
      ),
      el("td", { class: "num", text: item.size_known ? item.size_display + (item.items > 1 ? ` · ${item.items}` : "") : "unknown" }),
    )));
    table.append(tbody);
    body.append(table);
    card.append(head, body);
    return card;
  }));

  if (!(p.sections || []).length) {
    wrap.replaceChildren(el("div", { class: "card hint", text: "Nothing found — or the scan was interrupted. Try again." }));
  }
}

/* =========================================================
   Uninstall
   ========================================================= */

const uninstallState = { apps: [], sortDesc: true };

function setupUninstall() {
  $("#uninstall-search").addEventListener("input", renderUninstall);
  $("th[data-sort='size']").addEventListener("click", () => {
    uninstallState.sortDesc = !uninstallState.sortDesc;
    renderUninstall();
  });
  loadUninstall();
}

async function loadUninstall() {
  const status = $("#uninstall-summary");
  status.textContent = "Loading installed apps…";
  try {
    uninstallState.apps = await fetchJSON("/api/uninstall/list");
    renderUninstall();
  } catch (e) {
    if (e.message !== "unauthorized") {
      status.textContent = "Failed: " + e.message;
      status.className = "hint error";
    }
  }
}

function renderUninstall() {
  const q = ($("#uninstall-search").value || "").toLowerCase();
  const apps = uninstallState.apps
    .filter((a) => !q || (a.name || "").toLowerCase().includes(q) || (a.bundle_id || "").toLowerCase().includes(q))
    .sort((a, b) => (parseHumanSize(b.size) - parseHumanSize(a.size)) * (uninstallState.sortDesc ? 1 : -1));

  const totalBytes = uninstallState.apps.reduce((acc, a) => acc + parseHumanSize(a.size), 0);
  const summary = $("#uninstall-summary");
  summary.className = "hint";
  summary.textContent = `${uninstallState.apps.length} apps · ${fmtBytes(totalBytes)} on disk`;

  const tbody = $("#uninstall-table tbody");
  tbody.replaceChildren(...apps.map((a) => el("tr", null,
    el("td", { class: "cell-path", title: a.path, text: a.name || a.uninstall_name || "—" }),
    el("td", null, el("span", { class: "badge " + (a.source === "Homebrew" ? "brew" : ""), text: a.source || "App" })),
    el("td", { class: "mono", text: truncateMiddle(a.bundle_id || "—", 42) }),
    el("td", { class: "num", text: a.size || "—" }),
  )));
  if (!apps.length) {
    tbody.replaceChildren(el("tr", null, el("td", { colspan: "4", class: "hint", text: q ? "No matches." : "No apps found." })));
  }
}

/* =========================================================
   History
   ========================================================= */

function setupHistory() {
  $("#history-refresh").addEventListener("click", loadHistory);
  loadHistory();
}

async function loadHistory() {
  try {
    const data = await fetchJSON("/api/history");
    renderHistory(data);
  } catch (e) {
    if (e.message !== "unauthorized") console.error(e);
  }
}

function renderHistory(data) {
  const sessions = (data.sessions || []).slice().reverse(); // oldest → newest for the chart
  const chart = $("#chart-history") && makeChart("#chart-history");
  if (!chart) return;
  const labeled = sessions.slice(-20);
  chart.setOption({
    animation: true,
    grid: { left: 46, right: 16, top: 18, bottom: 46 },
    tooltip: { trigger: "axis" },
    xAxis: {
      type: "category",
      data: labeled.map((s) => (s.started_at || "").slice(5, 16).replace("T", " ")),
      axisLine: { lineStyle: { color: "#1e2433" } },
      axisLabel: { color: "#8b93a7", fontSize: 11 },
    },
    yAxis: {
      type: "value",
      axisLabel: {
        color: "#8b93a7", fontSize: 11,
        formatter: (v) => fmtBytes(v),
      },
      splitLine: { lineStyle: { color: "#1e2433" } },
    },
    series: [{
      type: "bar",
      data: labeled.map((s) => parseHumanSize(s.size)),
      barMaxWidth: 26,
      itemStyle: { color: "#5b9dff", borderRadius: [5, 5, 0, 0] },
    }],
  }, true);
  chart.setOption({ series: [{ tooltip: { valueFormatter: (v) => fmtBytes(v) } }] });

  /* sessions list — newest first */
  const list = $("#history-sessions");
  const newest = (data.sessions || []).slice(0, 15);
  list.replaceChildren(...newest.map((s) => {
    const acts = s.actions || {};
    const chips = [];
    const ac = (n, label, cls) => n ? el("span", { class: "ac " + (cls || ""), text: `${n} ${label}` }) : null;
    chips.push(ac(acts.removed, "removed", "removed"));
    chips.push(ac(acts.trashed, "trashed", "trashed"));
    chips.push(ac(acts.skipped, "skipped"));
    chips.push(ac(acts.failed, "failed", "failed"));
    chips.push(ac(acts.rebuilt, "rebuilt"));
    return el("div", { class: "session-item" },
      el("div", { class: "session-top" },
        el("span", { class: "session-cmd", text: s.command || "clean" }),
        el("span", { class: "session-size", text: s.size || "0B" }),
      ),
      el("div", { class: "session-meta", text: `${(s.started_at || "").slice(0, 19).replace("T", " ")} → ${(s.ended_at || "").slice(11, 19)} · ${s.items ?? 0} items · ${s.operation_count ?? 0} operations` }),
      el("div", { class: "action-chips" }, ...chips.filter(Boolean)),
    );
  }));
  if (!newest.length) list.replaceChildren(el("p", { class: "hint", text: "No cleanup sessions recorded yet." }));

  /* deletions */
  const tbody = $("#deletions-table tbody");
  const deletions = (data.deletions || []).slice(-15).reverse();
  tbody.replaceChildren(...deletions.map((d) => el("tr", null,
    el("td", { class: "hint", text: (d.timestamp || "").slice(5, 16) }),
    el("td", { class: "cell-path mono", title: d.path, text: truncateMiddle(d.path || "", 52) }),
    el("td", { class: "num", text: d.size_kb != null ? fmtBytes(d.size_kb * 1024) : "—" }),
  )));
  if (!deletions.length) tbody.replaceChildren(el("tr", null, el("td", { colspan: "3", class: "hint", text: "No deletions recorded yet." })));
}

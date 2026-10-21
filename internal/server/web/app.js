// LinkScout web UI – vanilla JavaScript, no build step, no dependencies.
//
// The Go server injects window.LINKSCOUT = {lang, messages, version, defaults,
// firefoxUA, state} into index.html. Everything the page shows is derived from
// `state` ({progress, results, stats, mirrors, config}), which is refreshed
// from GET /api/state while a check or an offline copy runs.
//
// Structure (top to bottom):
//   i18n / helpers     translation with %name% placeholders, formatting, API
//   tabs               "Check links" (sub-tabs overview + results) and
//                      "Save offline"; addressed by the URL hash
//   forms              check form, offline copy form, download list actions
//   rendering          progress, results table with filters, detail dialog
//   statistics         key figures and hand-written SVG charts; every figure
//                      carries data-drill (filter values for the results list)
//   mirror             the download list
//
// Security: every value that comes from a crawled site (URLs, titles) is
// inserted with esc() or textContent, never as raw HTML.
(() => {
  "use strict";

  const init = window.LINKSCOUT || {};
  const M = init.messages || {};
  let state = init.state || { progress: {}, results: [], stats: {}, mirror: {} };

  // UI-only state; sort and filters survive a reload via localStorage.
  const ui = Object.assign(
    { tab: "stats", sortKey: "", sortDesc: false, search: "", kind: "", status: "", type: "", depth: "", time: "", statsExternal: false },
    load("linkscout.ui")
  );
  delete ui.drill; // left over from earlier versions
  delete ui.site;

  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  // --- i18n -----------------------------------------------------------------

  /** Translate key and replace %name% placeholders. */
  function t(key, vars) {
    let s = M[key] ?? key;
    if (vars) for (const [k, v] of Object.entries(vars)) s = s.split(`%${k}%`).join(v);
    return s;
  }

  /** Fill every element marked with data-i18n* attributes with its translation. */
  function applyI18n() {
    for (const el of $$("[data-i18n]")) el.textContent = t(el.dataset.i18n);
    for (const el of $$("[data-i18n-placeholder]")) el.placeholder = t(el.dataset.i18nPlaceholder);
    for (const el of $$("[data-i18n-title]")) el.title = t(el.dataset.i18nTitle);
    $("#footer").textContent = t("misc.footer", { version: init.version || "dev" });
  }

  // --- helpers --------------------------------------------------------------

  /** Read a JSON value from localStorage ({} when missing or blocked). */
  function load(key) {
    try { return JSON.parse(localStorage.getItem(key)) || {}; } catch { return {}; }
  }
  /** Store a JSON value in localStorage (ignored when storage is blocked). */
  function save(key, val) {
    try { localStorage.setItem(key, JSON.stringify(val)); } catch { /* private mode */ }
  }
  /** Escape text for use inside HTML markup and attribute values. */
  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }
  /** Binary units (factor 1024): B, KiB, MiB, GiB, TiB. */
  function fmtBytes(n) {
    if (!(n > 0)) return "–";
    const u = ["B", "KiB", "MiB", "GiB", "TiB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    const digits = i ? 1 : 0;
    return `${new Intl.NumberFormat(init.lang, { minimumFractionDigits: digits, maximumFractionDigits: digits }).format(n)} ${u[i]}`;
  }
  /** Format a duration in milliseconds ("120 ms", "1.5 s"). */
  function fmtMs(ms) {
    if (!(ms >= 0)) return "–";
    return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
  }
  /** Format the time between two ISO timestamps ("42 s", "3 min 5 s"). */
  function fmtDuration(a, b) {
    const s = Math.max(0, Math.round((new Date(b) - new Date(a)) / 1000));
    return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60} s`;
  }
  /** Format a number with the grouping of the UI language (1,234 / 1.234). */
  function fmtNum(n) { return new Intl.NumberFormat(init.lang).format(n || 0); }
  /** Go serialises an unset time.Time as "0001-01-01T00:00:00Z". */
  function isSet(t) { return !!t && !t.startsWith("0001-"); }

  /** Translate the fixed error markers set by the crawler. */
  function errText(e) {
    return { aborted: t("error.aborted"), "not checked": t("error.not_checked") }[e] || e || "";
  }

  /** Type shown in the UI: the MIME type (old reports only have the extension). */
  const typeOf = (r) => r.mimeType || r.type || "";

  /** Status class used for colouring and filtering: 2xx…5xx, error, pending. */
  function statusClass(r) {
    if (r.pending) return "pending";
    if (r.status >= 100 && r.status < 600) return `${Math.floor(r.status / 100)}xx`;
    return "error";
  }

  /**
   * Call the JSON API. Every request carries the X-LinkScout header the server
   * requires for state changing requests (CSRF protection). Throws with the
   * server's error message on non-2xx answers.
   */
  async function api(method, path, body) {
    const res = await fetch(path, {
      method,
      headers: { "Content-Type": "application/json", "X-LinkScout": "1" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || res.statusText);
    return data;
  }

  /** Show an error in the panel of the active tab (the check settings are hidden on the mirror tab). */
  function showError(msg) {
    for (const b of $$(".error-box")) b.hidden = true;
    const box = $(ui.tab === "mirror" ? "#mirror-error" : "#error-box");
    box.hidden = !msg;
    box.textContent = msg ? t("error.generic", { error: msg }) : "";
  }

  // --- tabs -----------------------------------------------------------------

  /**
   * Switch the visible tab ("stats", "results" or "mirror"), keep the URL hash
   * and the navigation in sync and render the tab.
   */
  function setTab(tab) {
    if (!["results", "stats", "mirror"].includes(tab)) tab = "stats";
    ui.tab = tab;
    const navTab = tab === "stats" ? "results" : tab;
    for (const b of $$(".tabs button")) b.setAttribute("aria-selected", String(b.dataset.tab === navTab));
    for (const b of $$(".subtabs button")) b.setAttribute("aria-selected", String(b.dataset.tab === tab));
    for (const p of $$(".tab-panel")) p.classList.toggle("active", p.id === `tab-${tab}`);
    // Checking, import and export belong to the results and statistics only.
    $("#controls").hidden = $("#subtabs").hidden = tab === "mirror";
    showError("");
    if (location.hash !== `#${tab}`) history.replaceState(null, "", `#${tab}`);
    save("linkscout.ui", ui);
    render();
  }

  // --- forms ----------------------------------------------------------------

  const form = $("#check-form");
  const mform = $("#mirror-form");

  /** Fill the check form from the last run, falling back to the CLI defaults. */
  function fillForm() {
    const d = init.defaults || {};
    const cfg = state.config || {};
    const f = cfg.fetch || {};
    form.url.value = cfg.url || d.url || "";
    form.userAgent.value = f.userAgent || d.userAgent || init.firefoxUA || "";
    // The server never sends the password back; an empty field means "keep
    // the password already known for this user".
    form.username.value = f.username || d.username || "";
    if (form.username.value) $("details.advanced", form).open = true;
    form.concurrency.value = cfg.concurrency || d.concurrency || 8;
    form.maxPages.value = cfg.maxPages ?? d.maxPages ?? 0;
    form.delayMs.value = cfg.delay ? Math.round(cfg.delay / 1e6) : d.delayMs || 0;
    form.checkExternal.checked = cfg.url ? !!cfg.checkExternal : d.checkExternal !== false;
  }

  /** Fill the offline copy form with defaults and the remembered target template. */
  function fillMirrorForm() {
    const d = init.defaults || {};
    mform.outDir.value = load("linkscout.mirror").outDir || d.mirrorDir || "";
    mform.userAgent.value = d.userAgent || init.firefoxUA || "";
    mform.username.value = d.username || "";
    if (mform.username.value) $("details.advanced", mform).open = true;
    mform.concurrency.value = 4;
    mform.maxPages.value = 0;
    mform.delayMs.value = d.delayMs || 0;
    $("#dir-placeholders").innerHTML = t("mirror.placeholders")
      .replace(/<(\w+)>/g, (m) => `<code>${esc(m)}</code>`); // translations contain no other markup
    renderDirPreview();
  }

  /** Same replacement as server.ExpandMirrorDir, for the live preview. */
  function expandDir(tpl, url) {
    let host = "example.com", domain = "example.com";
    try {
      const u = new URL(url);
      const clean = (s) => s.replace(/[^A-Za-z0-9._-]/g, "_");
      host = clean(u.host);
      domain = clean(u.hostname.toLowerCase().replace(/^www\./, ""));
    } catch { /* no valid URL yet: show an example */ }
    const now = new Date();
    const p2 = (n) => String(n).padStart(2, "0");
    return tpl
      .split("<host>").join(host)
      .split("<domain>").join(domain)
      .split("<date>").join(`${now.getFullYear()}-${p2(now.getMonth() + 1)}-${p2(now.getDate())}`)
      .split("<time>").join(`${p2(now.getHours())}${p2(now.getMinutes())}${p2(now.getSeconds())}`);
  }

  /** Show the target directory the template expands to for the entered URL. */
  function renderDirPreview() {
    const tpl = mform.outDir.value.trim();
    $("#dir-preview").textContent = tpl ? t("mirror.preview", { dir: expandDir(tpl, mform.url.value.trim()) }) : "";
  }

  /** Collect the check settings from the form. */
  function formValues() {
    return {
      url: form.url.value.trim(),
      userAgent: form.userAgent.value.trim(),
      username: form.username.value.trim(),
      password: form.password.value,
      concurrency: +form.concurrency.value || 8,
      maxPages: +form.maxPages.value || 0,
      delayMs: +form.delayMs.value || 0,
      checkExternal: form.checkExternal.checked,
    };
  }

  /** Start a check: mode "all" (full crawl) or "failed" (re-check non-200). */
  async function startCheck(mode) {
    showError("");
    try {
      state = await api("POST", "/api/check", { ...formValues(), mode });
      render();
      poll();
    } catch (e) {
      showError(e.message);
    }
  }

  form.addEventListener("submit", (e) => { e.preventDefault(); startCheck("all"); });
  $("#btn-recheck").addEventListener("click", () => startCheck($("#recheck-mode").value));
  $("#btn-stop").addEventListener("click", async () => { state = await api("POST", "/api/stop"); render(); });
  $("#btn-reset-ua").addEventListener("click", () => { form.userAgent.value = init.firefoxUA; });

  $("#btn-import input").addEventListener("change", async (e) => {
    const file = e.target.files[0];
    e.target.value = "";
    if (!file) return;
    try {
      const report = JSON.parse(await file.text());
      state = await api("POST", "/api/import", report);
      fillForm();
      render();
    } catch (err) {
      showError(err.message);
    }
  });

  /** Add the mirror form settings to the download list (and start it). */
  async function addMirror(start) {
    showError("");
    const f = mform;
    try {
      state = await api("POST", start ? "/api/mirror" : "/api/mirror/queue", {
        url: f.url.value.trim(),
        outDir: f.outDir.value.trim(),
        userAgent: f.userAgent.value.trim(),
        username: f.username.value.trim(),
        password: f.password.value,
        concurrency: +f.concurrency.value || 4,
        maxPages: +f.maxPages.value || 0,
        delayMs: +f.delayMs.value || 0,
      });
      save("linkscout.mirror", { outDir: f.outDir.value.trim() });
      // Ready for the next website; all other settings stay.
      f.url.value = "";
      f.url.focus();
      renderDirPreview();
      render();
      poll();
    } catch (err) {
      showError(err.message);
    }
  }

  /** Run a list action (start/stop/remove) for one entry or all of them. */
  async function jobAction(action, body = {}) {
    showError("");
    try {
      state = await api("POST", `/api/mirror/${action}`, body);
      render();
      poll();
    } catch (err) {
      showError(err.message);
    }
  }

  mform.addEventListener("submit", (e) => { e.preventDefault(); addMirror(e.submitter?.name === "start"); });
  mform.url.addEventListener("input", renderDirPreview);
  mform.outDir.addEventListener("input", renderDirPreview);
  $("#btn-reset-dir").addEventListener("click", () => { mform.outDir.value = init.defaults?.mirrorDir || ""; renderDirPreview(); });
  $("#btn-mirror-reset-ua").addEventListener("click", () => { mform.userAgent.value = init.firefoxUA; });
  $("#btn-mirror-start-all").addEventListener("click", () => jobAction("start"));
  $("#btn-mirror-stop").addEventListener("click", () => jobAction("stop"));
  $("#btn-mirror-clear").addEventListener("click", () => jobAction("remove", { done: true }));
  $("#mirror-jobs").addEventListener("click", (e) => {
    const b = e.target.closest("button[data-action]");
    if (b) jobAction(b.dataset.action, { id: +b.closest("li").dataset.id });
  });

  // --- polling --------------------------------------------------------------

  let pollTimer = null;
  /** Whether a check or an offline copy is running (then the page polls). */
  const busy = () => !!state.progress?.running || (state.mirrors || []).some((j) => j.status === "running");
  /** Refresh the state every 800 ms while something is running. */
  function poll() {
    clearTimeout(pollTimer);
    pollTimer = setTimeout(async () => {
      try {
        state = await api("GET", "/api/state");
        render();
      } catch (e) {
        showError(e.message);
      }
      if (busy()) poll();
    }, 800);
  }

  // --- rendering ------------------------------------------------------------

  /** Re-render the progress bar and the visible tab from `state`. */
  function render() {
    renderProgress();
    if (ui.tab === "results") renderResults();
    if (ui.tab === "stats") renderStats();
    if (ui.tab === "mirror") renderMirror();
  }

  /** Update the buttons and the progress bar of the check controls. */
  function renderProgress() {
    const p = state.progress || {};
    const running = !!p.running;
    const hasResults = (state.results || []).length > 0;
    $("#btn-start").disabled = running;
    $("#btn-recheck").disabled = running || !hasResults;
    $("#recheck-mode").disabled = running || !hasResults;
    $("#btn-stop").disabled = !running;
    $("#btn-export").setAttribute("aria-disabled", String(!hasResults));

    const pct = p.total ? Math.round((p.done / p.total) * 100) : 0;
    const fill = $("#progress-fill");
    fill.style.width = `${running ? pct : hasResults ? 100 : 0}%`;
    fill.classList.toggle("done", !running && hasResults && !p.stopped);
    let text = t("progress.idle");
    const vars = { done: fmtNum(p.done), total: fmtNum(p.total) };
    if (running) text = t("progress.running", vars);
    else if (p.stopped) text = t("progress.stopped", vars);
    else if (isSet(p.finishedAt) && hasResults) text = t("progress.finished", { ...vars, duration: fmtDuration(p.startedAt, p.finishedAt) });
    $("#progress-text").textContent = text;
  }

  /** Fill a <select> keeping the current selection. */
  function fillSelect(sel, allLabel, options, current) {
    const html = [`<option value="">${esc(allLabel)}</option>`]
      .concat(options.map(([v, l]) => `<option value="${esc(v)}">${esc(l)}</option>`))
      .join("");
    if (sel.dataset.html !== html) {
      sel.innerHTML = html;
      sel.dataset.html = html;
    }
    sel.value = options.some(([v]) => v === current) ? current : "";
  }

  /** Depth filter values are "0", "1", … or "10+" (everything from 10 on). */
  const MAX_DEPTH = 10;
  function depthMatches(r, v) {
    const d = r.depth || 0;
    return v.endsWith("+") ? d >= parseInt(v, 10) : d === +v;
  }
  /** "Start page", "1 click", "2 clicks", "10+ clicks". */
  function clickLabel(d) {
    if (d === 0) return t("stats.start_page");
    return t(d === 1 ? "stats.one_click" : "stats.n_clicks", { n: d >= MAX_DEPTH ? `${MAX_DEPTH}+` : d });
  }

  /** Kind filter: a kind, or "noext" / "noint" = every kind except external / internal. */
  function kindMatches(r, k) {
    if (k === "noext") return r.kind !== "external";
    if (k === "noint") return r.kind !== "internal";
    return r.kind === k;
  }

  /** Status filter: a class (2xx …), "not200", "broken", "redirected" or an exact code "c404". */
  function statusMatches(r, st) {
    if (st === "not200") return r.status !== 200 && !r.pending;
    if (st === "broken") return r.status >= 400;
    if (st === "redirected") return !!r.redirects?.length;
    if (st[0] === "c") return r.status === +st.slice(1);
    return statusClass(r) === st;
  }

  /** Response time filter: index into TIME_BUCKETS (see the statistics). */
  function timeMatches(r, v) {
    const i = +v, lo = i ? TIME_BUCKETS[i - 1] : 0;
    return !r.pending && r.durationMs >= lo && r.durationMs < TIME_BUCKETS[i];
  }

  /** MIME type filter; "-" selects URLs without a known type. */
  const UNKNOWN_TYPE = "-";

  /** The results that pass every filter of the list. */
  function filtered() {
    const q = ui.search.trim().toLowerCase();
    return (state.results || []).filter((r) => {
      if (ui.kind && !kindMatches(r, ui.kind)) return false;
      if (ui.type && (typeOf(r) || UNKNOWN_TYPE) !== ui.type) return false;
      if (ui.status && !statusMatches(r, ui.status)) return false;
      if (ui.depth && !depthMatches(r, ui.depth)) return false;
      if (ui.time && !timeMatches(r, ui.time)) return false;
      if (q && !`${r.url} ${r.finalUrl || ""} ${r.title || ""}`.toLowerCase().includes(q)) return false;
      return true;
    });
  }

  /** Sort rows by the selected column (numbers numerically, text naturally). */
  function sorted(rows) {
    const k = ui.sortKey;
    if (!k) return rows;
    const dir = ui.sortDesc ? -1 : 1;
    const num = k === "status" || k === "durationMs" || k === "depth" || k === "size";
    const val = (r) => (k === "type" ? typeOf(r) : r[k]);
    return rows.slice().sort((a, b) => {
      let x = val(a), y = val(b);
      if (num) {
        // errors (status 0) sort after real status codes
        if (k === "status") { x = x || 1e9; y = y || 1e9; }
        return ((x || 0) - (y || 0)) * dir;
      }
      return String(x || "").localeCompare(String(y || ""), init.lang, { numeric: true }) * dir;
    });
  }

  /**
   * A URL that is cut off at the start ("…/path/page.html") when the cell is
   * too narrow: direction rtl moves the ellipsis to the left, the LRM marks
   * keep slashes and dots in place. The full URL is in the tooltip. http(s)
   * URLs are links that always open in a new tab.
   */
  const urlCell = (u, cls = "") => /^https?:\/\//i.test(u)
    ? `<a class="u ${cls}" href="${esc(u)}" target="_blank" rel="noopener noreferrer" title="${esc(u)}">&lrm;${esc(u)}&lrm;</a>`
    : `<span class="u ${cls}" title="${esc(u)}">&lrm;${esc(u)}&lrm;</span>`;

  // Rendering thousands of rows on every poll is wasteful; cap while running.
  const MAX_ROWS = 5000;

  /** Render the filter selects, the counter and the results table. */
  function renderResults() {
    const all = state.results || [];
    const types = [...new Set(all.map(typeOf).filter(Boolean))].sort();
    fillSelect($("#f-kind"), t("filter.all_kinds"),
      [...["internal", "external", "file"].map((k) => [k, t(`kind.${k}`)]), ["noint", t("filter.kind_noint")], ["noext", t("filter.kind_noext")]], ui.kind);
    const codes = [...new Set(all.map((r) => r.status).filter((c) => c > 0))].sort((a, b) => a - b);
    fillSelect($("#f-status"), t("filter.all_status"), [
      ["not200", t("status.not200")], ["broken", t("status.broken")], ["redirected", t("status.redirected")],
      ...["2xx", "3xx", "4xx", "5xx", "error", "pending"].map((c) => [c, t(`status.${c}`)]),
      ...codes.map((c) => [`c${c}`, t("status.code", { code: c })]),
    ], ui.status);
    const mimeOptions = types.map((x) => [x, x]);
    if (all.some((r) => !typeOf(r))) mimeOptions.push([UNKNOWN_TYPE, t("stats.unknown_type")]);
    fillSelect($("#f-type"), t("filter.all_types"), mimeOptions, ui.type);
    fillSelect($("#f-time"), t("filter.all_times"), TIME_BUCKETS.map((_, i) => [String(i), timeLabel(i)]), ui.time);
    const depths = [...new Set(all.map((r) => Math.min(r.depth || 0, MAX_DEPTH)))].sort((a, b) => a - b);
    fillSelect($("#f-depth"), t("filter.all_depths"),
      depths.map((d) => [d >= MAX_DEPTH ? `${MAX_DEPTH}+` : String(d), clickLabel(d)]), ui.depth);
    $("#f-search").value = ui.search;

    for (const th of $$("#results-table th")) {
      th.classList.toggle("sorted", th.dataset.sort === ui.sortKey);
      th.classList.toggle("desc", th.dataset.sort === ui.sortKey && ui.sortDesc);
    }

    const rows = sorted(filtered());
    $("#f-count").textContent = t("filter.count", { shown: fmtNum(rows.length), total: fmtNum(all.length) });
    $("#table-empty").hidden = rows.length > 0;
    $("#results-table tbody").innerHTML = rows.slice(0, MAX_ROWS).map((r) => {
      const cls = statusClass(r);
      const status = r.pending ? "…" : r.status || "ERR";
      const final = r.finalUrl ? urlCell(r.finalUrl, "final") : "";
      return `<tr data-url="${esc(r.url)}">
        <td class="url">${urlCell(r.url)}${final}</td>
        <td class="num"><span class="badge s${cls}" title="${esc(errText(r.error))}">${status}</span></td>
        <td class="type">${esc(typeOf(r))}</td>
        <td class="title">${esc(r.title || errText(r.error))}</td>
        <td><span class="kind">${esc(t(`kind.${r.kind}`))}</span></td>
        <td class="num">${r.depth || 0}</td>
        <td class="num">${r.pending ? "" : fmtBytes(r.size)}</td>
        <td class="num">${r.pending ? "" : fmtMs(r.durationMs)}</td>
      </tr>`;
    }).join("");
  }

  for (const th of $$("#results-table th")) {
    th.addEventListener("click", () => {
      const k = th.dataset.sort;
      if (ui.sortKey === k) {
        if (ui.sortDesc) { ui.sortKey = ""; ui.sortDesc = false; } // third click: unsorted
        else ui.sortDesc = true;
      } else {
        ui.sortKey = k; ui.sortDesc = false;
      }
      save("linkscout.ui", ui);
      renderResults();
    });
  }
  $("#f-search").addEventListener("input", (e) => { ui.search = e.target.value; save("linkscout.ui", ui); renderResults(); });
  for (const [id, key] of [["#f-kind", "kind"], ["#f-status", "status"], ["#f-type", "type"], ["#f-depth", "depth"], ["#f-time", "time"]]) {
    $(id).addEventListener("change", (e) => { ui[key] = e.target.value; save("linkscout.ui", ui); renderResults(); });
  }
  $("#f-reset").addEventListener("click", () => {
    Object.assign(ui, { search: "", kind: "", status: "", type: "", depth: "", time: "" });
    save("linkscout.ui", ui);
    renderResults();
  });

  // --- detail dialog --------------------------------------------------------

  $("#results-table tbody").addEventListener("click", (e) => {
    if (e.target.closest("a")) return; // the link itself opens in a new tab
    const tr = e.target.closest("tr");
    if (tr) openDetail(tr.dataset.url);
  });
  $("#detail-close").addEventListener("click", () => $("#detail").close());
  $("#detail").addEventListener("click", (e) => { if (e.target.id === "detail") $("#detail").close(); });

  /** Show all details of one result in the dialog. */
  function openDetail(url) {
    const r = (state.results || []).find((x) => x.url === url);
    if (!r) return;
    const link = (u) => `<a href="${esc(u)}" target="_blank" rel="noopener noreferrer">${esc(u)}</a>`;
    const rows = [
      [t("col.status"), `<span class="badge s${statusClass(r)}">${r.pending ? "…" : r.status || "ERR"}</span>`],
      r.error && [t("detail.error"), esc(errText(r.error))],
      r.title && [t("col.title"), esc(r.title)],
      r.finalUrl && [t("detail.final_url"), link(r.finalUrl)],
      r.redirects?.length && [t("detail.redirects"), `<ol>${r.redirects.concat(r.finalUrl || []).map((u) => `<li>${esc(u)}</li>`).join("")}</ol>`],
      [t("col.type"), esc(typeOf(r) || "–")],
      [t("col.kind"), esc(t(`kind.${r.kind}`))],
      r.contentType && [t("detail.content_type"), esc(r.contentType)],
      [t("detail.size"), fmtBytes(r.size)],
      [t("detail.duration"), fmtMs(r.durationMs)],
      [t("detail.depth"), r.depth],
      isSet(r.checkedAt) && [t("detail.checked_at"), new Date(r.checkedAt).toLocaleString(init.lang)],
      r.foundOn?.length && [t("detail.found_on"), `<ul>${r.foundOn.map((u) => `<li>${link(u)}</li>`).join("")}</ul>`],
    ].filter(Boolean);
    $("#detail-title").innerHTML = link(r.url);
    $("#detail-body").innerHTML = rows.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${v}</dd>`).join("");
    $("#detail").showModal();
  }

  // --- statistics -----------------------------------------------------------
  //
  // Charts are plain inline SVG built from state.results; no chart library.
  // Every mark carries data-tip / data-tip-label, the shared tooltip reads
  // them with textContent (URLs and titles are untrusted).

  /**
   * Drill-down: clicking a statistic opens the results list with its filters
   * set to the same URLs. data-drill holds those filter values as JSON.
   */
  let drillScope = {}; // set by renderStats: kind "noext" when external links are left out
  const drillAttr = (filters) => (filters ? `data-drill="${esc(JSON.stringify({ ...drillScope, ...filters }))}"` : "");

  /** Simple bar list (label · bar · value); drill(key) gives the criteria of a row. */
  function bars(items, label, colorFor, fmt = fmtNum, drill) {
    if (!items || !items.length) return `<p class="muted">${esc(t("stats.none"))}</p>`;
    const maxV = Math.max(...items.map((i) => i.count));
    return `<div class="bars">${items.map((i) => `<button type="button" class="bar-row" ${drillAttr(drill && drill(i.key))}>
      <span class="lbl">${esc(label(i.key))}</span>
      <span class="track"><span class="val" style="display:block;width:${(i.count / maxV) * 100}%;${colorFor ? `background:${colorFor(i.key)}` : ""}"></span></span>
      <span class="n">${esc(fmt(i.count))}</span></button>`).join("")}</div>`;
  }

  /** Colour of a status class / status code (the reserved status palette). */
  const classColor = (k) => ({ "2xx": "var(--ok)", "3xx": "var(--info)", "4xx": "var(--bad)", "5xx": "var(--warn)", error: "var(--err)", pending: "var(--muted)" }[k]);
  const codeColor = (k) => classColor(`${String(k)[0]}xx`) || "var(--muted)";
  const tipAttrs = (value, label) => `tabindex="0" data-tip="${esc(value)}" data-tip-label="${esc(label)}"`;
  /** Small "i" button whose explanation appears in the shared tooltip. */
  const info = (text) => `<button type="button" class="info" aria-label="${esc(text)}" data-tip="" data-tip-label="${esc(text)}">i</button>`;
  const pct = (n, total) => `${new Intl.NumberFormat(init.lang, { maximumFractionDigits: 1 }).format((n / total) * 100)} %`;

  /** Clean y axis: a step of 1, 2 or 5 × 10^k and at most 5 ticks above 0. */
  function niceScale(v) {
    const raw = Math.max(1, v) / 4;
    const p = 10 ** Math.floor(Math.log10(raw));
    const step = Math.max(1, [1, 2, 5, 10].map((m) => m * p).find((x) => x >= raw));
    return { step, max: Math.ceil(Math.max(1, v) / step) * step };
  }
  /** Compact axis label: 1,200 → "1.2K". */
  function fmtCompact(n) {
    return new Intl.NumberFormat(init.lang, { notation: "compact", maximumFractionDigits: 1 }).format(n);
  }

  /** Path of a column with a 4px rounded top and a square base. */
  function colPath(x, y, w, h, round) {
    const r = round ? Math.min(4, w / 2, h) : 0;
    return `M${x},${y + h}V${y + r}${r ? `Q${x},${y} ${x + r},${y}` : ""}H${x + w - r}${r ? `Q${x + w},${y} ${x + w},${y + r}` : ""}V${y + h}Z`;
  }

  /**
   * Column chart, optionally stacked. groups: [{label, values: [n per series], tip, tipLabel}],
   * series: [{name, color}]. The total is written on each column's cap.
   */
  function columnChart(groups, series, width) {
    const H = 220, top = 20, bottom = 28, left = 40, right = 8;
    const plotW = Math.max(100, width - left - right), plotH = H - top - bottom;
    const totals = groups.map((g) => g.values.reduce((a, b) => a + b, 0));
    const { step, max: maxV } = niceScale(Math.max(...totals));
    const band = plotW / Math.max(1, groups.length);
    const barW = Math.min(24, band * 0.6);
    const y = (v) => top + plotH - (v / maxV) * plotH;
    let svg = "";
    for (let v = 0; v <= maxV; v += step) { // hairline grid + ticks
      const yy = Math.round(y(v)) + 0.5;
      svg += `<line class="grid" x1="${left}" x2="${left + plotW}" y1="${yy}" y2="${yy}"/>`;
      svg += `<text class="tick" x="${left - 6}" y="${yy + 4}" text-anchor="end">${fmtCompact(v)}</text>`;
    }
    groups.forEach((g, i) => {
      const cx = left + band * i + band / 2, x = cx - barW / 2;
      let acc = 0;
      const last = g.values.reduce((l, v, si) => (v > 0 ? si : l), -1);
      g.values.forEach((v, si) => {
        if (!v) return;
        const y0 = y(acc), y1 = y(acc + v);
        // 2px surface gap between stacked segments
        const h = Math.max(1, y0 - y1 - (si === last ? 0 : 2));
        svg += `<path d="${colPath(x, y1 + (si === last ? 0 : 2), barW, h, si === last)}" fill="${series[si].color}"/>`;
        acc += v;
      });
      if (totals[i]) svg += `<text class="cap" x="${cx}" y="${y(totals[i]) - 6}" text-anchor="middle">${fmtCompact(totals[i])}</text>`;
      svg += `<text class="tick" x="${cx}" y="${H - 8}" text-anchor="middle">${esc(g.label)}</text>`;
      // hit target: the whole band, not just the painted column
      svg += `<rect class="hit" x="${left + band * i}" y="${top}" width="${band}" height="${plotH}" ${tipAttrs(g.tip, g.tipLabel || g.label)} ${totals[i] ? drillAttr(g.drill) : ""} role="button"/>`;
    });
    svg += `<line class="axis" x1="${left}" x2="${left + plotW}" y1="${top + plotH + 0.5}" y2="${top + plotH + 0.5}"/>`;
    const legend = series.length > 1
      ? `<div class="legend">${series.map((se) => `<span><i style="background:${se.color}"></i>${esc(se.name)}</span>`).join("")}</div>`
      : "";
    return `<svg class="chart" width="${width}" height="${H}" viewBox="0 0 ${width} ${H}" role="img">${svg}</svg>${legend}`;
  }

  /** One 100 % stacked bar (part-to-whole) with a legend carrying count and share. */
  function shareBar(items, label, color, drill) {
    const total = items.reduce((a, i) => a + i.count, 0);
    if (!total) return `<p class="muted">${esc(t("stats.none"))}</p>`;
    const segs = items.map((i) => {
      const tip = `${fmtNum(i.count)} · ${pct(i.count, total)}`;
      return `<span class="seg" role="button" style="flex-grow:${i.count};background:${color(i.key)}" ${tipAttrs(tip, label(i.key))} ${drillAttr(drill(i.key))}></span>`;
    }).join("");
    const legend = items.map((i) => `<span><i style="background:${color(i.key)}"></i>${esc(label(i.key))} <b>${fmtNum(i.count)}</b> <span class="muted">${pct(i.count, total)}</span></span>`).join("");
    return `<div class="share">${segs}</div><div class="legend">${legend}</div>`;
  }

  const TIME_BUCKETS = [100, 250, 500, 1000, 2000, 5000, Infinity];
  /** Label of response time bucket i ("< 100 ms", "1–2 s" …). */
  function timeLabel(i) {
    const half = new Intl.NumberFormat(init.lang).format(0.5);
    return ["< 100 ms", "100–250 ms", "250–500 ms", `${half}–1 s`, "1–2 s", "2–5 s", "≥ 5 s"][i];
  }

  /**
   * Key figures for a list of results – the same rules as crawler.Summarize,
   * computed here because the page can leave out external URLs.
   */
  function summarize(rs) {
    const s = { total: rs.length, ok: 0, redirected: 0, broken: 0, errors: 0, internalPages: 0, totalBytes: 0, avgMs: 0 };
    const classes = {}, codes = {};
    let timed = 0, sumMs = 0;
    for (const r of rs) {
      const c = statusClass(r);
      classes[c] = (classes[c] || 0) + 1;
      if (r.status > 0) { codes[r.status] = (codes[r.status] || 0) + 1; timed++; sumMs += r.durationMs || 0; }
      if (r.size > 0) s.totalBytes += r.size;
      if (r.redirects?.length) s.redirected++;
      if (r.status === 200) s.ok++;
      else if (r.status >= 400) s.broken++;
      else if (!r.status) s.errors++;
      if (r.kind === "internal" && r.status === 200) s.internalPages++;
    }
    s.avgMs = timed ? Math.round(sumMs / timed) : 0;
    s.byStatusClass = ["2xx", "3xx", "4xx", "5xx", "error"].filter((k) => classes[k]).map((key) => ({ key, count: classes[key] }));
    s.byCode = Object.keys(codes).map(Number).sort((a, b) => a - b).map((c) => ({ key: String(c), count: codes[c] }));
    return s;
  }

  /**
   * Render the overview: key figures, the status share bar, the charts and the
   * bar lists, all computed from the (optionally site-only) results.
   */
  function renderStats() {
    const el = $("#stats-body");
    const checked = (state.results || []).filter((r) => !r.pending);
    if (!checked.length) {
      el.innerHTML = `<div class="panel empty">${esc(t("stats.empty"))}</div>`;
      return;
    }
    // External = links to pages on other websites (kind "external"); files
    // always count, wherever they are hosted.
    const externalCount = checked.filter((r) => r.kind === "external").length;
    const rs = ui.statsExternal ? checked : checked.filter((r) => r.kind !== "external");
    const s = summarize(rs);
    drillScope = { kind: ui.statsExternal ? "" : "noext" };
    const kpi = (v, l, color, drill) => `<${drill ? "button type=\"button\"" : "div"} class="kpi" ${drillAttr(drill)}><div class="v" ${color ? `style="color:${color}"` : ""}>${v}</div><div class="l">${esc(t(l))}</div></${drill ? "button" : "div"}>`;
    const nURLs = (n) => (n === 1 ? t("stats.one_url") : t("stats.n_urls", { n: fmtNum(n) }));

    // response time histogram
    const timeCounts = TIME_BUCKETS.map(() => 0);
    for (const r of rs) timeCounts[TIME_BUCKETS.findIndex((b) => (r.durationMs || 0) < b)]++;
    // drop the empty slow tail, but always show the buckets up to 1 s
    while (timeCounts.length > 4 && !timeCounts[timeCounts.length - 1]) timeCounts.pop();
    const timeGroups = timeCounts.map((n, i) => ({
      label: timeLabel(i), values: [n], tip: nURLs(n),
      drill: { time: String(i) },
    }));

    // clicks from the start page; very deep links are folded into "10+"
    const depthMax = Math.min(MAX_DEPTH, Math.max(0, ...rs.map((r) => r.depth || 0)));
    const depth = Array.from({ length: depthMax + 1 }, () => 0);
    for (const r of rs) depth[Math.min(r.depth || 0, depthMax)]++;
    const depthGroups = depth.map((n, d) => ({
      label: clickLabel(d), values: [n], tip: nURLs(n),
      drill: { depth: d >= MAX_DEPTH ? `${MAX_DEPTH}+` : String(d) },
    }));

    // size by type (Content-Length of the responses); every type is listed,
    // no "other" bucket, so the bars add up to the total size
    const sizes = {};
    const unknownTypeLabel = t("stats.unknown_type");
    const types = {};
    for (const r of rs) {
      const ty = typeOf(r) || unknownTypeLabel;
      types[ty] = (types[ty] || 0) + 1;
      if (r.size > 0) sizes[ty] = (sizes[ty] || 0) + r.size;
    }
    const unknownType = t("stats.unknown_type");
    const mimeDrill = (k) => ({ type: k === unknownType ? UNKNOWN_TYPE : k });
    const typeItems = Object.entries(types).map(([key, count]) => ({ key, count })).sort((a, b) => b.count - a.count || a.key.localeCompare(b.key));
    const sizeItems = Object.entries(sizes).map(([key, count]) => ({ key, count })).sort((a, b) => b.count - a.count);

    const statusItems = s.byStatusClass;

    el.innerHTML = `
      <div class="stats-filter">
        <label class="check"><input type="checkbox" id="stats-external" ${ui.statsExternal ? "checked" : ""}>
          <span>${esc(t("stats.include_external", { n: fmtNum(externalCount) }))}</span></label>
      </div>
      <div class="kpis">
        ${kpi(fmtNum(s.total), "stats.total", "", {})}
        ${kpi(fmtNum(s.ok), "stats.ok", "var(--ok)", { status: "c200" })}
        ${kpi(fmtNum(s.redirected), "stats.redirected", "var(--info)", { status: "redirected" })}
        ${kpi(fmtNum(s.broken), "stats.broken", s.broken ? "var(--bad)" : "", { status: "broken" })}
        ${kpi(fmtNum(s.errors), "stats.errors", s.errors ? "var(--err)" : "", { status: "error" })}
        ${kpi(fmtNum(s.internalPages), "stats.pages", "", { kind: "internal", status: "c200" })}
        ${kpi(fmtMs(s.avgMs), "stats.avg_time", "", { sort: "durationMs" })}
      </div>
      <div class="panel"><h3>${esc(t("stats.chart_status"))}</h3>${shareBar(statusItems, (k) => t(`status.${k}`), classColor, (k) => ({ status: k }))}</div>
      <div class="grid charts">
        <div class="panel"><h3>${esc(t("stats.chart_time"))}</h3><p class="sub">${esc(t("stats.chart_time_sub"))}</p><div class="chart-box" data-chart="time"></div></div>
        <div class="panel"><h3>${esc(t("stats.chart_depth"))}</h3><p class="sub">${esc(t("stats.chart_depth_sub"))}</p><div class="chart-box" data-chart="depth"></div></div>
      </div>
      <div class="grid">
        <div class="panel"><h3>${esc(t("stats.by_code"))}</h3>${bars(s.byCode, (k) => k, codeColor, fmtNum, (k) => ({ status: `c${k}` }))}</div>
        <div class="panel"><h3>${esc(t("stats.by_type"))}</h3>${bars(typeItems, (k) => k, null, fmtNum, mimeDrill)}</div>
        <div class="panel"><h3 class="with-total">${esc(t("stats.size_by_type"))}${info(t("stats.size_explain"))}<span>${esc(t("stats.size_total", { size: fmtBytes(s.totalBytes) }))}</span></h3>${bars(sizeItems, (k) => k, null, fmtBytes, mimeDrill)}</div>
      </div>`;

    // Columns are drawn at the real width of their box (crisp text, no scaling).
    drawCharts = () => {
      for (const box of $$(".chart-box", el)) {
        const w = Math.floor(box.clientWidth);
        if (!w) continue;
        box.innerHTML = box.dataset.chart === "time"
          ? columnChart(timeGroups, [{ name: "", color: "var(--accent)" }], w)
          : columnChart(depthGroups, [{ name: "", color: "var(--accent)" }], w);
      }
    };
    drawCharts();
  }

  let drawCharts = () => {};
  let resizeTimer;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => { if (ui.tab === "stats") drawCharts(); }, 100);
  });

  /** Open the results list with the criteria of a clicked statistic. */
  function drillDown(el) {
    const { sort, ...filters } = JSON.parse(el.dataset.drill);
    Object.assign(ui, { search: "", kind: "", status: "", type: "", depth: "", time: "" }, filters);
    if (sort) { ui.sortKey = sort; ui.sortDesc = true; }
    tip.hidden = true;
    setTab("results");
    window.scrollTo({ top: $("#subtabs").offsetTop - 70 });
  }
  $("#stats-body").addEventListener("click", (e) => {
    const el = e.target.closest("[data-drill]");
    if (el) drillDown(el);
  });
  $("#stats-body").addEventListener("keydown", (e) => {
    const el = e.target.closest("[data-drill]");
    if (el && (e.key === "Enter" || e.key === " ") && el.tagName !== "BUTTON") { e.preventDefault(); drillDown(el); }
  });

  $("#stats-body").addEventListener("change", (e) => {
    if (e.target.id !== "stats-external") return;
    ui.statsExternal = e.target.checked;
    save("linkscout.ui", ui);
    renderStats();
    $("#stats-external").focus();
  });

  // Shared tooltip for every chart mark (hover and keyboard focus).
  const tip = document.createElement("div");
  tip.className = "chart-tip";
  tip.hidden = true;
  tip.innerHTML = "<strong></strong><span></span>";
  document.body.append(tip);
  /** Position and fill the shared tooltip for a chart mark near (x, y). */
  function showTip(mark, x, y) {
    tip.firstChild.textContent = mark.dataset.tip;
    tip.lastChild.textContent = mark.dataset.tipLabel;
    tip.hidden = false;
    const w = tip.offsetWidth, h = tip.offsetHeight;
    tip.style.left = `${Math.min(window.innerWidth - w - 8, Math.max(8, x - w / 2))}px`;
    tip.style.top = `${y - h - 12 < 8 ? y + 16 : y - h - 12}px`;
  }
  const statsBody = $("#stats-body");
  statsBody.addEventListener("pointermove", (e) => {
    const m = e.target.closest("[data-tip]");
    if (m) showTip(m, e.clientX, e.clientY); else tip.hidden = true;
  });
  statsBody.addEventListener("pointerleave", () => { tip.hidden = true; });
  statsBody.addEventListener("focusin", (e) => {
    const m = e.target.closest("[data-tip]");
    if (!m) return;
    const b = m.getBoundingClientRect();
    showTip(m, b.left + b.width / 2, b.top);
  });
  statsBody.addEventListener("focusout", () => { tip.hidden = true; });

  // --- mirror ---------------------------------------------------------------

  /** Render the toolbar state and the entries of the download list. */
  function renderMirror() {
    const jobs = state.mirrors || [];
    const running = jobs.some((j) => j.status === "running");
    $("#btn-mirror-start-all").disabled = !jobs.some((j) => ["queued", "failed", "stopped"].includes(j.status));
    $("#btn-mirror-stop").disabled = !running;
    $("#btn-mirror-clear").disabled = !jobs.some((j) => j.status === "done");
    $("#mirror-empty").hidden = jobs.length > 0;

    const list = $("#mirror-jobs");
    const html = jobs.map(jobHTML).join("");
    if (list.dataset.html !== html) { // keep focus and hover while polling
      list.innerHTML = html;
      list.dataset.html = html;
    }
  }

  /** Markup of one download list entry: URL, settings, progress, actions. */
  function jobHTML(j) {
    const m = j.progress || {};
    const st = j.status;
    const vars = { done: fmtNum(st === "running" ? m.done : m.files), bytes: fmtBytes(m.bytes), dir: j.outDir, error: m.error || "" };
    const text = {
      queued: t("mirror.queued"),
      running: t("mirror.running", vars),
      done: `${t("mirror.finished", vars)}\n${t("mirror.open_hint")}`, // own line, see .job .progress-text
      failed: t("mirror.failed", vars),
      stopped: t("mirror.stopped", vars),
    }[st] || "";
    const pct = st === "running" ? (m.total ? Math.round((m.done / m.total) * 100) : 0) : st === "done" || st === "failed" ? 100 : 0;
    const settings = [
      `${t("mirror.s_concurrency")}: ${j.concurrency}`,
      `${t("mirror.s_pages")}: ${j.maxPages || t("mirror.unlimited")}`,
      `${t("mirror.s_delay")}: ${fmtMs(j.delayMs)}`,
      j.username && `${t("mirror.s_user")}: ${esc(j.username)}`,
    ].filter(Boolean).join(" · ");
    const btn = (action, label, cls = "") => `<button type="button" class="small ${cls}" data-action="${action}">${esc(t(label))}</button>`;
    const actions = [
      st === "running" ? btn("stop", "form.stop", "danger")
        : btn("start", st === "queued" ? "mirror.start_one" : "mirror.restart"),
      st === "done" ? btn("open", "mirror.open_dir") : "",
      st === "done" ? `<a class="button small" href="/api/mirror/zip?id=${j.id}" download>${esc(t("mirror.zip"))}</a>` : "",
      btn("remove", "mirror.remove", "ghost"),
    ].join("");
    return `<li data-id="${j.id}" class="job ${st}">
      <div class="job-main">
        <div class="job-url">${esc(j.url)}</div>
        <div class="job-meta"><span class="mono">${esc(j.outDir)}</span></div>
        <div class="job-meta">${settings}</div>
        <div class="job-meta ua" title="${esc(j.userAgent)}">${esc(j.userAgent)}</div>
        <div class="bar"><div class="fill${st === "done" ? " done" : st === "failed" ? " failed" : ""}" style="width:${pct}%"></div></div>
        <div class="progress-text">${esc(text)}</div>
      </div>
      <div class="job-actions">${actions}</div>
    </li>`;
  }

  // Links to other pages never replace the LinkScout window, wherever they
  // are (results, detail dialog, …): force a new tab without referrer.
  document.addEventListener("click", (e) => {
    const a = e.target.closest("a[href]");
    if (!a || a.hasAttribute("download") || a.origin === location.origin) return;
    a.target = "_blank";
    a.rel = "noopener noreferrer";
  }, true);

  // --- theme ----------------------------------------------------------------

  const savedTheme = load("linkscout.theme").theme;
  if (savedTheme) document.documentElement.dataset.theme = savedTheme;
  $("#theme-toggle").addEventListener("click", () => {
    const dark = document.documentElement.dataset.theme
      ? document.documentElement.dataset.theme === "dark"
      : matchMedia("(prefers-color-scheme: dark)").matches;
    document.documentElement.dataset.theme = dark ? "light" : "dark";
    save("linkscout.theme", { theme: document.documentElement.dataset.theme });
  });

  // --- boot -----------------------------------------------------------------

  for (const b of $$(".subtabs button")) b.addEventListener("click", () => setTab(b.dataset.tab));
  // The link checker opens on its first sub-tab, the overview.
  for (const b of $$(".tabs button")) b.addEventListener("click", () => setTab(b.dataset.tab === "results" ? "stats" : b.dataset.tab));
  window.addEventListener("hashchange", () => setTab(location.hash.slice(1)));
  applyI18n();
  fillForm();
  fillMirrorForm();
  setTab(location.hash.slice(1) || "stats"); // a fresh start always shows the overview
  requestAnimationFrame(() => document.body.classList.add("ready"));
  if (busy()) poll();
})();

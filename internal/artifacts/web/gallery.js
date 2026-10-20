// Ta-Da artifacts — gallery page (served at /).
// Loaded synchronously in <head> so the saved theme applies before first paint;
// everything else waits for DOMContentLoaded.
(() => {
  'use strict';

  // ---------- shared helpers (kept in sync with viewer.js) ----------

  const THEME_KEY = 'tada:theme';
  const THEMES = ['system', 'light', 'dark'];
  const THEME_ICON = { system: '◐', light: '☀', dark: '☾' };

  function loadTheme() {
    try {
      const t = localStorage.getItem(THEME_KEY);
      return THEMES.includes(t) ? t : 'system';
    } catch { return 'system'; }
  }
  function saveTheme(t) {
    try {
      if (t === 'system') localStorage.removeItem(THEME_KEY);
      else localStorage.setItem(THEME_KEY, t);
    } catch { /* storage unavailable */ }
  }
  function applyTheme(t) {
    const root = document.documentElement;
    if (t === 'light' || t === 'dark') root.dataset.theme = t;
    else delete root.dataset.theme;
  }

  let theme = loadTheme();
  applyTheme(theme);

  // ---------- viewer key ----------
  // Ta-Da opens the viewer with #t=<ticket>, a one-time sign-in ticket.
  // Swap it for the key, keep the key in this origin's storage (which,
  // unlike cookies, is separate per port) and take the ticket out of the
  // address bar. Every API call sends the key.
  const KEY_STORE = 'tada:key';
  let viewerKey = '';
  try { viewerKey = localStorage.getItem(KEY_STORE) || ''; } catch { /* storage unavailable */ }
  async function redeemTicket() {
    const m = /^#t=([0-9a-f.]{1,128})$/.exec(location.hash);
    if (!m) return false;
    try { history.replaceState(null, '', location.pathname + location.search); } catch { /* ignore */ }
    try {
      const res = await fetch('/api/signin', {
        method: 'POST',
        headers: { 'X-Tada-Viewer': '1', 'Content-Type': 'application/json' },
        body: JSON.stringify({ ticket: m[1] }),
      });
      const data = await res.json();
      if (!res.ok || !/^[0-9a-f]{64}$/.test(data.key || '')) return false;
      viewerKey = data.key;
      try { localStorage.setItem(KEY_STORE, viewerKey); } catch { /* storage unavailable */ }
      return true;
    } catch { return false; }
  }
  const signedIn = redeemTicket();
  // A tab already open on this page only sees a hash change, so sign in
  // and start over with the new key.
  window.addEventListener('hashchange', async () => {
    if (await redeemTicket()) location.reload();
  });
  const SIGN_IN_MSG = 'Open it from Ta-Da ("Open Artifacts Gallery" in the command palette, or "tada artifacts open") to sign in this browser.';

  async function api(path, { method = 'GET', body } = {}) {
    const init = { method, headers: {} };
    if (viewerKey) init.headers['X-Tada-Key'] = viewerKey;
    if (method !== 'GET') {
      init.headers['X-Tada-Viewer'] = '1';
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body ?? {});
    }
    const res = await fetch(path, init);
    let data = null;
    try { data = await res.json(); } catch { /* empty or non-JSON */ }
    if (!res.ok) {
      const err = new Error((data && typeof data.error === 'string' && data.error) || `Request failed (${res.status})`);
      err.status = res.status;
      throw err;
    }
    return data || {};
  }

  function relTime(iso) {
    const t = Date.parse(iso);
    if (!Number.isFinite(t)) return '';
    const s = (Date.now() - t) / 1000;
    if (s < 45) return 'just now';
    if (s < 3600) return `${Math.max(1, Math.round(s / 60))}m ago`;
    if (s < 86400) return `${Math.round(s / 3600)}h ago`;
    if (s < 7 * 86400) return `${Math.round(s / 86400)}d ago`;
    const d = new Date(t);
    const opts = { month: 'short', day: 'numeric' };
    if (d.getFullYear() !== new Date().getFullYear()) opts.year = 'numeric';
    return d.toLocaleDateString(undefined, opts);
  }
  function fullTime(iso) {
    const t = Date.parse(iso);
    return Number.isFinite(t) ? new Date(t).toLocaleString() : '';
  }

  const ICONS = [
    [['chart', 'graph', 'plot'], '📊'], [['dashboard', 'metrics', 'kpi'], '📈'], [['calendar', 'schedule'], '📅'],
    [['recipe', 'food', 'cook'], '🍳'], [['code', 'terminal', 'dev'], '💻'], [['map'], '🗺️'],
    [['doc', 'document', 'note', 'notes'], '📄'], [['slides', 'slide', 'deck', 'presentation'], '🎞️'],
    [['game'], '🎮'], [['list', 'todo'], '📝'], [['table', 'spreadsheet', 'sheet'], '🧮'], [['design', 'art', 'palette'], '🎨'],
    [['music', 'audio', 'song'], '🎵'], [['photo', 'image', 'picture', 'gallery'], '🖼️'], [['book', 'reading'], '📚'],
    [['money', 'finance', 'budget', 'invoice'], '💰'], [['timer', 'clock', 'time'], '⏱️'], [['form', 'survey'], '🧾'],
    [['tracker', 'checklist', 'habit'], '✅'], [['report', 'summary'], '📑'], [['chat', 'message'], '💬'],
    [['mail', 'email', 'newsletter'], '✉️'], [['travel', 'trip', 'flight'], '✈️'], [['health', 'fitness', 'heart'], '❤️'],
    [['sport', 'sports', 'football', 'soccer'], '⚽'], [['weather'], '🌤️'], [['data', 'database', 'files'], '🗂️'],
    [['tool', 'tools', 'utility', 'calculator'], '🛠️'], [['web', 'site', 'website', 'page', 'globe'], '🌐'], [['idea', 'brainstorm'], '💡'],
  ];
  function iconFor(word) {
    const w = String(word || '').trim().toLowerCase();
    if (!w) return '';
    if (/[^\x00-\x7f]/.test(w) && w.length <= 8) return w; // already an emoji
    for (const [keys, emoji] of ICONS) if (keys.includes(w)) return emoji;
    for (const [keys, emoji] of ICONS) if (keys.some((k) => k.length > 3 && w.includes(k))) return emoji;
    return '';
  }
  function glyphEl(a) {
    const span = document.createElement('span');
    const emoji = iconFor(a.icon);
    span.setAttribute('aria-hidden', 'true');
    if (emoji) {
      span.className = 'glyph';
      span.textContent = emoji;
    } else {
      span.className = 'glyph letter';
      const first = Array.from(String(a.title || '?').trim())[0] || '?';
      span.textContent = first.toUpperCase();
    }
    return span;
  }
  function baseName(p) {
    const parts = String(p || '').split(/[\\/]+/).filter(Boolean);
    return parts.length ? parts[parts.length - 1] : '';
  }
  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function toast(msg, isErr) {
    const box = document.getElementById('toasts');
    if (!box) return;
    const t = el('div', 'toast' + (isErr ? ' err' : ''), msg);
    box.appendChild(t);
    setTimeout(() => { t.classList.add('out'); setTimeout(() => t.remove(), 350); }, isErr ? 5000 : 2800);
  }

  function confirmDialog({ title, message, ok = 'Delete' }) {
    const dlg = document.getElementById('confirm');
    return new Promise((resolve) => {
      if (!dlg || typeof dlg.showModal !== 'function') { resolve(false); return; }
      document.getElementById('confirm-title').textContent = title;
      document.getElementById('confirm-msg').textContent = message;
      document.getElementById('confirm-ok').textContent = ok;
      dlg.returnValue = '';
      dlg.addEventListener('close', () => resolve(dlg.returnValue === 'ok'), { once: true });
      dlg.showModal();
      dlg.querySelector('button[value="cancel"]').focus();
    });
  }

  // ---------- gallery ----------

  const state = {
    items: [],
    cards: new Map(), // id -> { el, frame, data }
    query: '',
    project: '', // full project path filter; '' = all
    loaded: false,
  };
  let $;

  function init() {
    $ = (id) => document.getElementById(id);

    const themeBtn = $('theme');
    const syncThemeBtn = () => {
      themeBtn.querySelector('.ico').textContent = THEME_ICON[theme];
      const label = `Theme: ${theme}`;
      themeBtn.setAttribute('aria-label', label);
      themeBtn.title = label;
    };
    syncThemeBtn();
    themeBtn.addEventListener('click', () => {
      theme = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
      saveTheme(theme);
      applyTheme(theme);
      syncThemeBtn();
      for (const c of state.cards.values()) postTheme(c.frame);
    });

    const q = $('q');
    q.addEventListener('input', () => { state.query = q.value.trim().toLowerCase(); applyFilters(); });
    document.addEventListener('keydown', (e) => {
      if (e.key === '/' && !isTyping(e.target)) { e.preventDefault(); q.focus(); q.select(); }
      else if (e.key === 'Escape' && document.activeElement === q && q.value) { q.value = ''; state.query = ''; applyFilters(); }
    });

    thumbObserver = 'ResizeObserver' in window ? new ResizeObserver((entries) => {
      for (const en of entries) {
        const w = en.contentRect.width;
        if (w > 0) en.target.style.setProperty('--s', String(w / 1280));
      }
    }) : null;

    window.addEventListener('message', onFrameMessage);
    setInterval(refreshTimes, 60_000);
    load();
    subscribe();
  }

  function isTyping(t) {
    return t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName));
  }

  let loading = null;
  let reloadQueued = false;
  async function load() {
    if (loading) { reloadQueued = true; return loading; }
    loading = (async () => {
      try {
        const data = await api('/api/artifacts');
        state.items = Array.isArray(data.artifacts) ? data.artifacts : [];
        state.loaded = true;
        $('error').hidden = true;
        render();
      } catch (err) {
        const box = $('error');
        box.textContent = err.status === 401
          ? `This browser isn't signed in to the viewer. ${SIGN_IN_MSG}`
          : `Couldn't load artifacts: ${err.message}`;
        box.hidden = false;
      }
    })();
    await loading;
    loading = null;
    if (reloadQueued) { reloadQueued = false; load(); }
  }

  let refreshTimer = 0;
  function scheduleLoad() {
    clearTimeout(refreshTimer);
    refreshTimer = setTimeout(load, 150);
  }

  function subscribe() {
    if (!('EventSource' in window) || !viewerKey) return;
    const es = new EventSource(`/api/events?k=${viewerKey}`);
    let hadError = false;
    es.addEventListener('version', scheduleLoad);
    es.addEventListener('deleted', scheduleLoad);
    es.addEventListener('open', () => { if (hadError) { hadError = false; scheduleLoad(); } });
    es.addEventListener('error', () => { hadError = true; });
  }

  // ---- rendering ----

  let thumbObserver = null;

  function render() {
    const seen = new Set();
    for (const a of state.items) {
      if (!a || typeof a.id !== 'string') continue;
      seen.add(a.id);
      const c = state.cards.get(a.id);
      if (!c) state.cards.set(a.id, buildCard(a));
      else updateCard(c, a);
    }
    for (const [id, c] of state.cards) {
      if (!seen.has(id)) {
        if (thumbObserver) thumbObserver.unobserve(c.thumb);
        c.el.remove();
        state.cards.delete(id);
      }
    }
    const pinned = [], rest = [];
    for (const a of state.items) {
      const c = state.cards.get(a && a.id);
      if (c) (a.pinned ? pinned : rest).push(c.el);
    }
    place($('pinned-grid'), pinned);
    place($('all-grid'), rest);
    renderFilters();
    applyFilters();
  }

  // Put elements in order while moving as few DOM nodes as possible —
  // moving an <iframe> reloads it.
  function place(container, els) {
    els.forEach((node, i) => {
      const at = container.children[i];
      if (at !== node) container.insertBefore(node, at || null);
    });
    while (container.children.length > els.length) container.lastChild.remove();
  }

  function frameSrc(a) {
    return typeof a.page_base === 'string' ? `${a.page_base}/latest/index.html` : 'about:blank';
  }

  function buildCard(a) {
    const card = el('article', 'card');
    const thumb = el('div', 'thumb');
    const frame = document.createElement('iframe');
    frame.setAttribute('loading', 'lazy');
    frame.setAttribute('sandbox', 'allow-scripts');
    frame.setAttribute('aria-hidden', 'true');
    frame.setAttribute('tabindex', '-1');
    frame.setAttribute('title', '');
    frame.src = frameSrc(a);
    frame.addEventListener('load', () => postTheme(frame));
    const pinMark = el('span', 'pin-mark', '📌');
    pinMark.setAttribute('aria-hidden', 'true');
    thumb.append(frame, pinMark);
    if (thumbObserver) thumbObserver.observe(thumb);

    const body = el('div', 'card-body');
    const head = el('div', 'card-head');
    const glyphSlot = el('span', 'glyph-slot');
    const title = el('h3', 'card-title');
    const link = document.createElement('a');
    title.appendChild(link);
    const menuBtn = el('button', 'btn icon ghost card-menu-btn', '⋯');
    menuBtn.type = 'button';
    menuBtn.setAttribute('aria-haspopup', 'menu');
    menuBtn.setAttribute('aria-expanded', 'false');
    head.append(glyphSlot, title, menuBtn);

    const desc = el('p', 'card-desc');
    const meta = el('div', 'card-meta');
    const time = el('time');
    const ver = el('span', 'ver');
    const chips = el('span', 'chips');
    meta.append(time, el('span', 'dot'), ver, chips);
    body.append(head, desc, meta);
    card.append(thumb, body);

    const c = { el: card, frame, thumb, pinMark, glyphSlot, link, desc, time, ver, chips, menuBtn, data: null };
    menuBtn.addEventListener('click', (e) => { e.preventDefault(); e.stopPropagation(); openMenu(c); });
    updateCard(c, a);
    return c;
  }

  function updateCard(c, a) {
    const prev = c.data;
    c.data = a;
    if (prev && prev.version !== a.version) c.frame.src = frameSrc(a); // reload thumbnail
    c.el.dataset.id = a.id;
    c.glyphSlot.replaceChildren(glyphEl(a));
    c.link.textContent = a.title || 'Untitled';
    c.link.href = typeof a.url === 'string' && a.url.startsWith('/') ? a.url : `/a/${encodeURIComponent(a.id)}`;
    c.desc.textContent = a.description || '';
    c.time.dateTime = a.updated || '';
    c.time.textContent = relTime(a.updated);
    c.time.title = `Updated ${fullTime(a.updated)}`;
    c.ver.textContent = `v${a.version || 1}`;
    c.pinMark.hidden = !a.pinned;
    c.menuBtn.setAttribute('aria-label', `Actions for ${a.title || 'artifact'}`);
    const chipEls = [];
    if (a.kind && a.kind !== 'page') chipEls.push(el('span', 'chip kind', a.kind));
    const proj = baseName(a.project);
    if (proj) {
      const chip = el('span', 'chip', proj);
      chip.title = a.project;
      chipEls.push(chip);
    }
    c.chips.replaceChildren(...chipEls);
  }

  function refreshTimes() {
    for (const c of state.cards.values()) if (c.data) c.time.textContent = relTime(c.data.updated);
  }

  function renderFilters() {
    const nav = $('filters');
    const projects = new Map(); // path -> count
    for (const a of state.items) if (a && a.project) projects.set(a.project, (projects.get(a.project) || 0) + 1);
    if (state.project && !projects.has(state.project)) state.project = '';
    if (projects.size < 2) { nav.hidden = true; nav.replaceChildren(); return; }
    const mk = (label, value, title) => {
      const b = el('button', 'filter', label);
      b.type = 'button';
      b.setAttribute('aria-pressed', String(state.project === value));
      if (title) b.title = title;
      b.addEventListener('click', () => {
        state.project = value;
        for (const x of nav.children) x.setAttribute('aria-pressed', String(x === b));
        applyFilters();
      });
      return b;
    };
    const sorted = [...projects.keys()].sort((x, y) => baseName(x).localeCompare(baseName(y)));
    nav.replaceChildren(mk('All projects', ''), ...sorted.map((p) => mk(baseName(p) || p, p, p)));
    nav.hidden = false;
  }

  function matches(a) {
    if (state.project && a.project !== state.project) return false;
    if (!state.query) return true;
    const hay = [a.title, a.description, a.kind, a.icon, baseName(a.project)].join(' ').toLowerCase();
    return state.query.split(/\s+/).every((w) => hay.includes(w));
  }

  function applyFilters() {
    let pinnedVis = 0, restVis = 0;
    for (const c of state.cards.values()) {
      const show = matches(c.data);
      c.el.hidden = !show;
      if (show) c.data.pinned ? pinnedVis++ : restVis++;
    }
    const total = state.items.length;
    $('pinned-section').hidden = pinnedVis === 0;
    $('all-section').hidden = restVis === 0;
    $('pinned-count').textContent = String(pinnedVis);
    $('all-count').textContent = String(restVis);
    $('all-title').firstChild.textContent = pinnedVis ? 'Everything else ' : 'All ';
    $('empty').hidden = !(state.loaded && total === 0);
    $('no-match').hidden = !(total > 0 && pinnedVis + restVis === 0);
  }

  // ---- card menu ----

  let menu = null;
  function closeMenu(focusBack) {
    if (!menu) return;
    const { node, card } = menu;
    node.remove();
    card.menuBtn.setAttribute('aria-expanded', 'false');
    document.removeEventListener('pointerdown', onOutside, true);
    window.removeEventListener('resize', onWinChange);
    window.removeEventListener('scroll', onWinChange, true);
    menu = null;
    if (focusBack) card.menuBtn.focus();
  }
  function onOutside(e) { if (menu && !menu.node.contains(e.target) && e.target !== menu.card.menuBtn) closeMenu(false); }
  function onWinChange() { closeMenu(false); }

  function openMenu(c) {
    const wasOpen = menu && menu.card === c;
    closeMenu(false);
    if (wasOpen) return;
    const a = c.data;
    const node = el('div', 'menu');
    node.setAttribute('role', 'menu');
    node.setAttribute('aria-label', `Actions for ${a.title || 'artifact'}`);
    const item = (ico, label, fn, cls) => {
      const b = el('button', cls || '');
      b.type = 'button';
      b.setAttribute('role', 'menuitem');
      const i = el('span', 'ico', ico);
      i.setAttribute('aria-hidden', 'true');
      b.append(i, el('span', '', label));
      b.addEventListener('click', () => { closeMenu(false); fn(); });
      return b;
    };
    node.append(
      item(a.pinned ? '⊘' : '📌', a.pinned ? 'Unpin' : 'Pin', () => togglePin(c)),
      item('↗', 'Open page in new tab', () => window.open(frameSrc(a), '_blank', 'noopener')),
      el('hr'),
      item('🗑', 'Delete…', () => deleteCard(c), 'danger'),
    );
    node.addEventListener('keydown', (e) => {
      const items = [...node.querySelectorAll('[role="menuitem"]')];
      const i = items.indexOf(document.activeElement);
      if (e.key === 'Escape') { e.preventDefault(); closeMenu(true); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); items[(i + 1) % items.length].focus(); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); items[(i - 1 + items.length) % items.length].focus(); }
      else if (e.key === 'Tab') closeMenu(false);
    });
    document.body.appendChild(node);
    const r = c.menuBtn.getBoundingClientRect();
    const w = node.offsetWidth, h = node.offsetHeight;
    let left = Math.min(r.right - w, window.innerWidth - w - 8);
    left = Math.max(8, left);
    let top = r.bottom + 4;
    if (top + h > window.innerHeight - 8) top = Math.max(8, r.top - h - 4);
    node.style.left = `${left}px`;
    node.style.top = `${top}px`;
    c.menuBtn.setAttribute('aria-expanded', 'true');
    menu = { node, card: c };
    document.addEventListener('pointerdown', onOutside, true);
    window.addEventListener('resize', onWinChange);
    window.addEventListener('scroll', onWinChange, true);
    node.querySelector('[role="menuitem"]').focus();
  }

  async function togglePin(c) {
    const a = c.data;
    const next = !a.pinned;
    try {
      await api(`/api/artifacts/${encodeURIComponent(a.id)}/pin`, { method: 'POST', body: { pinned: next } });
      a.pinned = next;
      render();
      toast(next ? 'Pinned' : 'Unpinned');
    } catch (err) {
      toast(`Couldn't ${next ? 'pin' : 'unpin'}: ${err.message}`, true);
    }
  }

  async function deleteCard(c) {
    const a = c.data;
    const ok = await confirmDialog({
      title: 'Delete this artifact?',
      message: `“${a.title || 'Untitled'}” and all of its versions, comments and data will be permanently deleted.`,
    });
    if (!ok) { c.menuBtn.focus(); return; }
    try {
      await api(`/api/artifacts/${encodeURIComponent(a.id)}`, { method: 'DELETE' });
      state.items = state.items.filter((x) => x.id !== a.id);
      render();
      toast('Deleted');
    } catch (err) {
      toast(`Couldn't delete: ${err.message}`, true);
    }
  }

  // ---- thumbnail bridge ----
  // Thumbnails run the artifact runtime too. Give them the chrome theme and
  // answer any runtime calls with a clear error instead of a 15s timeout.

  function postTheme(frame) {
    try { frame.contentWindow.postMessage({ tada: 1, event: 'theme', theme }, '*'); } catch { /* frame gone */ }
  }

  function onFrameMessage(ev) {
    const d = ev.data;
    if (!d || typeof d !== 'object' || d.tada !== 1 || !ev.source) return;
    let frame = null;
    let id = '';
    for (const [k, c] of state.cards) if (c.frame.contentWindow === ev.source) { frame = c.frame; id = k; break; }
    if (!frame) return;
    if (d.hello) { postTheme(frame); return; }
    if (typeof d.call !== 'number') return;
    const send = (r) => { try { ev.source.postMessage({ tada: 1, reply: d.call, ...r }, '*'); } catch { /* ignore */ } };
    previewCall(id, d.method, d.args || {}).then(
      (result) => send({ ok: true, result }),
      (err) => send({ ok: false, error: err.message || 'Not available in the gallery preview' }),
    );
  }

  // previewCall lets thumbnails read their data so they look like the real
  // page. Writes are ignored: a preview must never change anything.
  async function previewCall(id, method, args) {
    switch (method) {
      case 'theme.get': return effectiveTheme();
      case 'storage.set': case 'storage.remove': case 'storage.clear': return true;
      case 'db.get': case 'db.list': case 'db.query': {
        const op = method.slice(3);
        const body = { op, collection: String(args.collection || '') };
        if (args.id != null) body.doc_id = String(args.id);
        if (args.where !== undefined) body.query = args.where;
        if (args.limit != null) body.limit = Number(args.limit);
        if (typeof args.order_by === 'string') body.order_by = args.order_by;
        if (args.desc) body.desc = true;
        const res = await api(`/api/artifacts/${encodeURIComponent(id)}/db`, { method: 'POST', body });
        return op === 'get' ? (res.doc ?? null) : (res.docs || []);
      }
      default: throw new Error('Not available in the gallery preview');
    }
  }

  function effectiveTheme() {
    if (theme === 'light' || theme === 'dark') return theme;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }

  // Wait for the sign-in so the first API call and the live updates have
  // the key.
  const start = () => { signedIn.then(init); };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true });
  else start();
})();

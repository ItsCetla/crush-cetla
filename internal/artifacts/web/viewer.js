// Ta-Da artifacts — viewer page (served at /a/{id}, optional ?v=N).
// Loaded synchronously in <head> so the saved theme applies before first paint.
(() => {
  'use strict';

  // ---------- shared helpers (kept in sync with gallery.js) ----------

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
  // Ta-Da opens the viewer with #k=<key>. Keep the key in this origin's
  // storage (which, unlike cookies, is separate per port) and take it out of
  // the address bar. Every API call sends it.
  const KEY_STORE = 'tada:key';
  let viewerKey = '';
  {
    const m = /^#k=([0-9a-f]{64})$/.exec(location.hash);
    if (m) {
      viewerKey = m[1];
      try { localStorage.setItem(KEY_STORE, viewerKey); } catch { /* storage unavailable */ }
      try { history.replaceState(null, '', location.pathname + location.search); } catch { /* ignore */ }
    } else {
      try { viewerKey = localStorage.getItem(KEY_STORE) || ''; } catch { /* storage unavailable */ }
    }
  }
  // A tab already open on this page only sees a hash change, so sign in
  // and start over with the new key.
  window.addEventListener('hashchange', () => {
    const m = /^#k=([0-9a-f]{64})$/.exec(location.hash);
    if (!m) return;
    try { localStorage.setItem(KEY_STORE, m[1]); } catch { /* storage unavailable */ }
    location.replace(location.pathname + location.search);
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
  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function toast(msg, opts = {}) {
    const box = document.getElementById('toasts');
    if (!box) return;
    const t = el('div', 'toast' + (opts.error ? ' err' : ''));
    t.append(el('span', '', msg));
    if (opts.action) {
      const b = el('button', 'btn ghost', opts.action.label);
      b.type = 'button';
      b.style.cssText = 'height:24px;padding:0 8px;margin-left:8px;pointer-events:auto;color:var(--accent)';
      b.addEventListener('click', () => { opts.action.run(); dismiss(); });
      t.append(b);
      t.style.pointerEvents = 'auto';
    }
    box.appendChild(t);
    let gone = false;
    function dismiss() {
      if (gone) return;
      gone = true;
      t.classList.add('out');
      setTimeout(() => t.remove(), 350);
    }
    setTimeout(dismiss, opts.action ? 8000 : opts.error ? 5000 : 2800);
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

  function effectiveTheme() {
    if (theme === 'light' || theme === 'dark') return theme;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  }

  // ---------- viewer state ----------

  const ID = decodeURIComponent(location.pathname.split('/')[2] || '');
  const EID = encodeURIComponent(ID);
  const qv = parseInt(new URLSearchParams(location.search).get('v') || '', 10);

  const state = {
    artifact: null,
    versions: [], // ascending
    latest: 0,
    current: Number.isFinite(qv) && qv > 0 ? qv : 0, // 0 = latest, number not known yet
    follow: !(Number.isFinite(qv) && qv > 0),
    comments: [],
    showResolved: false,
    replyOpen: '',
    replyDraft: '',
    deleted: false,
    pageBase: '',
  };

  let $;
  let frame = null;

  function init() {
    $ = (id) => document.getElementById(id);
    if (!ID) { showNotice('Artifact not found', 'This link is missing an artifact id.'); return; }

    window.addEventListener('message', onMessage);
    createFrame();
    wireChrome();
    wireComments();
    loadArtifact();
    loadComments();
    subscribe();
    setInterval(() => { renderPicker(); renderThreads(); }, 60_000);
  }

  // ---------- frame ----------

  // Pages live under a per-artifact key the API hands out, so the frame
  // waits for loadArtifact before it loads anything.
  function pageURL(ver) {
    return `${state.pageBase}/${ver || 'latest'}/index.html`;
  }

  function createFrame() {
    frame = document.createElement('iframe');
    frame.setAttribute('sandbox', 'allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads');
    frame.setAttribute('allow', 'fullscreen; clipboard-write');
    frame.setAttribute('title', 'Artifact');
    frame.addEventListener('load', () => {
      $('loading').hidden = true;
      sendTheme();
    });
    $('frame-wrap').appendChild(frame);
  }

  function navigateFrame(ver) {
    if (!frame || !state.pageBase) return;
    $('loading').hidden = false;
    frame.src = pageURL(ver);
    $('open').href = pageURL(ver);
  }

  function post(msg) {
    try { frame && frame.contentWindow && frame.contentWindow.postMessage(msg, '*'); } catch { /* frame gone */ }
  }
  function sendTheme() { post({ tada: 1, event: 'theme', theme }); }

  // Select a version. n = 0 or n = latest means "follow latest".
  function selectVersion(n) {
    const followLatest = !n || n === state.latest;
    state.follow = followLatest;
    const target = followLatest ? state.latest : n;
    const url = new URL(location.href);
    if (followLatest) url.searchParams.delete('v');
    else url.searchParams.set('v', String(n));
    try { history.replaceState(null, '', url.pathname + url.search + url.hash); } catch { /* ignore */ }
    if (target !== state.current) {
      state.current = target;
      navigateFrame(target);
    }
    renderPicker();
  }

  // ---------- artifact metadata ----------

  async function loadArtifact() {
    try {
      const data = await api(`/api/artifacts/${EID}`);
      const a = data.artifact;
      if (!a || typeof a !== 'object') throw new Error('Malformed response');
      state.artifact = a;
      if (typeof a.page_base === 'string') state.pageBase = a.page_base;
      state.versions = Array.isArray(a.versions) ? a.versions.filter((v) => v && Number.isFinite(v.n)) : [];
      const maxN = state.versions.reduce((m, v) => Math.max(m, v.n), 0);
      state.latest = Math.max(Number(a.version) || 0, maxN);
      if (state.follow) {
        // Pin the frame to a concrete number so later reloads are explicit.
        if (state.current === 0) { state.current = state.latest; $('open').href = pageURL(state.current); }
        else if (state.current !== state.latest) { state.current = state.latest; navigateFrame(state.current); }
      } else if (state.current === state.latest) {
        selectVersion(0);
      }
      if (!frame.getAttribute('src')) navigateFrame(state.current);
      renderHeader();
      renderPicker();
      return true;
    } catch (err) {
      if (err.status === 404) {
        showNotice('Artifact not found', 'It may have been deleted, or the link is wrong.');
        markDeleted();
      } else if (err.status === 401) {
        showNotice('Sign in needed', `This browser isn't signed in to the viewer. ${SIGN_IN_MSG}`);
      } else {
        toast(`Couldn't load artifact details: ${err.message}`, { error: true });
      }
      return false;
    }
  }

  function renderHeader() {
    const a = state.artifact;
    if (!a) return;
    const title = a.title || 'Untitled';
    document.title = `${title} · Ta-Da`;
    $('title').textContent = title;
    $('title').title = title;
    const sub = $('subtitle');
    sub.textContent = a.description || '';
    sub.title = a.description || '';
    sub.hidden = !a.description;
    $('glyph').replaceChildren(glyphEl(a));
    renderPin();
  }

  function renderPicker() {
    const sel = $('version');
    if (!sel || !state.versions.length) return;
    const opts = [...state.versions].sort((x, y) => y.n - x.n).map((v) => {
      const bits = [`v${v.n}`];
      const t = relTime(v.time);
      if (t) bits.push(t);
      if (v.label) bits.push(String(v.label));
      if (v.n === state.latest) bits.push('latest');
      const o = el('option', '', bits.join(' · '));
      o.value = String(v.n);
      if (v.time) o.title = fullTime(v.time);
      return o;
    });
    sel.replaceChildren(...opts);
    sel.value = String(state.current || state.latest);
    sel.disabled = state.deleted || opts.length < 2;
  }

  function renderPin() {
    const a = state.artifact;
    const btn = $('pin');
    const pinned = !!(a && a.pinned);
    btn.setAttribute('aria-pressed', String(pinned));
    const label = pinned ? 'Unpin' : 'Pin';
    btn.title = label;
    btn.setAttribute('aria-label', label);
    btn.querySelector('.label').textContent = label;
  }

  function renderThemeBtn() {
    const btn = $('theme');
    const label = `Theme: ${theme}`;
    btn.querySelector('.ico').textContent = THEME_ICON[theme];
    btn.querySelector('.label').textContent = label;
    btn.title = label;
    btn.setAttribute('aria-label', label);
  }

  function showNotice(title, msg) {
    $('notice-title').textContent = title;
    $('notice-msg').textContent = msg;
    $('notice').hidden = false;
    $('loading').hidden = true;
  }

  function markDeleted() {
    state.deleted = true;
    for (const id of ['version', 'pin', 'delete', 'open', 'comments-btn']) {
      const b = $(id);
      if (!b) continue;
      if (b.tagName === 'A') { b.removeAttribute('href'); b.setAttribute('aria-disabled', 'true'); }
      else b.disabled = true;
    }
    setComments(false);
    if (frame) { frame.remove(); frame = null; }
  }

  // ---------- chrome actions ----------

  function wireChrome() {
    renderThemeBtn();
    $('theme').addEventListener('click', () => {
      theme = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
      saveTheme(theme);
      applyTheme(theme);
      renderThemeBtn();
      sendTheme();
    });

    $('version').addEventListener('change', (e) => selectVersion(parseInt(e.target.value, 10)));

    $('pin').addEventListener('click', async () => {
      const a = state.artifact;
      if (!a) return;
      const next = !a.pinned;
      try {
        await api(`/api/artifacts/${EID}/pin`, { method: 'POST', body: { pinned: next } });
        a.pinned = next;
        renderPin();
        toast(next ? 'Pinned' : 'Unpinned');
      } catch (err) {
        toast(`Couldn't ${next ? 'pin' : 'unpin'}: ${err.message}`, { error: true });
      }
    });

    $('delete').addEventListener('click', async () => {
      const name = (state.artifact && state.artifact.title) || 'this artifact';
      const ok = await confirmDialog({
        title: 'Delete this artifact?',
        message: `“${name}” and all of its versions, comments and data will be permanently deleted.`,
      });
      if (!ok) return;
      try {
        await api(`/api/artifacts/${EID}`, { method: 'DELETE' });
        location.href = '/';
      } catch (err) {
        toast(`Couldn't delete: ${err.message}`, { error: true });
      }
    });

    $('comments-btn').addEventListener('click', () => setComments(!$('comments').classList.contains('open')));
    $('comments-close').addEventListener('click', () => { setComments(false); $('comments-btn').focus(); });

    // Phone menu: the same action buttons, shown as a dropdown.
    const more = $('more');
    const actions = $('actions');
    const closeActions = () => { actions.classList.remove('open'); more.setAttribute('aria-expanded', 'false'); };
    more.addEventListener('click', (e) => {
      e.stopPropagation();
      const open = !actions.classList.contains('open');
      actions.classList.toggle('open', open);
      more.setAttribute('aria-expanded', String(open));
      if (open) { const f = actions.querySelector('button:not(:disabled), a[href]'); if (f) f.focus(); }
    });
    actions.addEventListener('click', (e) => { if (e.target.closest('.btn')) closeActions(); });
    document.addEventListener('pointerdown', (e) => {
      if (actions.classList.contains('open') && !actions.contains(e.target) && e.target !== more) closeActions();
    });
    document.addEventListener('keydown', (e) => {
      if (e.key !== 'Escape') return;
      if (actions.classList.contains('open')) { closeActions(); more.focus(); }
      else if ($('comments').classList.contains('open') && !$('confirm').open) { setComments(false); $('comments-btn').focus(); }
    });
  }

  function setComments(open) {
    const panel = $('comments');
    panel.classList.toggle('open', open);
    $('comments-btn').setAttribute('aria-expanded', String(open));
    $('comments-btn').setAttribute('aria-pressed', String(open));
    if (open) {
      loadComments();
      if (window.matchMedia('(min-width: 761px)').matches) setTimeout(() => $('c-body').focus(), 50);
    }
  }

  // ---------- comments ----------

  function wireComments() {
    $('show-resolved').addEventListener('change', (e) => { state.showResolved = e.target.checked; renderThreads(); });
    const form = $('composer');
    const body = $('c-body');
    body.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); form.requestSubmit(); }
    });
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const text = body.value.trim();
      if (!text) return;
      const payload = { body: text, to_agent: $('c-agent').checked };
      const anchor = $('c-anchor').value.trim();
      if (anchor) payload.anchor = anchor;
      const send = $('c-send');
      send.disabled = true;
      try {
        await api(`/api/artifacts/${EID}/comments`, { method: 'POST', body: payload });
        body.value = '';
        $('c-anchor').value = '';
        await loadComments();
        const list = $('threads');
        list.scrollTop = list.scrollHeight;
      } catch (err) {
        toast(`Couldn't post comment: ${err.message}`, { error: true });
      } finally {
        send.disabled = false;
      }
    });
  }

  let commentsLoading = null;
  async function loadComments() {
    if (state.deleted) return;
    if (commentsLoading) return commentsLoading;
    commentsLoading = (async () => {
      try {
        const data = await api(`/api/artifacts/${EID}/comments`);
        state.comments = Array.isArray(data.comments) ? data.comments.filter((c) => c && typeof c.id === 'string') : [];
        renderThreads();
      } catch (err) {
        if (err.status !== 404 && err.status !== 401) toast(`Couldn't load comments: ${err.message}`, { error: true });
      }
    })();
    try { await commentsLoading; } finally { commentsLoading = null; }
  }

  function threads() {
    const byId = new Map(state.comments.map((c) => [c.id, c]));
    const rootOf = (c) => {
      let cur = c;
      for (let i = 0; i < 50 && cur.parent && byId.has(cur.parent); i++) cur = byId.get(cur.parent);
      return cur;
    };
    const map = new Map();
    for (const c of state.comments) {
      const root = rootOf(c);
      if (!map.has(root.id)) map.set(root.id, { root, replies: [] });
      if (root !== c) map.get(root.id).replies.push(c);
    }
    const ts = (c) => Date.parse(c.created) || 0;
    const list = [...map.values()];
    for (const t of list) t.replies.sort((x, y) => ts(x) - ts(y));
    list.sort((x, y) => ts(x.root) - ts(y.root));
    return list;
  }

  function renderThreads() {
    if (!$) return;
    const all = threads();
    const open = all.filter((t) => !t.root.resolved).length;
    const badge = $('badge');
    badge.textContent = String(open);
    badge.hidden = open === 0;
    $('comments-btn').setAttribute('aria-label', open ? `Comments (${open} open)` : 'Comments');

    const box = $('threads');
    const visible = all.filter((t) => state.showResolved || !t.root.resolved);
    if (!visible.length) {
      const hidden = all.length - visible.length;
      const msg = all.length === 0
        ? 'No comments yet. Leave feedback here — comments sent to the agent show up when it checks this artifact.'
        : `No open threads. ${hidden} resolved thread${hidden === 1 ? '' : 's'} hidden.`;
      box.replaceChildren(el('p', 'none', msg));
      return;
    }
    // Keep an in-progress reply when re-rendering.
    const active = document.activeElement;
    const replyFocused = active && active.classList && active.classList.contains('reply-text');
    box.replaceChildren(...visible.map(renderThread));
    if (replyFocused) {
      const ta = box.querySelector('.reply-text');
      if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); }
    }
  }

  function renderComment(c) {
    const wrap = el('div', 'comment');
    const head = el('div', 'comment-head');
    const isAgent = c.author === 'agent';
    head.append(el('span', 'who' + (isAgent ? ' agent' : ''), isAgent ? 'Agent' : 'You'));
    const time = el('time', '', relTime(c.created));
    time.dateTime = c.created || '';
    time.title = fullTime(c.created);
    head.append(time);
    if (!isAgent && c.to_agent) {
      const s = el('span', 'sent', '→ agent');
      s.title = 'Sent to the agent';
      head.append(s);
    }
    wrap.append(head);
    if (c.anchor) wrap.append(el('blockquote', 'anchor', String(c.anchor)));
    wrap.append(el('p', 'comment-body', String(c.body || '')));
    return wrap;
  }

  function renderThread(t) {
    const node = el('article', 'thread' + (t.root.resolved ? ' resolved' : ''));
    node.append(renderComment(t.root));
    if (t.replies.length) {
      const reps = el('div', 'replies');
      reps.append(...t.replies.map(renderComment));
      node.append(reps);
    }
    const acts = el('div', 'thread-actions');
    const replyBtn = el('button', 'btn ghost', 'Reply');
    replyBtn.type = 'button';
    replyBtn.setAttribute('aria-expanded', String(state.replyOpen === t.root.id));
    replyBtn.addEventListener('click', () => {
      state.replyOpen = state.replyOpen === t.root.id ? '' : t.root.id;
      state.replyDraft = '';
      renderThreads();
      const ta = $('threads').querySelector('.reply-text');
      if (ta) ta.focus();
    });
    const resolveBtn = el('button', 'btn ghost', t.root.resolved ? 'Reopen' : 'Resolve');
    resolveBtn.type = 'button';
    resolveBtn.addEventListener('click', async () => {
      resolveBtn.disabled = true;
      try {
        await api(`/api/artifacts/${EID}/comments/${encodeURIComponent(t.root.id)}/resolve`, { method: 'POST', body: { resolved: !t.root.resolved } });
        t.root.resolved = !t.root.resolved;
        if (t.root.resolved && state.replyOpen === t.root.id) state.replyOpen = '';
        renderThreads();
      } catch (err) {
        resolveBtn.disabled = false;
        toast(`Couldn't update thread: ${err.message}`, { error: true });
      }
    });
    acts.append(replyBtn, resolveBtn);
    if (t.root.resolved) acts.append(el('span', 'resolved-tag', 'Resolved'));
    node.append(acts);
    if (state.replyOpen === t.root.id) node.append(replyForm(t.root));
    return node;
  }

  function replyForm(root) {
    const form = el('form', 'reply-form');
    const ta = el('textarea', 'reply-text');
    ta.rows = 2;
    ta.placeholder = 'Reply…';
    ta.setAttribute('aria-label', 'Reply');
    ta.value = state.replyDraft;
    ta.addEventListener('input', () => { state.replyDraft = ta.value; });
    ta.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); form.requestSubmit(); }
      else if (e.key === 'Escape') { e.stopPropagation(); state.replyOpen = ''; renderThreads(); }
    });
    const row = el('div', 'composer-row');
    const lab = el('label', 'toggle');
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = true;
    lab.append(cb, document.createTextNode(' Send to agent'));
    const cancel = el('button', 'btn ghost', 'Cancel');
    cancel.type = 'button';
    cancel.style.marginLeft = 'auto';
    cancel.addEventListener('click', () => { state.replyOpen = ''; renderThreads(); });
    const send = el('button', 'btn primary', 'Reply');
    send.type = 'submit';
    send.style.marginLeft = '0';
    row.append(lab, cancel, send);
    form.append(ta, row);
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const text = ta.value.trim();
      if (!text) return;
      send.disabled = true;
      try {
        await api(`/api/artifacts/${EID}/comments`, { method: 'POST', body: { body: text, parent: root.id, to_agent: cb.checked } });
        state.replyOpen = '';
        state.replyDraft = '';
        await loadComments();
      } catch (err) {
        send.disabled = false;
        toast(`Couldn't post reply: ${err.message}`, { error: true });
      }
    });
    return form;
  }

  // ---------- live updates ----------

  function subscribe() {
    if (!('EventSource' in window) || !viewerKey) return;
    const es = new EventSource(`/api/events?id=${EID}&k=${viewerKey}`);
    let hadError = false;
    const parse = (ev) => {
      try {
        const d = JSON.parse(ev.data);
        return d && typeof d === 'object' && (d.id === undefined || d.id === ID) ? d : null;
      } catch { return null; }
    };
    es.addEventListener('version', async (ev) => {
      const d = parse(ev);
      if (!d || state.deleted) return;
      const v = Number(d.version) || 0;
      const wasFollowing = state.follow;
      await loadArtifact();
      const n = Math.max(v, state.latest);
      if (wasFollowing) {
        state.follow = true;
        if (state.current !== n) { state.current = n; navigateFrame(n); }
        renderPicker();
        toast(`Updated to v${n}`);
      } else {
        toast(`v${n} available`, { action: { label: 'View', run: () => selectVersion(0) } });
      }
    });
    es.addEventListener('comment', (ev) => { if (parse(ev)) loadComments(); });
    es.addEventListener('db', (ev) => {
      const d = parse(ev);
      if (d && typeof d.collection === 'string') post({ tada: 1, event: 'db', collection: d.collection });
    });
    es.addEventListener('deleted', (ev) => {
      if (!parse(ev)) return;
      showNotice('This artifact was deleted', 'It no longer exists in your Ta-Da artifacts.');
      markDeleted();
      es.close();
    });
    es.addEventListener('open', () => {
      if (!hadError) return;
      hadError = false;
      loadArtifact().then((ok) => {
        if (ok && state.follow && state.current !== state.latest) { state.current = state.latest; navigateFrame(state.latest); }
      });
      loadComments();
    });
    es.addEventListener('error', () => { hadError = true; });
  }

  // ---------- bridge: artifact runtime <-> viewer ----------

  function onMessage(ev) {
    if (!frame || ev.source !== frame.contentWindow) return;
    const d = ev.data;
    if (!d || typeof d !== 'object' || d.tada !== 1) return;
    if (d.hello) { sendTheme(); return; }
    if (typeof d.call !== 'number' || typeof d.method !== 'string') return;
    const src = ev.source;
    const reply = (msg) => { try { src.postMessage(Object.assign({ tada: 1, reply: d.call }, msg), '*'); } catch { /* frame gone */ } };
    Promise.resolve()
      .then(() => handleCall(d.method, d.args && typeof d.args === 'object' ? d.args : {}))
      .then(
        (result) => reply({ ok: true, result: result === undefined ? null : result }),
        (err) => reply({ ok: false, error: String((err && err.message) || err || 'Error') }),
      );
  }

  function needStr(v, name) {
    if (typeof v !== 'string' || !v) throw new Error(`${name} is required`);
    return v;
  }

  async function db(op, args, needId) {
    const body = { op, collection: needStr(args.collection, 'collection') };
    if (needId) body.doc_id = needStr(args.id, 'id');
    else if (args.id != null && args.id !== '') body.doc_id = String(args.id);
    if (args.data !== undefined) body.data = args.data;
    if (args.where !== undefined) body.query = args.where;
    if (args.limit != null && Number.isFinite(Number(args.limit))) body.limit = Number(args.limit);
    if (typeof args.order_by === 'string' && args.order_by) body.order_by = args.order_by;
    if (args.desc) body.desc = true;
    return api(`/api/artifacts/${EID}/db`, { method: 'POST', body });
  }

  async function storage(body) {
    await api(`/api/artifacts/${EID}/storage`, { method: 'POST', body });
    return true;
  }

  async function handleCall(method, args) {
    if (state.deleted) throw new Error('This artifact was deleted');
    switch (method) {
      case 'db.get': return (await db('get', args, true)).doc ?? null;
      case 'db.set': return (await db('set', args, false)).doc;
      case 'db.update': return (await db('update', args, true)).doc;
      case 'db.delete': await db('delete', args, true); return true;
      case 'db.list': return (await db('list', args, false)).docs || [];
      case 'db.query': return (await db('query', args, false)).docs || [];
      case 'storage.set':
        return storage({ key: needStr(args.key, 'key'), value: args.value == null ? null : String(args.value) });
      case 'storage.remove': return storage({ key: needStr(args.key, 'key'), value: null });
      case 'storage.clear': return storage({ clear: true });
      case 'save': return saveFile(args);
      case 'comment': {
        const body = typeof args.body === 'string' ? args.body.trim() : '';
        if (!body) throw new Error('comment body is required');
        const payload = { body, to_agent: true };
        if (typeof args.anchor === 'string' && args.anchor.trim()) payload.anchor = args.anchor.trim();
        const res = await api(`/api/artifacts/${EID}/comments`, { method: 'POST', body: payload });
        loadComments();
        return res.comment;
      }
      case 'theme.get': return effectiveTheme();
      default: throw new Error(`Unknown method: ${method}`);
    }
  }

  function saveFile({ filename, data, mime, base64 }) {
    const type = typeof mime === 'string' && mime ? mime : '';
    let blob;
    if (data instanceof Blob) {
      blob = type && type !== data.type ? new Blob([data], { type }) : data;
    } else if (data instanceof ArrayBuffer || ArrayBuffer.isView(data)) {
      blob = new Blob([data], { type: type || 'application/octet-stream' });
    } else if (typeof data === 'string') {
      if (base64) {
        const bin = atob(data.replace(/^data:[^,]*,/, '').replace(/\s+/g, ''));
        const bytes = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
        blob = new Blob([bytes], { type: type || 'application/octet-stream' });
      } else {
        blob = new Blob([data], { type: type || 'text/plain;charset=utf-8' });
      }
    } else {
      throw new Error('save: data must be a string, Blob or ArrayBuffer');
    }
    const name = String(filename || '')
      .replace(/[\\/:*?"<>|\u0000-\u001f]+/g, '_')
      .replace(/^\.+/, '')
      .slice(0, 200) || 'download';
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.rel = 'noopener';
    a.hidden = true;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
    toast(`Downloading ${name}`);
    return true;
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();

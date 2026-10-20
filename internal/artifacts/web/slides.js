/* Ta-Da slides runtime (artifact kind "slides").
   Top-level <section> elements become 1280×720 slides. Keys: ←/→, ↑/↓,
   PageUp/PageDown, Space (Shift+Space back), Home/End, o = overview,
   f = fullscreen, Esc = close overview. Click the left third to go back,
   elsewhere to advance; swipe on touch. #3 in the URL opens slide 3.
   Fires a "tada:slide" event on document with detail {index, number, total}.
   Defines no globals and never throws. */
(function () {
  'use strict';
  try {
    if (document.documentElement.hasAttribute('data-tada-slides')) return;
    document.documentElement.setAttribute('data-tada-slides', '');
  } catch (e) { return; }

  var W = 1280;
  var H = 720;
  var slides = [];
  var index = 0;
  var counter = null;
  var counterTimer = 0;
  var overview = null;
  var suppressClickUntil = 0;
  var started = false;

  function warn(e) {
    try { if (window.console && console.warn) console.warn('[tada slides]', e && e.message ? e.message : e); } catch (x) { /* ignore */ }
  }
  function guard(fn) {
    return function () {
      try { return fn.apply(this, arguments); } catch (e) { warn(e); }
    };
  }

  function findSlides() {
    var body = document.body;
    if (!body) return [];
    var direct = [];
    for (var i = 0; i < body.children.length; i++) {
      if (body.children[i].tagName === 'SECTION') direct.push(body.children[i]);
    }
    if (direct.length) return direct;
    // Allow one wrapper, e.g. <main> or <div class="deck">, around the sections.
    for (var j = 0; j < body.children.length; j++) {
      var kids = body.children[j].children;
      var found = [];
      for (var k = 0; k < kids.length; k++) if (kids[k].tagName === 'SECTION') found.push(kids[k]);
      if (found.length) return found;
    }
    return [];
  }

  function fit() {
    var s = Math.min(window.innerWidth / W, window.innerHeight / H);
    if (!isFinite(s) || s <= 0) s = 1;
    document.documentElement.style.setProperty('--tada-scale', String(s));
  }

  function setUp() {
    var found = findSlides();
    var same = found.length === slides.length && found.every(function (s, i) { return s === slides[i]; });
    if (same) return;
    slides.forEach(function (s) {
      if (found.indexOf(s) < 0) s.classList.remove('tada-slide', 'tada-current', 'tada-last');
    });
    var current = slides[index];
    slides = found;
    var root = document.documentElement;
    if (!slides.length) {
      root.classList.remove('tada-deck');
      if (counter) counter.hidden = true;
      return;
    }
    root.classList.add('tada-deck');
    slides.forEach(function (s, i) {
      s.classList.add('tada-slide');
      s.classList.toggle('tada-last', i === slides.length - 1);
    });
    ensureChrome();
    var keep = current ? slides.indexOf(current) : -1;
    var start = keep >= 0 ? keep : started ? Math.min(index, slides.length - 1) : fromHash();
    started = true;
    show(start < 0 ? 0 : start, false);
  }

  function fromHash() {
    var m = /^#(\d+)$/.exec(location.hash || '');
    if (!m) return 0;
    var n = parseInt(m[1], 10) - 1;
    return Math.max(0, Math.min(slides.length - 1, n));
  }

  function writeHash(n) {
    var h = '#' + (n + 1);
    if (location.hash === h) return;
    try {
      history.replaceState(history.state, '', h);
      return;
    } catch (e) { /* opaque-origin sandboxes may refuse */ }
    try { location.replace(h); } catch (e) { /* ignore */ }
  }

  function ensureChrome() {
    if (!counter) {
      counter = document.createElement('div');
      counter.className = 'tada-counter';
      counter.setAttribute('aria-live', 'polite');
      counter.setAttribute('aria-atomic', 'true');
      document.body.appendChild(counter);
    } else if (!counter.isConnected) {
      document.body.appendChild(counter);
    }
    counter.hidden = false;
  }

  function flashCounter() {
    if (!counter) return;
    counter.textContent = (index + 1) + ' / ' + slides.length;
    counter.classList.add('tada-show');
    clearTimeout(counterTimer);
    counterTimer = setTimeout(function () { counter.classList.remove('tada-show'); }, 1600);
  }

  function show(n, announce) {
    if (!slides.length) return;
    n = Math.max(0, Math.min(slides.length - 1, n));
    var changed = n !== index || !slides[n].classList.contains('tada-current');
    index = n;
    slides.forEach(function (s, i) {
      var on = i === n;
      s.classList.toggle('tada-current', on);
      if (on) s.removeAttribute('aria-hidden');
      else s.setAttribute('aria-hidden', 'true');
    });
    // Don't leave focus inside a hidden slide.
    var act = document.activeElement;
    if (act && act !== document.body && !slides[n].contains(act) && slides.some(function (s) { return s.contains(act); })) {
      try { act.blur(); } catch (e) { /* ignore */ }
    }
    writeHash(n);
    flashCounter();
    if (changed || announce) {
      try {
        document.dispatchEvent(new CustomEvent('tada:slide', { detail: { index: n, number: n + 1, total: slides.length } }));
      } catch (e) { /* ignore */ }
    }
  }

  function next() { show(index + 1); }
  function prev() { show(index - 1); }

  // ---------- overview ----------

  function openOverview() {
    if (!slides.length) return;
    if (!overview) {
      overview = document.createElement('div');
      overview.className = 'tada-overview';
      overview.setAttribute('role', 'dialog');
      overview.setAttribute('aria-label', 'Slide overview');
      overview.addEventListener('click', guard(function (e) {
        e.stopPropagation();
        var tile = e.target.closest ? e.target.closest('.tada-tile') : null;
        if (tile) {
          closeOverview();
          show(parseInt(tile.getAttribute('data-n'), 10) || 0);
        } else if (e.target === overview) {
          closeOverview();
        }
      }));
      overview.addEventListener('keydown', guard(onOverviewKey));
      document.body.appendChild(overview);
    }
    var tiles = slides.map(function (s, i) {
      var tile = document.createElement('button');
      tile.type = 'button';
      tile.className = 'tada-tile' + (i === index ? ' tada-current-tile' : '');
      tile.setAttribute('data-n', String(i));
      tile.setAttribute('aria-label', 'Slide ' + (i + 1));
      tile.appendChild(cloneSlide(s));
      var num = document.createElement('span');
      num.className = 'tada-tile-num';
      num.textContent = String(i + 1);
      tile.appendChild(num);
      return tile;
    });
    overview.replaceChildren.apply(overview, tiles);
    overview.hidden = false;
    sizeTiles();
    var cur = tiles[index];
    if (cur) {
      try { cur.focus({ preventScroll: true }); } catch (e) { cur.focus(); }
      try { cur.scrollIntoView({ block: 'center' }); } catch (e) { /* ignore */ }
    }
  }

  function cloneSlide(s) {
    var c = s.cloneNode(true);
    c.classList.remove('tada-slide', 'tada-current', 'tada-last');
    c.classList.add('tada-clone');
    c.removeAttribute('aria-hidden');
    c.removeAttribute('id');
    c.setAttribute('aria-hidden', 'true');
    c.setAttribute('inert', '');
    var drop = c.querySelectorAll('script, iframe, video, audio, object, embed');
    for (var i = 0; i < drop.length; i++) drop[i].remove();
    var all = c.querySelectorAll('[id], [name], [autofocus]');
    for (var j = 0; j < all.length; j++) {
      all[j].removeAttribute('id');
      all[j].removeAttribute('name');
      all[j].removeAttribute('autofocus');
    }
    // Canvas pixels don't survive cloneNode; copy them where possible.
    var src = s.querySelectorAll('canvas');
    var dst = c.querySelectorAll('canvas');
    for (var k = 0; k < src.length && k < dst.length; k++) {
      try { dst[k].getContext('2d').drawImage(src[k], 0, 0); } catch (e) { /* tainted or webgl */ }
    }
    return c;
  }

  function sizeTiles() {
    if (!overview || overview.hidden) return;
    var t = overview.querySelector('.tada-tile');
    if (!t) return;
    var w = t.getBoundingClientRect().width;
    if (w > 0) overview.style.setProperty('--tada-tile-scale', String(w / W));
  }

  function closeOverview() {
    if (!overview || overview.hidden) return;
    overview.hidden = true;
    overview.replaceChildren();
  }

  function overviewOpen() { return !!overview && !overview.hidden; }

  function onOverviewKey(e) {
    var tiles = overview.querySelectorAll('.tada-tile');
    var at = Array.prototype.indexOf.call(tiles, document.activeElement);
    if (at < 0) return;
    var cols = Math.max(1, Math.round(overview.clientWidth / (tiles[0].getBoundingClientRect().width + 18)));
    var to = -1;
    if (e.key === 'ArrowRight') to = at + 1;
    else if (e.key === 'ArrowLeft') to = at - 1;
    else if (e.key === 'ArrowDown') to = at + cols;
    else if (e.key === 'ArrowUp') to = at - cols;
    else if (e.key === 'Home') to = 0;
    else if (e.key === 'End') to = tiles.length - 1;
    if (to < 0 && e.key !== 'ArrowLeft' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    e.stopPropagation();
    to = Math.max(0, Math.min(tiles.length - 1, to));
    tiles[to].focus();
  }

  // ---------- fullscreen ----------

  function toggleFullscreen() {
    try {
      var d = document;
      if (d.fullscreenElement || d.webkitFullscreenElement) {
        (d.exitFullscreen || d.webkitExitFullscreen).call(d);
        return;
      }
      var el = d.documentElement;
      var req = el.requestFullscreen || el.webkitRequestFullscreen;
      if (!req) return;
      var p = req.call(el);
      if (p && p.catch) p.catch(function (e) { warn(e); });
    } catch (e) { warn(e); }
  }

  // ---------- input ----------

  function isEditable(t) {
    if (!t || t.nodeType !== 1) return false;
    if (t.isContentEditable) return true;
    var tag = t.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
  }
  function isInteractive(t) {
    if (!t || !t.closest) return false;
    return !!t.closest('a[href], button, input, select, textarea, label, summary, details, video, audio, iframe, [contenteditable], [tabindex], [role="button"], [role="link"], [onclick], [data-tada-noclick]');
  }

  function onKey(e) {
    if (!slides.length || e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey) return;
    if (isEditable(e.target)) return;
    if (overviewOpen()) {
      if (e.key === 'Escape' || e.key === 'o' || e.key === 'O') { e.preventDefault(); closeOverview(); }
      return;
    }
    var k = e.key;
    var handled = true;
    if (k === 'ArrowRight' || k === 'ArrowDown' || k === 'PageDown' || (k === ' ' && !e.shiftKey) || k === 'Spacebar') next();
    else if (k === 'ArrowLeft' || k === 'ArrowUp' || k === 'PageUp' || (k === ' ' && e.shiftKey)) prev();
    else if (k === 'Home') show(0);
    else if (k === 'End') show(slides.length - 1);
    else if (k === 'o' || k === 'O') openOverview();
    else if (k === 'f' || k === 'F') toggleFullscreen();
    else handled = false;
    if (handled) e.preventDefault();
  }

  function onClick(e) {
    if (!slides.length || overviewOpen() || e.defaultPrevented || e.button !== 0) return;
    if (Date.now() < suppressClickUntil) return;
    if (e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
    if (isInteractive(e.target)) return;
    if (counter && counter.contains(e.target)) return;
    try {
      var sel = window.getSelection && window.getSelection();
      if (sel && !sel.isCollapsed && String(sel).trim()) return;
    } catch (x) { /* ignore */ }
    if (e.clientX < window.innerWidth / 3) prev();
    else next();
  }

  var touch = null;
  function onTouchStart(e) {
    if (!slides.length || overviewOpen() || e.touches.length !== 1) { touch = null; return; }
    touch = { x: e.touches[0].clientX, y: e.touches[0].clientY, t: Date.now() };
  }
  function onTouchEnd(e) {
    if (!touch || !e.changedTouches.length) return;
    var dx = e.changedTouches[0].clientX - touch.x;
    var dy = e.changedTouches[0].clientY - touch.y;
    var fast = Date.now() - touch.t < 800;
    touch = null;
    if (fast && Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy) * 1.5) {
      suppressClickUntil = Date.now() + 500;
      if (dx < 0) next();
      else prev();
    }
  }

  // ---------- boot ----------

  var rescanTimer = 0;
  function scheduleRescan() {
    clearTimeout(rescanTimer);
    rescanTimer = setTimeout(guard(setUp), 30);
  }

  function boot() {
    fit();
    setUp();
    window.addEventListener('resize', guard(function () { fit(); sizeTiles(); }));
    document.addEventListener('keydown', guard(onKey));
    document.addEventListener('click', guard(onClick));
    document.addEventListener('touchstart', guard(onTouchStart), { passive: true });
    document.addEventListener('touchend', guard(onTouchEnd), { passive: true });
    window.addEventListener('hashchange', guard(function () {
      if (/^#\d+$/.test(location.hash) && slides.length) show(fromHash());
    }));
    window.addEventListener('load', guard(setUp));
    try {
      // Decks built by script: pick up sections added after load.
      new MutationObserver(function (records) {
        for (var i = 0; i < records.length; i++) {
          var r = records[i];
          if (r.target === document.body || (r.target.parentNode === document.body)) { scheduleRescan(); return; }
        }
      }).observe(document.body, { childList: true, subtree: true });
    } catch (e) { warn(e); }
  }

  function start() {
    if (!document.body) { setTimeout(start, 10); return; }
    guard(boot)();
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true });
  else start();
})();

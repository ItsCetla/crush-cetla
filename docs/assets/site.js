/* Crush (cetla build) site: nav menu, download links for this computer and the latest release,
   screenshot lightbox, copy buttons. Everything here is progressive enhancement; the page works
   without it. The only network request is to the GitHub releases API. */
(function () {
  'use strict';

  var API_URL = 'https://api.github.com/repos/ItsCetla/crush-cetla/releases/latest';
  var RELEASES_PAGE = 'https://github.com/ItsCetla/crush-cetla/releases';
  var CACHE_KEY = 'crush-cetla:latest-release:v1';
  var CACHE_OK_MS = 60 * 60 * 1000;    // a found release
  var CACHE_FAIL_MS = 10 * 60 * 1000;  // 404 (nothing published yet) or 403 (rate limited)

  // goreleaser names: crush_<version>_<Os>_<arch>.<ext>
  var ASSETS = {
    'win-x64-setup':   /_Windows_x86_64_Setup\.exe$/i,
    'win-arm64-setup': /_Windows_arm64_Setup\.exe$/i,
    'win-x64':     /_Windows_x86_64\.zip$/i,
    'win-arm64':   /_Windows_arm64\.zip$/i,
    'mac-arm64':   /_Darwin_arm64\.tar\.gz$/i,
    'mac-x64':     /_Darwin_x86_64\.tar\.gz$/i,
    'linux-x64':   /_Linux_x86_64\.tar\.gz$/i,
    'linux-arm64': /_Linux_arm64\.tar\.gz$/i,
    'sums':        /^SHA256SUMS\.txt$/i
  };

  var $$ = function (sel, scope) { return Array.prototype.slice.call((scope || document).querySelectorAll(sel)); };

  /* ---------- header: scrolled state + mobile menu ---------- */
  function initHeader() {
    var header = document.querySelector('.site-header');
    if (!header) return;
    var onScroll = function () { header.classList.toggle('is-scrolled', window.scrollY > 4); };
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });

    var toggle = header.querySelector('.nav-toggle');
    var nav = document.getElementById('primary-nav');
    if (!toggle || !nav) return;
    var setOpen = function (open) {
      header.classList.toggle('nav-open', open);
      toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
      toggle.setAttribute('aria-label', open ? 'Close menu' : 'Menu');
    };
    toggle.addEventListener('click', function () { setOpen(!header.classList.contains('nav-open')); });
    nav.addEventListener('click', function (e) { if (e.target.closest('a')) setOpen(false); });
    document.addEventListener('click', function (e) {
      if (header.classList.contains('nav-open') && !header.contains(e.target)) setOpen(false);
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && header.classList.contains('nav-open')) { setOpen(false); toggle.focus(); }
    });
  }

  /* ---------- which computer is this? ---------- */
  // 'windows' | 'mac' | 'linux' | '' (phones, tablets, ChromeOS and anything unknown).
  function detectOS() {
    var nav = window.navigator || {};
    var hint = nav.userAgentData && typeof nav.userAgentData.platform === 'string' ? nav.userAgentData.platform : '';
    var ua = String(nav.userAgent || '');
    var plat = String(nav.platform || '');
    var all = (hint + ' ' + plat + ' ' + ua).toLowerCase();
    if (/android|iphone|ipad|ipod|cros|chrome os/.test(all)) return '';
    if (/win/.test(hint.toLowerCase() || plat.toLowerCase()) || /windows/.test(ua.toLowerCase())) return 'windows';
    if (/mac/.test(all)) {
      if (nav.maxTouchPoints > 1) return ''; // an iPad asking for the desktop site
      return 'mac';
    }
    if (/linux|x11|bsd/.test(all)) return 'linux';
    return '';
  }

  // Browsers on Apple silicon still say "Intel" in their user agent. Chromium can tell the real
  // CPU when asked; elsewhere this stays unknown and both builds are offered as equals.
  function detectArch() {
    var data = window.navigator && window.navigator.userAgentData;
    if (!data || typeof data.getHighEntropyValues !== 'function') return Promise.resolve('');
    return data.getHighEntropyValues(['architecture']).then(function (v) {
      var a = v && typeof v.architecture === 'string' ? v.architecture.toLowerCase() : '';
      return a === 'arm' ? 'arm64' : a === 'x86' ? 'x64' : '';
    }, function () { return ''; });
  }

  /* ---------- latest release ---------- */
  function readCache() {
    try {
      var raw = window.localStorage.getItem(CACHE_KEY);
      if (!raw) return null;
      var entry = JSON.parse(raw);
      if (!entry || typeof entry.until !== 'number' || entry.until < Date.now()) return null;
      return entry;
    } catch (e) { return null; }
  }
  function writeCache(entry) {
    try { window.localStorage.setItem(CACHE_KEY, JSON.stringify(entry)); } catch (e) { /* private mode etc. */ }
  }

  function pickAsset(assets, re) {
    for (var i = 0; i < assets.length; i++) {
      var a = assets[i];
      if (a && typeof a.name === 'string' && re.test(a.name) && a.browser_download_url) {
        return { name: a.name, url: a.browser_download_url, size: a.size };
      }
    }
    return null;
  }

  function summarize(release) {
    var assets = Array.isArray(release.assets) ? release.assets : [];
    var files = {};
    Object.keys(ASSETS).forEach(function (kind) {
      var a = pickAsset(assets, ASSETS[kind]);
      if (a) files[kind] = a;
    });
    // Releases made before the installer existed: point installer buttons at the zip.
    if (!files['win-x64-setup'] && files['win-x64']) files['win-x64-setup'] = files['win-x64'];
    if (!files['win-arm64-setup'] && files['win-arm64']) files['win-arm64-setup'] = files['win-arm64'];
    var tag = typeof release.tag_name === 'string' ? release.tag_name : '';
    return {
      version: tag.replace(/^v/, ''),
      tag: tag,
      page: typeof release.html_url === 'string' ? release.html_url : RELEASES_PAGE,
      date: typeof release.published_at === 'string' ? release.published_at : '',
      files: files
    };
  }

  function loadRelease() {
    var cached = readCache();
    if (cached) return Promise.resolve(cached.info || null);
    if (!window.fetch) return Promise.resolve(null);
    var controller = typeof AbortController === 'function' ? new AbortController() : null;
    var timer = controller ? setTimeout(function () { controller.abort(); }, 8000) : 0;
    return fetch(API_URL, {
      headers: { Accept: 'application/vnd.github+json' },
      signal: controller ? controller.signal : undefined
    }).then(function (res) {
      if (timer) clearTimeout(timer);
      if (!res.ok) { writeCache({ until: Date.now() + CACHE_FAIL_MS, info: null }); return null; }
      return res.json().then(function (data) {
        var info = summarize(data);
        writeCache({ until: Date.now() + CACHE_OK_MS, info: info });
        return info;
      });
    }).catch(function () {
      if (timer) clearTimeout(timer);
      return null;
    });
  }

  function formatSize(bytes) {
    if (typeof bytes !== 'number' || !(bytes > 0)) return '';
    if (bytes >= 1e9) return (bytes / 1e9).toFixed(2) + ' GB';
    if (bytes >= 1e6) return (bytes / 1e6).toFixed(1) + ' MB';
    return Math.round(bytes / 1e3) + ' kB';
  }
  function formatDate(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return '';
    try { return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }); }
    catch (e) { return iso.slice(0, 10); }
  }

  function applyRelease(info) {
    if (!info || !info.version) {
      $$('[data-release-none]').forEach(function (el) { el.hidden = false; });
      return;
    }
    $$('[data-release-version]').forEach(function (el) { el.textContent = info.version; });
    Object.keys(info.files).forEach(function (kind) {
      var asset = info.files[kind];
      $$('[data-dl="' + kind + '"]').forEach(function (a) { a.href = asset.url; });
      $$('[data-dl-file="' + kind + '"]').forEach(function (el) { el.textContent = asset.name; });
      var size = formatSize(asset.size);
      $$('[data-dl-size="' + kind + '"]').forEach(function (el) { el.textContent = size; el.hidden = !size; });
    });
    $$('[data-release-date]').forEach(function (el) { el.textContent = formatDate(info.date); });
    $$('[data-release-page]').forEach(function (a) { a.href = info.page; });
    $$('[data-release-info]').forEach(function (el) { el.hidden = false; });
    $$('[data-release-none]').forEach(function (el) { el.hidden = true; });
  }

  function initRelease() {
    if (!document.querySelector('[data-dl], [data-release-version]')) return;
    loadRelease().then(applyRelease).catch(function () { /* never surface errors */ });
  }

  /* ---------- this computer ---------- */
  function initPlatform(os) {
    if (!os) return;
    document.documentElement.classList.add('os-' + os);
    var card = document.querySelector('[data-os-card="' + os + '"]');
    if (card) card.classList.add('is-mine');
    var kind = os === 'windows' ? 'win-x64-setup' : os === 'linux' ? 'linux-x64' : '';
    var hero = $$('[data-hero-dl]');
    var label = { windows: 'Download for Windows', mac: 'Download for macOS', linux: 'Download for Linux' }[os];
    hero.forEach(function (a) {
      a.querySelector('[data-hero-label]').textContent = label;
      if (kind) a.setAttribute('data-dl', kind);
    });
    if (os === 'mac') {
      detectArch().then(function (arch) {
        var k = arch === 'x64' ? 'mac-x64' : 'mac-arm64';
        hero.forEach(function (a) { a.setAttribute('data-dl', k); });
        initRelease();
      });
    } else {
      initRelease();
    }
  }

  /* ---------- lightbox ---------- */
  function initLightbox() {
    var links = $$('a[data-lightbox]');
    if (!links.length || typeof HTMLDialogElement !== 'function') return;
    var dialog = document.createElement('dialog');
    dialog.className = 'lightbox';
    var img = document.createElement('img');
    img.alt = '';
    dialog.appendChild(img);
    document.body.appendChild(dialog);
    links.forEach(function (link) {
      link.addEventListener('click', function (e) {
        e.preventDefault();
        img.src = link.href;
        img.alt = link.getAttribute('data-lightbox') || '';
        dialog.showModal();
      });
    });
    dialog.addEventListener('click', function () { dialog.close(); });
  }

  /* ---------- copy buttons ---------- */
  function initCopy() {
    if (!navigator.clipboard) return;
    $$('[data-copy]').forEach(function (btn) {
      var target = document.getElementById(btn.getAttribute('data-copy'));
      if (!target) return;
      btn.hidden = false;
      btn.addEventListener('click', function () {
        navigator.clipboard.writeText(target.textContent.trim()).then(function () {
          btn.classList.add('is-done');
          btn.textContent = 'Copied';
          setTimeout(function () { btn.classList.remove('is-done'); btn.textContent = 'Copy'; }, 1600);
        }, function () { /* ignore */ });
      });
    });
  }

  function init() {
    initHeader();
    var os = detectOS();
    if (os) initPlatform(os); else initRelease();
    initLightbox();
    initCopy();
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();

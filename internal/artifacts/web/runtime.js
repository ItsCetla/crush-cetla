/* Ta-Da artifact runtime. Inlined into every artifact page after
   window.__TADA__ = {id, version, title, kind, storage}. Defines window.tada
   and nothing else. It must never throw: every step is guarded. */
(function () {
  'use strict';
  try {
    if (window.tada && window.tada.__runtime === 1) return;
  } catch (e) { /* keep going */ }

  var cfg = {};
  try { if (window.__TADA__ && typeof window.__TADA__ === 'object') cfg = window.__TADA__; } catch (e) { /* ignore */ }

  var parentWin = null;
  var inViewer = false;
  try { parentWin = window.parent; inViewer = !!parentWin && parentWin !== window; } catch (e) { inViewer = false; }

  var TIMEOUT = 15000;
  var seq = 0;
  var pending = {};
  var dbSubs = [];
  var themeSubs = [];

  function err(msg) { return new Error(msg); }
  function noViewer(what) { return err('Open this artifact in the Ta-Da viewer to use ' + what); }
  function warn(what, e) {
    try { if (window.console && console.warn) console.warn('[tada] ' + what + ':', e && e.message ? e.message : e); } catch (x) { /* ignore */ }
  }
  function safe(fn, arg, arg2) {
    try { fn(arg, arg2); } catch (e) { warn('listener failed', e); }
  }

  function post(msg) { parentWin.postMessage(msg, '*'); }

  function call(method, args, what) {
    return new Promise(function (resolve, reject) {
      if (!inViewer) { reject(noViewer(what)); return; }
      var id = ++seq;
      var timer = setTimeout(function () {
        if (!pending[id]) return;
        delete pending[id];
        reject(err('The Ta-Da viewer did not respond (' + method + ')'));
      }, TIMEOUT);
      pending[id] = { resolve: resolve, reject: reject, timer: timer };
      try {
        post({ tada: 1, call: id, method: method, args: args || {} });
      } catch (e) {
        clearTimeout(timer);
        delete pending[id];
        reject(err('Could not send ' + method + ' to the Ta-Da viewer: ' + (e && e.message ? e.message : e)));
      }
    });
  }

  // ---------- artifact info ----------

  var artifact = {};
  try {
    artifact = {
      id: typeof cfg.id === 'string' ? cfg.id : '',
      version: typeof cfg.version === 'number' ? cfg.version : Number(cfg.version) || 0,
      title: typeof cfg.title === 'string' ? cfg.title : '',
      kind: typeof cfg.kind === 'string' ? cfg.kind : ''
    };
    if (Object.freeze) Object.freeze(artifact);
  } catch (e) { /* ignore */ }

  // ---------- db ----------

  function needCollection(c) {
    if (typeof c !== 'string' || !c) throw err('tada.db: collection must be a non-empty string');
    return c;
  }
  function needId(id) {
    if (id === undefined || id === null || id === '') throw err('tada.db: document id is required');
    return String(id);
  }
  function limitOf(opts) {
    if (typeof opts === 'number') return opts;
    if (opts && typeof opts === 'object' && opts.limit != null) return Number(opts.limit);
    return undefined;
  }
  // withOpts copies limit, order_by (or orderBy) and desc from opts.
  function withOpts(a, opts) {
    var l = limitOf(opts);
    if (l != null && isFinite(l)) a.limit = l;
    if (opts && typeof opts === 'object') {
      var ob = opts.order_by != null ? opts.order_by : opts.orderBy;
      if (typeof ob === 'string' && ob) a.order_by = ob;
      if (opts.desc) a.desc = true;
    }
    return a;
  }
  function dbCall(method, build) {
    try {
      var args = build();
      return call(method, args, 'tada.db');
    } catch (e) {
      return Promise.reject(e);
    }
  }

  var db = {
    get: function (collection, id) {
      return dbCall('db.get', function () { return { collection: needCollection(collection), id: needId(id) }; });
    },
    // set(collection, data) creates a document with a server-chosen id;
    // set(collection, id, data) creates or replaces that document.
    set: function (collection, idOrData, data) {
      return dbCall('db.set', function () {
        var a = { collection: needCollection(collection) };
        if (data === undefined && idOrData !== null && typeof idOrData === 'object') {
          a.data = idOrData;
        } else {
          a.id = needId(idOrData);
          a.data = data === undefined ? {} : data;
        }
        return a;
      });
    },
    update: function (collection, id, data) {
      return dbCall('db.update', function () {
        return { collection: needCollection(collection), id: needId(id), data: data === undefined ? {} : data };
      });
    },
    'delete': function (collection, id) {
      return dbCall('db.delete', function () { return { collection: needCollection(collection), id: needId(id) }; });
    },
    list: function (collection, opts) {
      return dbCall('db.list', function () {
        return withOpts({ collection: needCollection(collection) }, opts);
      });
    },
    query: function (collection, where, opts) {
      return dbCall('db.query', function () {
        // Also accept query(collection, {where, limit, ...}).
        if (opts === undefined && where && typeof where === 'object' && where.where && typeof where.where === 'object') {
          opts = where;
          where = where.where;
        }
        return withOpts({ collection: needCollection(collection), where: where || {} }, opts);
      });
    },
    // subscribe(collection, cb): cb({collection}) runs whenever that
    // collection changes (from this page, another tab, or the agent).
    // Use "*" for every collection. Returns an unsubscribe function.
    subscribe: function (collection, cb) {
      if (typeof cb !== 'function') return function () {};
      var sub = { collection: typeof collection === 'string' && collection ? collection : '*', cb: cb };
      dbSubs.push(sub);
      return function () {
        var i = dbSubs.indexOf(sub);
        if (i >= 0) dbSubs.splice(i, 1);
      };
    }
  };
  try { if (Object.freeze) Object.freeze(db); } catch (e) { /* ignore */ }

  // ---------- storage ----------

  function makeStorage(seed, persist) {
    var map = Object.create(null);
    var order = [];
    try {
      if (seed && typeof seed === 'object') {
        Object.keys(seed).forEach(function (k) {
          if (seed[k] === null || seed[k] === undefined) return;
          map[k] = String(seed[k]);
          order.push(k);
        });
      }
    } catch (e) { /* ignore */ }

    function sync(args, method) {
      if (!persist || !inViewer) return;
      call(method, args, 'storage').then(null, function (e) { warn('storage not saved', e); });
    }

    var api = {
      getItem: function (k) {
        k = String(k);
        return Object.prototype.hasOwnProperty.call(map, k) ? map[k] : null;
      },
      setItem: function (k, v) {
        k = String(k);
        v = String(v);
        if (!Object.prototype.hasOwnProperty.call(map, k)) order.push(k);
        else if (map[k] === v) return;
        map[k] = v;
        sync({ key: k, value: v }, 'storage.set');
      },
      removeItem: function (k) {
        k = String(k);
        if (!Object.prototype.hasOwnProperty.call(map, k)) return;
        delete map[k];
        var i = order.indexOf(k);
        if (i >= 0) order.splice(i, 1);
        sync({ key: k }, 'storage.remove');
      },
      clear: function () {
        if (!order.length) return;
        map = Object.create(null);
        order = [];
        sync({}, 'storage.clear');
      },
      key: function (i) {
        i = Number(i);
        return i >= 0 && i < order.length ? order[i] : null;
      }
    };
    var proto = {};
    Object.keys(api).forEach(function (k) {
      Object.defineProperty(proto, k, { value: api[k], enumerable: false, writable: false, configurable: false });
    });
    Object.defineProperty(proto, 'length', { get: function () { return order.length; }, enumerable: false, configurable: false });
    var target = Object.create(proto);

    if (typeof Proxy !== 'function') return target;
    try {
      return new Proxy(target, {
        get: function (t, p) {
          if (typeof p !== 'string' || p in proto) return Reflect.get(t, p);
          return Object.prototype.hasOwnProperty.call(map, p) ? map[p] : undefined;
        },
        set: function (t, p, v) {
          if (typeof p !== 'string' || p in proto) return true;
          api.setItem(p, v);
          return true;
        },
        has: function (t, p) {
          return (typeof p === 'string' && Object.prototype.hasOwnProperty.call(map, p)) || p in proto;
        },
        deleteProperty: function (t, p) {
          if (typeof p === 'string') api.removeItem(p);
          return true;
        },
        ownKeys: function () { return order.slice(); },
        getOwnPropertyDescriptor: function (t, p) {
          if (typeof p === 'string' && Object.prototype.hasOwnProperty.call(map, p)) {
            return { value: map[p], writable: true, enumerable: true, configurable: true };
          }
          return undefined;
        },
        defineProperty: function () { return false; }
      });
    } catch (e) {
      return target;
    }
  }

  var storage = makeStorage(cfg.storage, true);

  function brokenStorage(name) {
    try {
      var s = window[name];
      if (!s) return true;
      s.getItem('__tada_probe__');
      return false;
    } catch (e) { return true; }
  }
  function shim(obj, fallbackProto, name, value) {
    var desc = { configurable: true, enumerable: true, get: function () { return value; } };
    try { Object.defineProperty(obj, name, desc); } catch (e) { /* try the prototype */ }
    try {
      if (obj[name] === value) return true;
    } catch (e) { /* still broken */ }
    try {
      if (fallbackProto) Object.defineProperty(fallbackProto, name, desc);
      return obj[name] === value;
    } catch (e) { return false; }
  }
  var winProto = null;
  try { winProto = typeof Window !== 'undefined' ? Window.prototype : null; } catch (e) { /* ignore */ }

  try {
    // Sandboxed artifacts have an opaque origin, so the real Web Storage
    // throws. localStorage becomes tada.storage (persisted by the viewer);
    // sessionStorage becomes a per-page in-memory store.
    if (brokenStorage('localStorage')) shim(window, winProto, 'localStorage', storage);
    if (brokenStorage('sessionStorage')) shim(window, winProto, 'sessionStorage', makeStorage(null, false));
  } catch (e) { warn('storage shim failed', e); }

  try {
    var cookieBroken = false;
    try { void document.cookie; } catch (e) { cookieBroken = true; }
    if (cookieBroken) {
      var jar = Object.create(null);
      var cookieDesc = {
        configurable: true,
        enumerable: true,
        get: function () {
          return Object.keys(jar).map(function (k) { return k ? k + '=' + jar[k] : jar[k]; }).join('; ');
        },
        set: function (v) {
          var parts = String(v).split(';');
          var first = parts.shift() || '';
          var eq = first.indexOf('=');
          var name = (eq >= 0 ? first.slice(0, eq) : '').trim();
          var val = (eq >= 0 ? first.slice(eq + 1) : first).trim();
          var expired = false;
          parts.forEach(function (attr) {
            var a = attr.split('=');
            var key = (a[0] || '').trim().toLowerCase();
            var av = a.slice(1).join('=').trim();
            if (key === 'max-age' && Number(av) <= 0) expired = true;
            if (key === 'expires') {
              var t = Date.parse(av);
              if (isFinite(t) && t <= Date.now()) expired = true;
            }
          });
          if (expired) delete jar[name];
          else jar[name] = val;
        }
      };
      try { Object.defineProperty(document, 'cookie', cookieDesc); } catch (e) {
        try { Object.defineProperty(Document.prototype, 'cookie', cookieDesc); } catch (x) { /* give up */ }
      }
    }
  } catch (e) { warn('cookie shim failed', e); }

  // ---------- theme ----------

  var pref = 'system';
  var setByRuntime = false;
  var lastNotified = '';
  var mq = null;
  try { mq = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null; } catch (e) { mq = null; }

  function effective() {
    if (pref === 'light' || pref === 'dark') return pref;
    try { return mq && mq.matches ? 'dark' : 'light'; } catch (e) { return 'light'; }
  }
  function notifyTheme() {
    var cur = effective();
    var key = cur + '|' + pref;
    if (key === lastNotified) return;
    lastNotified = key;
    themeSubs.slice().forEach(function (cb) { safe(cb, cur, pref); });
    try {
      window.dispatchEvent(new CustomEvent('tadatheme', { detail: { theme: cur, preference: pref } }));
    } catch (e) { /* ignore */ }
  }
  function setTheme(t) {
    if (t !== 'light' && t !== 'dark') t = 'system';
    pref = t;
    try {
      var root = document.documentElement;
      if (t === 'system') {
        // Only undo what we did; an author's own data-theme stays.
        if (setByRuntime) { root.removeAttribute('data-theme'); setByRuntime = false; }
      } else {
        root.setAttribute('data-theme', t);
        setByRuntime = true;
      }
    } catch (e) { warn('theme', e); }
    notifyTheme();
  }
  try {
    if (mq) {
      var onScheme = function () { if (pref === 'system') notifyTheme(); };
      if (mq.addEventListener) mq.addEventListener('change', onScheme);
      else if (mq.addListener) mq.addListener(onScheme);
    }
  } catch (e) { /* ignore */ }
  lastNotified = effective() + '|' + pref;

  var theme = {};
  try {
    Object.defineProperty(theme, 'current', { enumerable: true, get: effective });
    Object.defineProperty(theme, 'preference', { enumerable: true, get: function () { return pref; } });
    theme.onChange = function (cb) {
      if (typeof cb !== 'function') return function () {};
      themeSubs.push(cb);
      return function () {
        var i = themeSubs.indexOf(cb);
        if (i >= 0) themeSubs.splice(i, 1);
      };
    };
    if (Object.freeze) Object.freeze(theme);
  } catch (e) { /* ignore */ }

  // ---------- save / comment ----------

  function localDownload(filename, data, mime, base64) {
    var type = mime || 'application/octet-stream';
    var blob;
    if (typeof Blob !== 'undefined' && data instanceof Blob) blob = data;
    else if (typeof data === 'string' && base64) {
      var bin = atob(data.replace(/^data:[^,]*,/, '').replace(/\s+/g, ''));
      var bytes = new Uint8Array(bin.length);
      for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      blob = new Blob([bytes], { type: type });
    } else blob = new Blob([data], { type: mime || 'text/plain;charset=utf-8' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = String(filename || 'download');
    a.style.display = 'none';
    (document.body || document.documentElement).appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 60000);
    return true;
  }

  function save(filename, data, mime, opts) {
    try {
      if (data === undefined || data === null) return Promise.reject(err('tada.save: data is required'));
      var base64 = !!(opts && typeof opts === 'object' && opts.base64);
      var m = typeof mime === 'string' ? mime : '';
      if (!inViewer) {
        // Opened directly in a tab: a plain download works there.
        return Promise.resolve(localDownload(filename, data, m, base64));
      }
      var args = { filename: String(filename || 'download'), data: data, mime: m };
      if (base64) args.base64 = true;
      return call('save', args, 'tada.save');
    } catch (e) {
      return Promise.reject(e);
    }
  }

  function comment(body, anchor) {
    if (typeof body !== 'string' || !body.trim()) return Promise.reject(err('tada.comment: body must be a non-empty string'));
    var args = { body: body };
    // Accept comment(text, "anchor") or comment(text, {anchor}).
    if (anchor && typeof anchor === 'object') anchor = anchor.anchor;
    if (typeof anchor === 'string' && anchor) args.anchor = anchor;
    return call('comment', args, 'tada.comment');
  }

  // ---------- messages from the viewer ----------

  try {
    window.addEventListener('message', function (ev) {
      try {
        if (!inViewer || ev.source !== parentWin) return;
        var d = ev.data;
        if (!d || typeof d !== 'object' || d.tada !== 1) return;
        if (typeof d.reply === 'number') {
          var p = pending[d.reply];
          if (!p) return;
          delete pending[d.reply];
          clearTimeout(p.timer);
          if (d.ok) p.resolve(d.result === undefined ? null : d.result);
          else p.reject(err(typeof d.error === 'string' && d.error ? d.error : 'Request failed'));
          return;
        }
        if (d.event === 'theme') { setTheme(d.theme); return; }
        if (d.event === 'db' && typeof d.collection === 'string') {
          var info = { collection: d.collection };
          dbSubs.slice().forEach(function (s) {
            if (s.collection === '*' || s.collection === d.collection) safe(s.cb, info);
          });
        }
      } catch (e) { warn('message', e); }
    });
  } catch (e) { /* ignore */ }

  // ---------- expose ----------

  try {
    var tada = {
      artifact: artifact,
      inViewer: inViewer,
      db: db,
      storage: storage,
      save: save,
      comment: comment,
      theme: theme
    };
    Object.defineProperty(tada, '__runtime', { value: 1 });
    if (Object.freeze) Object.freeze(tada);
    Object.defineProperty(window, 'tada', { value: tada, configurable: true, enumerable: false, writable: false });
  } catch (e) {
    try { window.tada = { artifact: artifact, inViewer: inViewer, db: db, storage: storage, save: save, comment: comment, theme: theme }; } catch (x) { /* ignore */ }
  }

  try { if (inViewer) post({ tada: 1, hello: true }); } catch (e) { /* ignore */ }
})();

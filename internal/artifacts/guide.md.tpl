# Writing Ta-Da artifacts

An artifact is a page you publish with the `artifact` tool. It's stored on
this machine and shown in a local viewer (a gallery plus a side panel for
versions, comments and the page's database). Nothing leaves the machine.

## When to make one

Make an artifact when a page would be clearer than terminal text, or when
the user would use the page rather than only read it: a report, a chart, a
dashboard, a small tool, a game, a slide deck, a long document. Don't make
one for an answer that fits in a few lines of chat.

## Kinds

- `page` (default): a single HTML file plus optional supporting files.
- `document`: Markdown (`index.md`), rendered for reading with headings,
  tables, footnotes and code blocks. Use it for write-ups, plans and notes.
- `slides`: HTML where each top-level `<section>` is one 16:9 slide. The
  viewer adds arrow-key navigation, a progress bar and print-to-PDF.

The kind is fixed when the artifact is created.

## The page contract

- Start with `<!doctype html>`, a charset and a viewport meta. A page
  without a doctype is wrapped in a skeleton for you.
- Give it a `<title>` of two to four words that names the thing (for
  example "Q3 Revenue", not "Revenue: explainer"). Put the explanation in
  `description`.
- Define colors as CSS custom properties on `:root`, and redefine them for
  dark mode under `@media (prefers-color-scheme: dark)`. Give `body` an
  explicit background and text color. The viewer follows the system theme.
- Make it work at phone width: a 16px side gutter, no horizontal scroll,
  tables that scroll inside their own container.
- Keep each page under 16 MB; `data:` URIs count toward that.
{{- if .AllowCDN}}
- Scripts may load only from cdnjs.cloudflare.com (preferred),
  cdn.jsdelivr.net/npm/, unpkg.com, cdn.tailwindcss.com or code.jquery.com,
  each at an exact version (`chart.js@4.4.1`, never `chart.js` or
  `chart.js@4`). Stylesheets may load only from Google Fonts. Everything
  else must be inline or a supporting file.
{{- else}}
- No outside requests: the viewer blocks every external script, stylesheet,
  font, image and fetch. Write all CSS and JavaScript inline or as
  supporting files. Draw charts with inline SVG or `<canvas>`, and use the
  system font stack (`system-ui, -apple-system, "Segoe UI", sans-serif`).
{{- end}}
- Pages run in a sandboxed frame. Links open in a new tab; forms, popups
  and alerts don't work, so build those interactions into the page.

## Design

Pick a level of polish that fits the request. A quick chart needs a clean
layout and readable labels; a dashboard or tool the user will keep using
deserves a deliberate type scale, spacing rhythm and a restrained palette.
Prefer one accent color, generous whitespace, and real content over
placeholder text. Never imitate a real company's branding.

## Supporting files

Pass `files` as `{"published/path": "local/source/path"}`. The page refers
to them by the published path (`<script src="app.js">`, `<img
src="img/logo.png">`). On an update, files you pass are added or replaced,
files you leave out are kept, and `null` removes one. HTML files in `files`
are separate pages and need their own doctype.

## Updating

Publish again with the same `id` to make a new version; the link stays the
same and an open viewer reloads. Read the current page first with
`action: "read"` and build on it, and pass `base_version` so you don't
overwrite a newer version the user saved from the page. Give big changes a
`label`.

## The page runtime: `window.tada`

Every page gets a small runtime, no import needed:

- `await tada.db.get(collection, id)`, `tada.db.set(collection, id, data)`
  (or `tada.db.set(collection, data)` for a new id), `tada.db.update(
  collection, id, fields)`, `tada.db.delete(collection, id)`,
  `tada.db.list(collection, {order_by, desc, limit})` and
  `tada.db.query(collection, {field: value}, {order_by, desc, limit})`.
  Documents come back as `{id, data, created, updated}`. This is a small
  shared database saved with the artifact. Use it for anything that must
  persist: tracker rows, sign-ups, game scores, settings. You can read and
  write the same data with the `artifact_data` tool, and the page sees your
  writes live through `tada.db.subscribe(collection, callback)` (use `"*"`
  for every collection; it returns an unsubscribe function).
- `tada.storage.getItem/setItem/removeItem/clear`: a per-artifact
  key-value store for small conveniences such as a remembered tab or
  filter. `localStorage` is mapped onto it.
- `tada.comment(text, {anchor})`: post a comment to the side panel, for
  example from a "Send feedback" button.
- `tada.save(filename, data, mime)`: hand the user a file to download.
- `tada.theme.current` is `"light"` or `"dark"`. Follow changes with
  `tada.theme.onChange(callback)` or the `tadatheme` window event. The
  viewer also sets `data-theme` on `<html>` when the user picks a theme,
  so the `:root[data-theme="dark"]` tokens just work.
- Slides fire a `tada:slide` event on `document` with
  `{index, number, total}` in `detail`.

Don't store important state in plain JavaScript variables only: the page
reloads whenever a new version is published.

## Comments

The user can comment on an artifact in the viewer and send a comment to
you. Read open comments with `artifact_comments` (`action: "list"`), reply
with `action: "reply"`, and resolve a thread once you've acted on it.

## After publishing

The tool returns a link like `http://127.0.0.1:7480/a/<id>`. Give the user
that link in your reply. If the result lists warnings, fix them and publish
again.

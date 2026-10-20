Publish and manage artifacts: HTML pages, Markdown documents and slide decks the user opens in a local viewer at a stable link (http://127.0.0.1:…/a/<id>). Nothing leaves the machine.

Use it when a page would be clearer than terminal text, or when the user would use the page rather than only read it: reports, charts, dashboards, small tools, games, slide decks, long write-ups. Don't use it for short answers.

Actions (`action`, default publish):
- publish: no `id` creates a new artifact; with an `id` it publishes a new version at the same link. Write the page to a file first and pass `file_path` (preferred), or pass `content`. Set `title` (two to four words), `description` (one sentence), `icon` (one generic word) and, for new artifacts, `kind`: page, document (Markdown) or slides.
- read: the page source and its files, comments and database summary. Read before you update, and pass `base_version`.
- list, open, pin, unpin, delete (asks the user).
- guide: the page contract and the `window.tada` runtime (shared database, storage, comments). Read it once before your first artifact in a session.

After publishing, give the user the link from the result, and fix any warnings it lists.

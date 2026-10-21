# AGENTS.md – notes for future sessions

Everything a coding agent (or human) should know before changing LinkScout.

## What it is

LinkScout is a website link checker with a local web UI, written in Go and
shipped as a single static binary (`CGO_ENABLED=0`). The user only enters the
start URL; the tool crawls all internal pages, checks every internal, external
and file URL, and can create an offline copy of the site.

Original requirements (from the user, in German):

- input: only the main URL of the website
- report: all internal URLs, external URLs (for redirects: the target), links to
  PDFs / images / other files
- Referer freely selectable, default behaviour = like Firefox
  (later decision: the Referer field was removed from the UI because it
  confused users; a fixed referer is only available via `-referer` on the CLI)
- sortable / filterable list with URL, HTTP status, type (by file extension), title
- JSON export; re-check either only non-200 URLs or all URLs
- small statistics page
- runs on Linux, ideally as a binary
- unit tests
- download a complete offline copy of the website
- source comments, README etc. in English; the tool itself in German and
  English depending on the system locale (English fallback)
- README: short description, screenshots, install guide for Arch and Debian
- later: releases for Linux and macOS (Apple Silicon) on every version tag,
  a GitHub Pages site, UI in German/English by system language (easy to extend)

## Layout

```
main.go                     CLI flags, UI / headless / mirror modes
internal/i18n               locale detection; one translation table per language (lang_*.go)
internal/fetch              HTTP client: Firefox headers, referer policy, redirects, timeouts
internal/links              pure helpers: normalise, extract links/titles, classify types
internal/crawler            crawl + check, re-check failed, stats, JSON report format
internal/mirror             offline copy (download, path mapping, link rewriting, ZIP)
internal/server             HTTP API + embedded UI (web/index.html, app.js, style.css),
                            download list of offline copies (mirrorjobs.go)
internal/desktop            open URLs / folders (xdg-open, gio; open on macOS)
docs/                       GitHub Pages site (index.html, impressum.html) + screenshots
.github/workflows           ci.yml (tests on Linux + macOS), release.yml (tag → release)
packaging/                  .desktop launcher + icon (installed by `make install`)
```

## Build / test

```bash
make build      # static ./linkscout
make test       # go test ./...
make race       # with the race detector – run this after touching crawler/mirror
make vet        # go vet + gofmt check
make release    # dist/linkscout-{linux-amd64,linux-arm64,darwin-arm64}
```

- Go >= 1.24 (go.mod says `go 1.24.0`). `golang.org/x/net` is pinned to
  v0.50.0 because newer versions require Go 1.26 – Debian 13 ships 1.24.
  Only bump it together with the `go` directive.
- Only dependency: `golang.org/x/net/html`. Keep it that way if possible.
- `make` derives the version from the latest git tag (`v1.2.3` → `1.2.3`,
  `dev` without tags) and bakes it in with `-X main.version`.
- Everything must build with `CGO_ENABLED=0` for Linux *and* macOS; code that
  talks to the desktop belongs in `internal/desktop`.

## Releases, website

- `git tag v1.2.3 && git push origin v1.2.3` starts `.github/workflows/release.yml`:
  checks, `make release`, archives `linkscout-<os>-<arch>.tar.gz` (names
  without version so `releases/latest/download/...` links stay valid;
  binary + README + LICENSE, Linux also `packaging/`), `SHA256SUMS`, a smoke
  test of the macOS binary on a macOS runner, then `gh release create` with
  generated notes. Only tags matching `v<major>.<minor>.<patch>` trigger it.
- The project page is served by GitHub Pages from `docs/` on `main`
  (`docs/.nojekyll`: plain files, no Jekyll). It is English; the link to
  „Impressum & Datenschutz“ and that page are German. No external resources
  (fonts, scripts) – the privacy text promises that.
- Screenshots in `docs/screenshots/*-{light,dark}.webp` were taken with
  headless Chrome (1280 px wide, English UI, check of dlehmann.github.io).
  Retake them when the UI changes noticeably.

## Design decisions (keep consistent)

- **Firefox emulation** (`internal/fetch`): UA `FirefoxUA` (update the version
  number now and then), Firefox Accept headers, cookie jar, and the referer
  policy `strict-origin-when-cross-origin` (`fetch.Referer`). The referer of
  the *original* request is kept across redirects (net/http would replace it;
  see `CheckRedirect`). A fixed referer (`Options.Referer`, CLI `-referer`)
  overrides all of it. The web UI has no referer field (removed on request);
  checks started from the UI use the CLI referer if one was given.
- **HTTP authentication** (added later on request): Basic auth only,
  `fetch.Options.Username/Password/AuthHost`. Crawler and mirror set
  `AuthHost` to the start URL's host; `setAuth` sends credentials
  preemptively only to that host (www/scheme/port-insensitive) and strips them
  on redirects to other hosts. `Password` has `json:"-"`, so it never reaches
  `/api/state`, the injected page state or the JSON report. The UI has the
  fields under "Advanced settings"; an empty password field means "reuse the
  password known for this user name" (`server.fetchOptions`), because the page
  cannot refill it after a reload. CLI: `-user`, `-password`, env
  `LINKSCOUT_PASSWORD`, or credentials in the URL (stripped before use).
  Digest/NTLM/form logins are not supported.
- **Same site** = same host ignoring `www.` and scheme; sub-domains are
  external (`links.SameSite`).
- **Kinds**: `internal` (page on the site), `external` (page elsewhere),
  `file` (known non-page extension: images, PDF, documents, archives, media,
  CSS, JS, fonts, data – internal or external, see `Result.Internal`).
- **MIME type** (`Result.MimeType`, `links.MimeType`) is what the UI shows as
  type everywhere (table, filter, detail, statistics): the content type of a
  successful response, otherwise the type expected from the extension (error
  pages are HTML whatever was requested); extension-less URLs count as
  `text/html`. Old reports without `mimeType` fall back to `type`.
- **Type** = file extension (kept in the JSON; still used for `Category`) (normalised: htm→html, jpeg→jpg); when there is no
  known extension the content type of a *successful* response is used; error
  pages never change the type. Extensions without letters (`v6.12.113`,
  `2024.10`) are ignored.
- **Requests**: internal pages GET (body only if HTML, ≤10 MiB), external pages
  GET (≤512 KiB, for the `<title>`), files HEAD with GET fallback (many servers
  reject HEAD). Bodies of non-HTML responses are never read.
- **Status** in results is the *final* status after redirects; `finalUrl` and
  `redirects` describe the chain. "OK" means exactly 200.
- **Internal redirects** (`/old` → `/new`) are not parsed under the old name;
  the target is queued as its own URL.
- **Re-check failed** (`Crawler.RecheckFailed`) re-dispatches every result
  with status != 200 (including errors/aborted/"not checked"), keeps all 200
  results and follows links of pages that now work.
- **MaxDepth**: URLs deeper than MaxDepth are not discovered (pages at depth
  MaxDepth are checked but not parsed). **MaxPages** (crawler) limits parsed
  internal pages; in the mirror it limits *downloaded* pages.
- **Progress counts** are computed on demand (`Progress()` recounts, O(n)),
  not per processed URL.
- **Mirror** is two-phase: download everything to `<out>/.linkscout-tmp`,
  then map URLs to paths (`mirror.LocalPath`) and rewrite HTML/CSS. Links to
  things that were not downloaded become absolute URLs. `<base>` and
  `integrity` attributes are removed. External resources go to
  `_external/<host>/…` (only with `External`).
  The directory of the start URL is the root of the copy (`mirror.BaseDir`,
  `LocalPathIn`): for a project site like `https://user.github.io/edith`
  `index.html` lands directly in the target directory, other URLs of the
  same host go to `_site/<path>`. Links are resolved against the URL a file
  was actually served from (`entry.final`, after redirects such as
  `/edith` → `/edith/`).
- **Offline copy list** (`server/mirrorjobs.go`): the UI manages a list of
  mirror jobs; every job snapshots URL, target directory, user agent,
  credentials and limits when it is added, and all jobs can run at the same
  time (one `mirror.Mirror` per job). `POST /api/mirror` = add + start,
  `/api/mirror/queue` = add only, `/start`, `/stop`, `/remove` take `{id}`
  (0 = all). Two jobs never share a target directory: an explicit duplicate
  is rejected, a default directory gets `-2`, `-3`, … The list is kept in
  memory only (lost when the process exits).
  "Ordner öffnen" (`POST /api/mirror/open {id}`) runs `Options.OpenDir`
  (default xdg-open/gio) for the directory of a *finished* list entry only –
  never for a path sent by the page – and is refused when the server listens
  on a non-loopback address (`AllowAnyHost`).
  The mirror tab has its own complete form (URL, target directory, user agent,
  advanced settings); the check controls (`#controls`) are hidden there.
  The target directory is a template (`server.ExpandMirrorDir`: `<host>`,
  `<domain>`, `<date>`, `<time>`, default `<base>/linkscout/<host>`); only
  directories produced by a placeholder get the `-2` suffix. Jobs from the
  UI always download external resources (`MirrorRequest.External` defaults
  to true); the CLI keeps `-mirror-external` (default off).
- **UI**: no build step, vanilla JS. The server injects the initial state into
  `index.html` (placeholder `/*__INITIAL_STATE__*/null`, JSON escaped by
  `json.Marshal` so `</script>` cannot break out). The page polls
  `/api/state` while a job runs. Tabs are addressed by URL hash (`#results`,
  `#stats`, `#mirror`) – do not give DOM elements these ids, the browser would
  scroll to them. The nav shows only "Links prüfen"/"Check links" (`#results`) and
  "Offline speichern"/"Save offline" (`#mirror`); inside the link checker the statistics
  ("Übersicht"/"Overview", `#stats`, first) and the results list (`#results`)
  are sub-tabs (`.subtabs`); the overview is always enabled (it shows a hint
  until there are results), is the default on a fresh start (no hash) and is
  what the "Links prüfen" nav button opens. German UI texts say
  "Offline-Kopie" (not "Offline-Abbild").
- **Statistics charts** are hand-written inline SVG in `app.js` (no chart
  library, keep it that way): a 100 % share bar of status classes, a response
  time histogram and "clicks from the start page" columns (single colour –
  a version stacked by kind was too hard to read), plus CSS bar lists.
  Status colours reuse `--ok/--info/--bad/--warn/--err`.
  Every mark has a tooltip (`data-tip`, filled via `textContent`). The broken
  and slowest URL lists were removed from the page on request (the filterable
  results list covers them); `Stats.BrokenList/Slowest` stay in the JSON.
  The page computes its figures itself (`summarize()` in `app.js`, same rules
  as `crawler.Summarize`) because it can leave out external URLs
  (kind `external` = links to pages on other websites; files always count, wherever they are hosted); that toggle is off by default and stored in
  localStorage (`ui.statsExternal`). Type lists show every MIME type – no
  "other" bucket – so "size by MIME type" adds up to the total size.
- **Drill-down**: every tile, status segment, column and bar row of the
  statistics carries `data-drill` = JSON of results-list filter values
  (`kind`, `status`, `type`, `depth`, `time`, optional `sort`);
  clicking it resets the list filters and applies those. Everything the
  statistics can show must therefore exist as a real list filter (no hidden
  criteria): status also takes `broken`, `redirected` and exact codes `c404`,
  MIME type `-` = unknown, `time` is an index into `TIME_BUCKETS`. When
  external links are left out of the statistics the drill sets
  `kind: "noext"` (all except external), so the list count equals the clicked
  number.
  Every filter value is also a visible column (Klicks = depth, plus Größe);
  a separate "this/other website" column was dropped as redundant with Art. Long URLs are cut at the *start* with an ellipsis
  (`urlCell()`: `direction: rtl` + LRM marks, `td.url { max-width: 0 }`);
  the full URL is in the `title` tooltip. URLs in the list are links; every link to another
  origin opens in a new tab (`target=_blank`, `noopener noreferrer`, enforced
  by a capturing click handler), clicking elsewhere in the row opens the
  detail dialog.
- **Security of the local server**: Host header must be loopback (DNS
  rebinding) unless `-listen` is a non-loopback address; every non-GET request
  needs `X-LinkScout: 1` (CSRF; custom headers force a CORS preflight that is
  never answered). Keep both when adding endpoints.
- **i18n**: every user-visible string lives in `internal/i18n/lang_<code>.go`
  (English = reference + fallback, German). A new language is one new file
  that calls `register` (see the package comment); tests enforce that every
  table has exactly the English keys and the same placeholders (`%d`, `%s`,
  `%name%`). The UI uses `%name%` placeholders, the CLI printf verbs.
  Language precedence: `-lang` flag, `LANGUAGE`, `LC_ALL`, `LC_MESSAGES`,
  `LANG`; on macOS without these variables (started from Finder/Dock) the
  first entry of `defaults read -g AppleLanguages`.

## Desktop launcher

`packaging/linkscout.desktop` starts `linkscout` without arguments. If the
default address 127.0.0.1:8088 is taken by another LinkScout (`isLinkScout`
probes `/api/state`) and no URL was given, the new process just opens the
browser on the running instance and exits; otherwise it falls back to a free
port. The user's own install is `make install PREFIX=$HOME/.local` (no sudo).

## JSON report format

`crawler.Report`: `generator, startUrl, createdAt, userAgent, referer, stats,
results[]` – each result is `crawler.Result` (see JSON tags). The same format
is accepted by `POST /api/import` and `-recheck`, so keep it backwards
compatible (only add fields).

## Testing approach

Unit tests use `httptest` servers. To simulate an *external* site a second
test server is addressed via `localhost` instead of `127.0.0.1` (different
host name → external). Server tests drive the real HTTP API.

## Ideas / not done yet

- robots.txt support (currently ignored on purpose – it is the site owner's checker)
- sitemap.xml as additional seed
- per-host rate limiting (only a global delay exists)
- link anchors (`#fragment`) are not validated
- HTML report export (only JSON today)
- persistent history / diff between two runs
- `.deb` / AUR package (`PKGBUILD`) – README currently builds from source
- Digest authentication and cookie/form based logins

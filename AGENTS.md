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
internal/mirror             offline copy (download, path mapping, link rewriting, ZIP),
                            rules.go (+/- patterns), scripts.go (URLs in JS),
                            state.go (manifest, update, continue)
internal/robots             robots.txt and sitemap.xml (used by crawler and mirror)
internal/server             HTTP API + embedded UI (web/index.html, app.js, style.css),
                            download list of offline copies (mirrorjobs.go)
internal/desktop            open URLs / folders (xdg-open, gio; open on macOS)
docs/                       GitHub Pages site (index.html, impressum.html) + screenshots
.github/workflows           ci.yml (tests on Linux + macOS), release.yml (tag → release),
                            packages.yml (.deb + Arch packages, called by release.yml)
packaging/                  .desktop launcher + icon (installed by `make install`),
                            nfpm.yaml (package definition)
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
  binary + README + LICENSE, Linux also `packaging/`), the packages of
  `packages.yml` (see below), `SHA256SUMS` over all files, a smoke
  test of the macOS binary on a macOS runner, then `gh release create` with
  the section of the version in `CHANGELOG.md` as notes (generated notes
  when it is missing) – add the section before tagging. Only tags matching
  `v<major>.<minor>.<patch>` trigger it.
- **Packages** (`.github/workflows/packages.yml`, `packaging/nfpm.yaml`):
  nfpm (pinned in `NFPM_VERSION`, only a CI tool) turns the static Linux
  binaries into `linkscout-{amd64,arm64}.deb` and
  `linkscout-{x86_64,aarch64}.pkg.tar.zst` (names without version for the
  `releases/latest/download/…` links on the website), with the binary in
  `/usr/bin`, desktop file, icon, README and licence. The x86_64 packages
  are installed once in `debian:trixie` and `archlinux` containers before
  the release is published. The workflow can also be run by hand (artifact
  "packages"); between tags the version is `1.2.3+r4.gabc1234`
  (`version_schema: none`, nfpm's semver handling mangles it for Arch).
  nfpm does not expand variables in `src`, so the binary is copied to
  `dist/pkg/linkscout` first. Not in an apt/pacman repository – updates
  mean installing the new package. An AUR `PKGBUILD` is still open.
- The project page is served by GitHub Pages from `docs/` on `main`
  (`docs/.nojekyll`: plain files, no Jekyll). It is English; the link to
  „Impressum & Datenschutz“ and that page are German. No external resources
  (fonts, scripts) – the privacy text promises that. The only script is
  the inline one at the end of `index.html` that adds a "Copy" button to
  every `<pre>` (Clipboard API, `execCommand` fallback, else it selects the
  text).
- Screenshots in `docs/screenshots/*-{light,dark}.webp` were taken with
  headless Chrome (1280 px wide, English UI, check of dlehmann.github.io).
  Retake them when the UI changes noticeably (details in the checklist).

### Checklist for a new version

Work through it completely – a release is not done when the tag is pushed.

1. **Version number** (semver): major = incompatible change of the CLI
   flags or of the JSON report format (it must stay readable by
   `-recheck`, `-compare` and `/api/import`), minor = new features, patch =
   fixes only.
2. **Checks**: `make vet`, `make race`, `make build`; try the UI in German
   and English (`-lang de|en`), light and dark, a phone-sized window, and
   the exports (JSON, CSV, HTML, PDF, sitemap.xml, sitemap HTML).
   New UI strings need an entry in every `lang_*.go` (the tests enforce it).
   Now and then: bump the Firefox version in `fetch.FirefoxUA`.
3. **CHANGELOG.md**: rename `[Unreleased]` to `[x.y.z] - YYYY-MM-DD`
   (sections Added / Changed / Fixed / Removed, written for users), add a
   new empty `[Unreleased]` only when work continues, and fix the link
   references at the end (`[x.y.z]: …/compare/vPREV...vx.y.z`,
   `[Unreleased]: …/compare/vx.y.z...HEAD`). This section becomes the
   release notes.
4. **README.md**: first paragraph, "What it is for", feature list, CLI
   examples and the options table (compare with `linkscout -help`), the
   screenshot tables (new screenshots go there too).
5. **Website `docs/index.html`**: lead text, feature cards, the sections
   with their screenshots (`width`/`height` attributes must match the
   image files), the nav links, the CLI block, `<meta name="description">`
   and the OpenGraph tags. `docs/impressum.html` (German) only when hosting
   or privacy change. No external resources.
6. **Screenshots** (whenever the UI changed visibly – compare every image
   with the current UI, also the ones that seem unaffected):
   - start an English instance (`./linkscout -lang en -no-browser -listen
     127.0.0.1:<port>`), check `https://dlehmann.github.io/`, wait until
     it is finished;
   - headless Chrome with remote debugging (`google-chrome-stable
     --headless=new --remote-debugging-port=… --hide-scrollbars`), via the
     DevTools protocol: `Emulation.setDeviceMetricsOverride` (1280 wide,
     scale 1), `Emulation.setEmulatedMedia` with `prefers-color-scheme`
     light and dark, `Page.captureScreenshot`;
   - replace the dev version in the footer by the release version
     ("LinkScout 1.2.0-3-gabc-dirty" → "LinkScout 1.3.0") before capturing;
   - files, each `-light` and `-dark`, WebP quality 82
     (`magick in.png -quality 82 out.webp`):
     `overview` (`#stats`, full page height), `seo` (`#stats`, 1280×900,
     scrolled to `.seo-health` minus 80 px for the sticky header),
     `sitemap` (`#sitemap`, 1280×900), `results` (`#results`, 1280×900),
     `mirror` (`#mirror` with a few entries in the download list, 1280×900);
   - the card of the profile README: `#stats` at 1600×900, resized to
     1280×720 → `images/linkscout-{light,dark}.webp` in `dlehmann/dlehmann`.
7. **AGENTS.md**: layout, design decisions and these notes for everything
   new or changed.
8. **Publish**: commit, `git push origin main`, then
   `git tag -a vx.y.z -m "LinkScout x.y.z" && git push origin vx.y.z`.
   Wait for the CI and Release workflows (`gh run watch`), then check the
   release: three archives, two `.deb`, two `.pkg.tar.zst` plus
   `SHA256SUMS`, notes = CHANGELOG section;
   download one archive and run `linkscout -version`.
9. **GitHub Pages** deploys `docs/` with the push; check
   https://dlehmann.github.io/linkscout/ (new texts and images, phone
   width without horizontal scrolling).
10. **Profile repository `dlehmann/dlehmann`**: the card images (step 6) and
    the one-line description of LinkScout in `README.md` when the features
    changed.
11. **Homepage `dlehmann/dlehmann.github.io`** (default branch is
    `master`, not `main` – never push to `main` there): the short
    description in the project list and the `<meta name="description">` of
    `index.html` when they no longer fit, the `lastmod` of
    `/linkscout/` in `sitemap.xml`.

Pitfalls: pushing over SSH needs the Bitwarden SSH agent to be unlocked.
The HTTPS fallback with the `gh` token cannot push changes to
`.github/workflows/` (the token lacks the `workflow` scope) – such commits
have to wait for SSH.

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
- **Mirror updates** (`mirror/state.go`, taken from HTTrack): a finished run
  writes `<out>/.linkscout/manifest.json` (URL, local path, type, status,
  ETag, Last-Modified per file) and keeps the *originals* of rewritten
  HTML/CSS in `.linkscout/originals/<sha1(url)>` (needed to parse unchanged
  pages). The next run sends If-None-Match / If-Modified-Since
  (`fetch.DownloadIf`); on 304 – and on temporary failures (transport
  error, 5xx, 429, 408: `Problem.Kept`) – the previous file is hard-linked
  (`linkOrCopy`) into the tmp dir and goes through phase 2 like a fresh
  download (renaming a hard link onto the file it links to does nothing;
  `rewriteFile` removes `dst` first so it never truncates a shared inode).
  Files of the old manifest whose path is not used any more are deleted
  (`Progress.Removed`) – only files listed there, never other files. A
  stopped run (or a failed phase 2) keeps `.linkscout-tmp` and writes
  `.linkscout/partial.json`; the next run reuses those files *without* a
  request (`prevFile.partial`). `Config.Fresh` ignores all of it. Both
  directories are left out of the ZIP, and a site path starting with them
  gets a `_` prefix. The UI calls the start button of a finished entry
  "Aktualisieren"/"Update", of a stopped one "Fortsetzen"/"Continue".
- **What the mirror downloads** (`run.add`): the start URL always; then
  `Rules` (`mirror/rules.go`: `+`/`-` glob patterns, `*` = anything, a
  pattern starting with `/` applies to path+query, otherwise host+path+query
  without scheme; whole-string match, also against the percent-decoded form;
  last matching rule wins) – an including rule can even take an external
  page (saved, not crawled). Without a matching rule: everything of the
  site, external embedded resources with `External`, external *linked*
  files (known file extension) with `ExternalFiles`, never external pages.
  `Robots` (off by default in UI and CLI, like the check ignores it; CLI
  `-mirror-robots`) skips URLs of the
  site that robots.txt ("*" group) disallows; they are listed as
  `Problem.Robots`. `Sitemap` adds the sitemap pages after the link crawl
  (like the crawler; CLI `-no-sitemap`). Scripts are searched for quoted
  URLs/paths with a known extension (`mirror/scripts.go`; relative paths
  only in inline scripts, script files only when internal and ≤5 MiB);
  they are downloaded but scripts are never rewritten, and their errors are
  not reported (`entry.guessed`). A stopped run does not report the URLs
  that were cancelled.
- **Limits** live in `fetch.Options`: `RatePerHost` (requests per second per
  host, `www.`/port ignored; `Download` waits *before* its idle timer
  starts) and `Bandwidth` (bytes per second shared by all downloads of a
  client, read in chunks of ≤¼ s so waits stay below the idle timeout).
  CLI `-rate` applies to checks and copies (UI checks use it too, via
  `Defaults.RatePerHost`), `-bandwidth`/`-max-file-size` to copies; the
  mirror form has fields for all three.
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
  Pages (links, not embedded resources) with a status other than 200 are
  saved and parsed anyway when they have a body (custom error pages, sites
  answering everything with 404); every non-200 URL, transport error and
  too-large file ends up in `Progress.Problems` (`Saved` tells whether a file
  was written; sorted by URL when the run ends – as a copy, earlier
  snapshots share the slice). The UI shows them as `<details class="problems">`
  per list entry (open state kept in `openProblems` across re-renders).
  Internationalised host names are converted to Punycode (`links.ToASCII`,
  `links.ASCIIHost` – own RFC 3492 encoder, because `x/net/idna` would pull
  in `golang.org/x/text`): in the UI URL field on `change`/submit
  (`asciiURL()`), in `addMirror`, `ExpandMirrorDir` and for every URL the
  mirror queues. The checker is not affected.
- **UI**: no build step, vanilla JS. The server injects the initial state into
  `index.html` (placeholder `/*__INITIAL_STATE__*/null`, JSON escaped by
  `json.Marshal` so `</script>` cannot break out). The page polls
  `/api/state` while a job runs. Tabs are addressed by URL hash (`#results`,
  `#stats`, `#mirror`; the former `#seo` maps to `#stats`) – do not give DOM
  elements these ids, the
  browser would scroll to them. The nav shows only "Website prüfen"/"Check
  website" (`#results`) and "Offline speichern"/"Save offline" (`#mirror`);
  inside the checker the sub-tabs (`.subtabs`) are the statistics
  ("Übersicht"/"Overview", `#stats`, first, including the SEO charts:
  health by worst issue, title and description length), the site
  structure (`#sitemap`) and the results list (`#results`, always the last
  tab). There is no SEO tab any more:
  issues are shown per URL (column, filter, details with `issueContext()`). The overview is always enabled (it shows a
  hint until there are results), is the default on a fresh start (no hash)
  and is what the "Website prüfen" nav button opens. German UI texts say
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
  Every filter value is also visible in the list (clicks = depth, plus size);
  a separate "this/other website" column was dropped as redundant with Art.
  The results list is not a table but two-line entries (`resultRow()`): the
  URL on the first line, the values on the second, in columns shared by all
  entries (`#results-list` grid, each `.result` a subgrid – so values line
  up; a missing value is an empty cell, not "–"; phones fall back to
  wrapping); sorting is a "Sort by"
  select plus direction button (`SORT_KEYS`). Every filter has a label
  above it and "All"/"Alle" as first option; a set filter gets `.active`. Long URLs are cut at the *start* with an ellipsis
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

## SEO checks

- Facts are collected in the same HTML pass that extracts the links
  (`links.Facts`: titles, descriptions, robots, canonicals, headings, images
  without alt, hreflang, JSON-LD, OpenGraph/Twitter, lang, viewport, ids,
  #fragment links) and stored per internal 200 HTML page without redirect in
  `Result.SEO` (`crawler.PageSEO`, plus X-Robots-Tag / Link headers, mixed
  content, content hash, missing security headers, certificate expiry).
  A URL that redirects gets no facts – they belong to the target.
- `crawler.Analyze` evaluates all checks and sets `Result.Issues`; it runs
  on every state snapshot and in `NewReport` (O(n)), so imported reports are
  analysed too. The catalogue is `crawler.Issues` (ID + severity, display
  order); texts are `issue.<id>` and `issue.<id>.help` in the i18n tables (a
  test enforces them for every language). Add a check = add it to `Issues`,
  implement it in `Analyze`/`analyzeURL`, add both texts, add a test.
- Duplicate checks (title, description, H1, content) only compare indexable
  pages (no `noindex`, canonical = self). Security headers and certificate
  are reported on the start page only (they are site-wide).
- robots.txt (`internal/robots`) is loaded before the crawl and only marks
  URLs (`RobotsBlocked`); the crawl ignores it on purpose. With
  `Config.Sitemap` the sitemap URLs (robots.txt `Sitemap:` or `/sitemap.xml`,
  indexes and .gz followed, limits in robots/robots.go) are added *after* the link
  crawl; URLs not reached by links get `Depth = NotLinked (-1)` = orphans,
  and so do the pages only linked from orphans.
- `POST /api/compare` / `-compare` (`crawler.Compare`) and the CSV / HTML /
  PDF exports (`crawler.WriteCSV`, `server.WriteHTMLReport`,
  `server.WritePDFReport`, `GET /api/export?format=`) re-run Analyze, so they
  work with old reports too. The PDF writer (`server/pdf.go`) is hand-written
  (no dependency): standard Helvetica fonts, WinAnsi encoding, A4 portrait,
  link annotations for URLs.

## Site structure (Sitemap tab)

- `crawler.SiteTree` builds the tree of `IsSitePage` results (internal,
  200, no redirect, HTML) by URL path segments; directories without a page
  of their own are nodes without URL, query strings are child nodes
  (`/list?page=2` below `/list`). `GET /api/sitemap` serves it as JSON for
  the tab (`renderSitemap()` refetches only when the results changed),
  `format=xml` as sitemap.xml (`WriteSitemapXML`, protocol 0.9, max. 50,000
  URLs, `lastmod` = `Result.LastModified` in UTC) and `format=html` as
  nested list (`server/sitemap.go`). Pages with `noindex`,
  `canonical_other` or robots.txt block are marked (`SiteNode.Excluded`) and
  left out of sitemap.xml – the handler runs Analyze first. Search engines
  only accept URLs of the property the sitemap belongs to, so
  `WriteSitemapXML` rewrites every URL to `SiteNode.Origin` (scheme + host
  of the start page after redirects, else the most common one) and drops
  the duplicates this creates; tree and HTML export keep the URLs as found.
- `Result.LastModified` is the Last-Modified header of a 2xx response
  (`fetch.Response.LastModified`); it is shown in the list, the details,
  the reports and the exports.

## Reports and exports

- JSON (`crawler.Report`, incl. `overview`) and CSV (`crawler.CSVHeader`:
  every field of a result and of its `PageSEO` except the element ids;
  several values per cell separated by newlines) carry all data.
- HTML and PDF show the overview and the detail view of every URL, nothing
  else (the separate SEO issue list was removed on request). Both render
  `server.reportView` (`reportview.go`), built once and translated:
  `crawler.NewOverview` (`crawler/overview.go`) computes the figures of
  the overview page – it mirrors `summarize`/`renderStats`/`seoCharts` in
  `app.js`, and `detailBuilder` mirrors `openDetail`/`seoRows`/
  `issueContext`. Change both sides together.
- `external=1` on `/api/export` counts external links in the figures; the
  UI sets it from the overview checkbox (`updateExportLinks`). The CLI
  leaves them out, like the UI default.
- Reports never shorten a list (on request): every linking page, every
  entry of an issue context and of the bar lists is shown, unlike the
  detail dialog of the UI, which caps issue context lists at 10. In the PDF
  long bar lists continue on the next page (`drawBarsFlow`), detail values
  wrap and break across pages line by line.

## JSON report format

`crawler.Report`: `generator, startUrl, createdAt, userAgent, referer, stats,
issues[], results[]` – each result is `crawler.Result` (see JSON tags; the SEO
fields `inSitemap, robotsBlocked, seo, issues` are optional). The same format
is accepted by `POST /api/import`, `POST /api/compare`, `-recheck` and
`-compare`, so keep it backwards compatible (only add fields).

## Testing approach

Unit tests use `httptest` servers. To simulate an *external* site a second
test server is addressed via `localhost` instead of `127.0.0.1` (different
host name → external). Server tests drive the real HTTP API.

## Ideas / not done yet

- rate/bandwidth fields in the check form of the UI (only `-rate` on the
  CLI applies to checks today)
- byte-range resume of a single interrupted large file (continuing works
  per file)
- persistent history of runs (comparing needs an exported report today)
- AUR package (`PKGBUILD`) and apt/pacman repositories (the release only
  has the package files)
- Digest authentication and cookie/form based logins
- SEO checks that would need a headless browser (JavaScript rendering) or
  external services (Search Console, PageSpeed, backlinks) – deliberately left
  out to keep the single binary and "nothing leaves your machine"

# Changelog

All notable changes to LinkScout. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), versions follow
[Semantic Versioning](https://semver.org/). The release workflow uses the
section of a version as release notes.

## [1.2.0] - 2026-10-22

### Added

- **PDF export** of the report (A4 landscape: key figures, SEO issues, all
  URLs), in the export menu and with `-report report.pdf`
- **SEO charts in the overview**: SEO health of the pages (most severe issue
  per page) and the length of titles and meta descriptions with the
  recommended range; every part opens the filtered results list. New filters
  for the worst issue, title length and description length
- The **details of a URL** explain every SEO issue: severity, what the check
  means and what exactly was found (lengths, the pages with the same title,
  the images without alt text, the missing headers, the redirect chain …)

### Changed

- Results list: two-line entries – the URL gets the full width of the first
  line; the second line shows status and title on the left and MIME type,
  kind, clicks, size, time and SEO issues on the right, in columns that line
  up across all URLs (an empty cell where a value is missing, numbers
  right-aligned). Sorting moved to a "Sort by" selection
- Filters have labels, their first option is simply "All"; a filter that is
  set is highlighted
- The SEO tab is gone: its charts moved to the overview, the issues are
  shown per URL in the results list (column, filter, details)
- Sub-tab order: overview first, results list last

## [1.1.0] - 2026-10-21

### Added

- **SEO checks** in a new *SEO* tab: 48 checks grouped into errors, warnings
  and notices. A click on a check opens the results list with the affected
  URLs; the result details show the SEO data of a page.
  - Indexability: `noindex` (meta robots and `X-Robots-Tag`), missing,
    multiple, foreign or broken canonicals, URLs blocked by robots.txt
  - Titles and meta descriptions: missing, multiple, duplicate, too long or
    too short; H1 missing, multiple or duplicate, skipped heading levels
  - Redirect chains and loops, internal links to redirects, broken
    `#fragment` anchors, mixed content (http resources on https pages)
  - Images without alt text, images larger than 300 KiB
  - Pages more than 3 clicks deep, pages linked only once
  - hreflang: invalid codes, broken targets, missing return links, conflicts
    with canonicals
  - Invalid JSON-LD, missing viewport, missing `lang`, missing OpenGraph /
    Twitter preview, exact duplicate content
  - Missing security headers and certificates that expire within 30 days
  - Long URLs, uppercase letters, underscores and parameters in URLs
- **sitemap.xml**: after the link crawl the URLs of the sitemap(s) (from
  robots.txt or `/sitemap.xml`, including sitemap indexes and `.gz`) are
  checked too. Pages that are only listed there show up as *not linked*
  (orphan pages). Option in the advanced settings, `-no-sitemap` on the
  command line
- **robots.txt** is read and blocked URLs are marked (they are still checked –
  LinkScout is the site owner's tool)
- **Compare** the results with an earlier JSON report: new and removed URLs,
  changed status codes, new and fixed SEO issues. In the UI (*Compare …*) and
  with `-compare old.json` on the command line
- **Export as CSV and HTML** in addition to JSON. On the command line the
  format follows the file extension of `-report`
- Results list: column and filter for SEO issues; the click depth filter and
  chart know *not linked* URLs

### Changed

- The first tab is now called *Check website* (*Website prüfen*), because it
  checks more than links
- Redirect loops are reported as such instead of "too many redirects"

## [1.0.0] - 2026-10-21

First public release.

- Check every internal page, external link and file of a website, with a
  local web UI (overview with charts, filterable results list, details),
  JSON export/import and re-check of failed URLs
- Behaves like Firefox (headers, cookies, referrer policy); HTTP Basic
  authentication
- Save websites offline, with a download list for several sites at the same
  time
- English and German UI by system language
- Static binaries for Linux (x86_64, arm64) and macOS (Apple Silicon)

[1.2.0]: https://github.com/dlehmann/linkscout/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/dlehmann/linkscout/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/dlehmann/linkscout/releases/tag/v1.0.0

package mirror

// Updates and resuming.
//
// A finished run writes <out>/.linkscout/manifest.json: for every file of the
// copy its URL, local path, content type, status and the validators of the
// response (ETag, Last-Modified). HTML and CSS files are rewritten in the
// copy, so their original bodies are kept in .linkscout/originals – the next
// run needs them to find the links of unchanged pages.
//
// The next run into the same directory asks the server with If-None-Match /
// If-Modified-Since. On 304 Not Modified the previous file is reused: it is
// hard-linked (or copied) into the temporary directory and goes through
// phase 2 like a fresh download. When a file that was in the previous copy
// fails temporarily (transport error, 5xx, 429) the previous version is kept
// as well. Files of the previous copy whose URL is no longer part of the
// site are removed – only files listed in the manifest, never anything else
// in the directory.
//
// A run that is stopped (or fails in phase 2) keeps its temporary directory
// and writes .linkscout/partial.json; the next run reuses those downloads
// without asking the server again, so it continues where the last one ended.

import (
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/dlehmann/linkscout/internal/fetch"
	"github.com/dlehmann/linkscout/internal/links"
)

// File and directory names inside the target directory.
const (
	stateDirName = ".linkscout"
	manifestName = "manifest.json"
	partialName  = "partial.json"
	originalsDir = "originals"
)

// record describes one file of a copy (or of an interrupted run).
type record struct {
	URL          string    `json:"url"`
	Final        string    `json:"final,omitempty"`
	Local        string    `json:"local,omitempty"` // path in the copy (manifest only)
	Type         string    `json:"type,omitempty"`  // content type
	Status       int       `json:"status"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag,omitempty"`
	LastModified time.Time `json:"lastModified,omitzero"`
}

// manifest is the content of manifest.json and partial.json.
type manifest struct {
	Generator string    `json:"generator"`
	StartURL  string    `json:"startUrl"`
	Updated   time.Time `json:"updated"`
	Files     []record  `json:"files"`
}

// prevFile is a file of an earlier run that may be reused.
type prevFile struct {
	record
	src     string // file with the original body
	partial bool   // from an interrupted run: reuse without asking the server
}

// validators returns the conditional request headers for the file.
func (p *prevFile) validators() fetch.Validators {
	if p == nil || p.partial {
		return fetch.Validators{}
	}
	return fetch.Validators{ETag: p.ETag, LastModified: p.LastModified}
}

// response describes the reused file as if it had just been downloaded.
func (p *prevFile) response(final string) fetch.Response {
	if final == "" {
		final = p.Final
	}
	if final == "" {
		final = p.URL
	}
	return fetch.Response{Status: p.Status, ContentType: p.Type, FinalURL: final, Size: p.Size, ETag: p.ETag, LastModified: p.LastModified}
}

// needsOriginal reports whether a file is rewritten in the copy, so its
// original body must be kept for the next update.
func needsOriginal(ctype, u string) bool {
	return links.IsHTML(ctype) || isCSS(ctype, u)
}

// stateFile returns the path of a file in the state directory.
func (r *run) stateFile(name ...string) string {
	return filepath.Join(append([]string{r.cfg.OutDir, stateDirName}, name...)...)
}

// loadPrevious reads the manifest and the state of an interrupted run. It
// must run before the temporary directory is created: without a usable
// partial.json leftovers of a crashed run are removed.
func (r *run) loadPrevious() {
	r.prev = map[string]*prevFile{}
	r.old = readManifest(r.stateFile(manifestName)) // needed to remove vanished files, also with Fresh
	partial := readManifest(r.stateFile(partialName))
	if r.cfg.Fresh || partial == nil {
		os.RemoveAll(r.tmp)
		os.Remove(r.stateFile(partialName))
		partial = nil
	}
	if r.cfg.Fresh {
		return
	}
	if r.old != nil {
		for _, rec := range r.old.Files {
			if rec.Status != 200 || (rec.ETag == "" && rec.LastModified.IsZero()) || rec.Local == "" {
				continue // without validators the server cannot answer 304
			}
			src := filepath.Join(r.cfg.OutDir, filepath.FromSlash(rec.Local))
			if needsOriginal(rec.Type, rec.URL) {
				src = r.stateFile(originalsDir, hash(rec.URL))
			}
			if isFile(src) {
				r.prev[rec.URL] = &prevFile{record: rec, src: src}
			}
		}
	}
	if partial != nil {
		for _, rec := range partial.Files {
			if src := filepath.Join(r.tmp, hash(rec.URL)); isFile(src) {
				r.prev[rec.URL] = &prevFile{record: rec, src: src, partial: true}
			}
		}
	}
}

// savePartial records the downloads of an interrupted run (including the
// ones an earlier interrupted run left and this one did not reach), so the
// next run can continue.
func (r *run) savePartial() {
	r.mu.Lock()
	byURL := map[string]record{}
	for u, p := range r.prev {
		if p.partial {
			byURL[u] = p.record
		}
	}
	for _, e := range r.order {
		if e.ok && e.tmp != "" {
			byURL[e.url] = e.record()
		}
	}
	r.mu.Unlock()
	m := &manifest{Generator: "LinkScout", StartURL: r.start.String(), Updated: time.Now().UTC()}
	for _, rec := range byURL {
		if isFile(filepath.Join(r.tmp, hash(rec.URL))) {
			m.Files = append(m.Files, rec)
		}
	}
	if len(m.Files) == 0 { // nothing to continue with
		os.RemoveAll(r.tmp)
		os.Remove(r.stateFile(partialName))
		os.Remove(r.stateFile()) // only if empty
		return
	}
	writeJSON(r.stateFile(partialName), m)
}

// record returns the manifest entry of a downloaded entry.
func (e *entry) record() record {
	return record{URL: e.url, Final: e.final, Local: e.local, Type: e.ctype, Status: e.status,
		Size: e.size, ETag: e.etag, LastModified: e.lastMod}
}

// finishState is the end of phase 2: it writes the manifest, keeps the
// originals of rewritten files, removes the files of the previous copy that
// are gone and the state of an interrupted run. It returns the number of
// removed files.
func (r *run) finishState(written []*entry) (int, error) {
	m := &manifest{Generator: "LinkScout", StartURL: r.start.String(), Updated: time.Now().UTC()}
	used := map[string]bool{}
	originals := map[string]bool{}
	for _, e := range written {
		m.Files = append(m.Files, e.record())
		used[e.local] = true
		if needsOriginal(e.ctype, e.url) {
			originals[hash(e.url)] = true
		}
	}
	removed := 0
	if r.old != nil {
		for _, rec := range r.old.Files {
			if rec.Local == "" || used[rec.Local] || !filepath.IsLocal(filepath.FromSlash(rec.Local)) {
				continue
			}
			p := filepath.Join(r.cfg.OutDir, filepath.FromSlash(rec.Local))
			if os.Remove(p) == nil {
				removed++
				removeEmptyDirs(r.cfg.OutDir, path.Dir(rec.Local))
			}
		}
	}
	if list, err := os.ReadDir(r.stateFile(originalsDir)); err == nil {
		for _, f := range list {
			if !originals[f.Name()] {
				os.Remove(r.stateFile(originalsDir, f.Name()))
			}
		}
	}
	os.Remove(r.stateFile(partialName))
	return removed, writeJSON(r.stateFile(manifestName), m)
}

// keepOriginal moves the downloaded original of a rewritten file into the
// state directory.
func (r *run) keepOriginal(e *entry) error {
	dst := r.stateFile(originalsDir, hash(e.url))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return moveFile(e.tmp, dst)
}

// removeEmptyDirs removes dir (slash separated, relative to root) and its
// parents as long as they are empty.
func removeEmptyDirs(root, dir string) {
	for d := dir; d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if os.Remove(filepath.Join(root, filepath.FromSlash(d))) != nil {
			return // not empty (or gone)
		}
	}
}

// readManifest reads manifest.json or partial.json; nil when missing or
// unreadable.
func readManifest(p string) *manifest {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var m manifest
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &m
}

// writeJSON writes v atomically (temporary file + rename).
func writeJSON(p string, v any) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// isFile reports whether p is a regular file.
func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// linkOrCopy makes dst a hard link of src, or a copy when the file system
// has no hard links. A hard link costs no space, and phase 2 can "move" it
// onto the very file it links to (rename of two links of one file does
// nothing).
func linkOrCopy(src, dst string) error {
	os.Remove(dst)
	if os.Link(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

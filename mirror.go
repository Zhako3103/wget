package main

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MirrorOptions are the settings of --mirror and its companion flags.
type MirrorOptions struct {
	Dir     string   // -P
	Rate    int64    // --rate-limit
	Reject  []string // --reject / -R : file suffixes to skip
	Exclude []string // --exclude / -X : directories to skip
	Convert bool     // --convert-links
	Level   int      // --level / -l : link depth (-1 = unlimited, 0 = only the start page + its files)
}

type page struct {
	url  *url.URL
	path string
	kind string // "html" or "css"
}

type item struct {
	u     *url.URL
	depth int
	nav   bool // reached through an <a> link (must stay on the site's host)
}

type mirror struct {
	opt     MirrorOptions
	root    *url.URL
	outDir  string
	client  *http.Client
	seen    map[string]bool   // URLs already queued/fetched
	saved   map[string]string // URL key -> local file
	queue   []item
	pages   []page
	reject  []string
	exclude []string
	files   int
	bytes   int64
}

var unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// urlKey identifies a resource independently of scheme and fragment.
func urlKey(u *url.URL) string {
	p := u.Path
	if p == "" {
		p = "/"
	}
	k := strings.ToLower(u.Host) + p
	if u.RawQuery != "" {
		k += "?" + u.RawQuery
	}
	return k
}

// resolve turns a reference found in a document into an absolute http(s) URL.
func resolve(base *url.URL, ref string) (*url.URL, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "#") {
		return nil, false
	}
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, false
	}
	return u, true
}

var extByType = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp",
	"image/svg+xml": ".svg", "image/x-icon": ".ico", "image/vnd.microsoft.icon": ".ico",
	"image/avif": ".avif", "application/javascript": ".js", "text/javascript": ".js",
	"application/json": ".json", "font/woff": ".woff", "font/woff2": ".woff2",
	"application/font-woff": ".woff", "application/pdf": ".pdf",
}

// localPath decides where a downloaded URL is stored inside the mirror folder.
// Files of the main host go directly into outDir; files of other hosts (images
// or styles served from a CDN) go into outDir/<host>/.
func localPath(outDir string, root, u *url.URL, kind, mt string) string {
	hasSlash := u.Path == "" || strings.HasSuffix(u.Path, "/")
	p := path.Clean("/" + u.Path)
	if hasSlash {
		p = path.Join(p, "index.html")
	}
	dir, base := path.Split(p)
	if u.RawQuery != "" {
		ext := path.Ext(base)
		base = strings.TrimSuffix(base, ext) + "_" + unsafeNameRe.ReplaceAllString(u.RawQuery, "_") + ext
	}
	ext := strings.ToLower(path.Ext(base))
	switch {
	case kind == "html" && ext != ".html" && ext != ".htm":
		base += ".html"
	case kind == "css" && ext != ".css":
		base += ".css"
	case kind == "" && ext == "":
		base += extByType[mt]
	}
	if len(base) > 120 { // keep file names within file system limits
		e := path.Ext(base)
		if len(e) > 10 {
			e = ""
		}
		sum := sha1.Sum([]byte(base))
		base = fmt.Sprintf("%s_%x%s", base[:80], sum[:4], e)
	}
	if !strings.EqualFold(u.Host, root.Host) {
		dir = path.Join("/", strings.ReplaceAll(strings.ToLower(u.Host), ":", "_"), dir) + "/"
	}
	return filepath.Join(outDir, filepath.FromSlash(dir), base)
}

// passesFilters applies --reject and --exclude.
func (m *mirror) passesFilters(u *url.URL) bool {
	lp := strings.ToLower(u.Path)
	for _, s := range m.reject {
		if strings.HasSuffix(lp, "."+s) {
			return false
		}
	}
	p := path.Clean("/" + u.Path)
	for _, x := range m.exclude {
		if p == x || strings.HasPrefix(p, x+"/") {
			return false
		}
	}
	return true
}

func (m *mirror) sameHost(u *url.URL) bool { return strings.EqualFold(u.Host, m.root.Host) }

// enqueue schedules a reference found in a page at the given link depth.
func (m *mirror) enqueue(base *url.URL, ref, kind string, depth int) {
	if kind == kindOther {
		return
	}
	u, ok := resolve(base, ref)
	if !ok || !m.passesFilters(u) {
		return
	}
	nav := kind == kindNav
	if nav {
		if !m.sameHost(u) {
			return
		}
		depth++
		if m.opt.Level >= 0 && depth > m.opt.Level {
			return
		}
	}
	k := urlKey(u)
	if m.seen[k] {
		return
	}
	m.seen[k] = true
	u.Fragment = ""
	m.queue = append(m.queue, item{u, depth, nav})
}

func detectKind(mt string, u *url.URL) string {
	switch mt {
	case "text/html", "application/xhtml+xml":
		return "html"
	case "text/css":
		return "css"
	case "", "application/octet-stream", "text/plain":
		switch strings.ToLower(path.Ext(u.Path)) {
		case ".html", ".htm":
			return "html"
		case ".css":
			return "css"
		}
	}
	return ""
}

// get performs a request, retrying a few times when the server asks us to slow down.
func (m *mirror) get(u *url.URL) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := newRequest(u.String())
		if err != nil {
			return nil, err
		}
		resp, err := m.client.Do(req)
		if err != nil {
			return nil, err
		}
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) && attempt < 3 {
			resp.Body.Close()
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		return resp, nil
	}
}

func (m *mirror) fetch(it item) {
	u := it.u
	resp, err := m.get(u)
	if err != nil {
		fmt.Printf("failed  %s: %v\n", u, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("skipped %s (status %s)\n", u, resp.Status)
		return
	}

	final := resp.Request.URL
	key, fkey := urlKey(u), urlKey(final)
	if fkey != key {
		// redirected: respect the filters (and stay on the host for page links)
		if !m.passesFilters(final) || (it.nav && !m.sameHost(final)) {
			return
		}
		if p, ok := m.saved[fkey]; ok {
			m.saved[key] = p
			return
		}
		m.seen[fkey] = true
	}

	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	kind := detectKind(mt, final)
	local := localPath(m.outDir, m.root, final, kind, mt)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		fmt.Printf("failed  %s: %v\n", u, err)
		return
	}
	f, err := os.Create(local)
	if err != nil {
		fmt.Printf("failed  %s: %v\n", u, err)
		return
	}
	var buf bytes.Buffer
	var w io.Writer = f
	if kind != "" {
		w = io.MultiWriter(f, &buf)
	}
	n, err := copyBody(w, resp.Body, m.opt.Rate, nil)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(local)
		fmt.Printf("failed  %s: %v\n", u, err)
		return
	}

	m.saved[key], m.saved[fkey] = local, local
	m.files++
	m.bytes += n
	fmt.Printf("saved   %s -> %s [%d bytes]\n", final, local, n)

	switch kind {
	case "html":
		m.pages = append(m.pages, page{final, local, kind})
		processHTML(buf.String(), func(ref, k string) string { m.enqueue(final, ref, k, it.depth); return ref })
	case "css":
		m.pages = append(m.pages, page{final, local, kind})
		processCSS(buf.String(), func(ref string) string { m.enqueue(final, ref, kindRes, it.depth); return ref })
	}
}

// convertRef rewrites one reference of a downloaded file so it points to the
// local copy. References that were not downloaded become absolute URLs so they
// keep working online.
func (m *mirror) convertRef(pg page, ref string) string {
	u, ok := resolve(pg.url, ref)
	if !ok {
		return ref
	}
	target, ok := m.saved[urlKey(u)]
	if !ok {
		return u.String()
	}
	rel, err := filepath.Rel(filepath.Dir(pg.path), target)
	if err != nil {
		return ref
	}
	rel = filepath.ToSlash(rel)
	out := (&url.URL{Path: rel}).String()
	if i := strings.Index(out, "/"); (i < 0 && strings.Contains(out, ":")) || (i > 0 && strings.Contains(out[:i], ":")) {
		out = "./" + out
	}
	if u.Fragment != "" {
		out += "#" + u.EscapedFragment()
	}
	return out
}

func (m *mirror) convertLinks() {
	converted := 0
	for _, pg := range m.pages {
		data, err := os.ReadFile(pg.path)
		if err != nil {
			continue
		}
		var out string
		if pg.kind == "html" {
			out = processHTML(string(data), func(ref, _ string) string { return m.convertRef(pg, ref) })
		} else {
			out = processCSS(string(data), func(ref string) string { return m.convertRef(pg, ref) })
		}
		if out != string(data) {
			if err := os.WriteFile(pg.path, []byte(out), 0o644); err == nil {
				converted++
			}
		}
	}
	fmt.Printf("converted links in %d files\n", converted)
}

func normalizeSuffixes(in []string) []string {
	var out []string
	for _, s := range in {
		s = strings.ToLower(strings.TrimLeft(strings.TrimSpace(s), "*."))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func normalizeDirs(in []string) []string {
	var out []string
	for _, d := range in {
		d = strings.Trim(strings.TrimSpace(d), "/")
		if d != "" {
			out = append(out, path.Clean("/"+d))
		}
	}
	return out
}

// runMirror downloads a whole website into a folder named after its host.
func runMirror(rawURL string, opt MirrorOptions) error {
	root, err := url.Parse(normalizeURL(rawURL))
	if err != nil || root.Host == "" {
		return fmt.Errorf("invalid URL %q", rawURL)
	}
	m := &mirror{
		opt:     opt,
		root:    root,
		client:  newClient(),
		seen:    map[string]bool{},
		saved:   map[string]string{},
		reject:  normalizeSuffixes(opt.Reject),
		exclude: normalizeDirs(opt.Exclude),
		outDir:  filepath.Join(expandHome(opt.Dir), strings.ReplaceAll(root.Host, ":", "_")),
	}
	if err := os.MkdirAll(m.outDir, 0o755); err != nil {
		return err
	}

	fmt.Printf("start at %s\n", timestamp())
	fmt.Printf("mirroring %s into %s\n", root, m.outDir)

	root.Fragment = ""
	m.seen[urlKey(root)] = true
	m.queue = append(m.queue, item{root, 0, true})
	for len(m.queue) > 0 {
		it := m.queue[0]
		m.queue = m.queue[1:]
		m.fetch(it)
	}
	if m.files == 0 {
		return fmt.Errorf("could not download anything from %s", rawURL)
	}
	if opt.Convert {
		m.convertLinks()
	}
	fmt.Printf("\nDownloaded %d files [%d bytes] from [%s]\nfinished at %s\n", m.files, m.bytes, rawURL, timestamp())
	return nil
}

package main

import (
	"bytes"
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
)

// MirrorOptions are the settings of --mirror and its companion flags.
type MirrorOptions struct {
	Dir     string   // -P
	Rate    int64    // --rate-limit
	Reject  []string // --reject / -R : file suffixes to skip
	Exclude []string // --exclude / -X : directories to skip
	Convert bool     // --convert-links
}

type page struct {
	url  *url.URL
	path string
	kind string // "html" or "css"
}

type mirror struct {
	opt     MirrorOptions
	root    *url.URL
	outDir  string
	client  *http.Client
	seen    map[string]bool   // URLs already queued/fetched
	saved   map[string]string // URL key -> local file
	queue   []*url.URL
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

// localPath decides where a downloaded URL is stored inside the mirror folder.
func localPath(outDir string, u *url.URL, kind string) string {
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
	if kind == "html" {
		if ext := strings.ToLower(path.Ext(base)); ext != ".html" && ext != ".htm" {
			base += ".html"
		}
	}
	return filepath.Join(outDir, filepath.FromSlash(dir), base)
}

func (m *mirror) allowed(u *url.URL) bool {
	if (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, m.root.Host) {
		return false
	}
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

func (m *mirror) enqueue(base *url.URL, ref string) {
	u, ok := resolve(base, ref)
	if !ok || !m.allowed(u) {
		return
	}
	k := urlKey(u)
	if m.seen[k] {
		return
	}
	m.seen[k] = true
	u.Fragment = ""
	m.queue = append(m.queue, u)
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

func (m *mirror) fetch(u *url.URL) {
	req, err := newRequest(u.String())
	if err != nil {
		fmt.Printf("skipped %s: %v\n", u, err)
		return
	}
	resp, err := m.client.Do(req)
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
		// redirected: stay on the same host and respect the filters
		if !m.allowed(final) {
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
	local := localPath(m.outDir, final, kind)
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
		processHTML(buf.String(), func(ref string) string { m.enqueue(final, ref); return ref })
	case "css":
		m.pages = append(m.pages, page{final, local, kind})
		processCSS(buf.String(), func(ref string) string { m.enqueue(final, ref); return ref })
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
		fn := func(ref string) string { return m.convertRef(pg, ref) }
		var out string
		if pg.kind == "html" {
			out = processHTML(string(data), fn)
		} else {
			out = processCSS(string(data), fn)
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
	m.queue = append(m.queue, root)
	for len(m.queue) > 0 {
		u := m.queue[0]
		m.queue = m.queue[1:]
		m.fetch(u)
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

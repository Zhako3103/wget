package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRate(t *testing.T) {
	cases := map[string]int64{"400k": 400 * 1024, "2M": 2 << 20, "100": 100, "": 0}
	for in, want := range cases {
		got, err := parseRate(in)
		if err != nil || got != want {
			t.Errorf("parseRate(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseRate("abc"); err == nil {
		t.Error("expected error for invalid rate")
	}
}

func testSite() *httptest.Server {
	mux := http.NewServeMux()
	html := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(body))
		}
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		html(`<html><head><link rel="stylesheet" href="/css/style.css"><link rel="canonical" href="/never"></head>
<body><a href="about.html">About</a> <a href="/assets/x.js">x</a> <a href="https://other.example/y">ext</a>
<img src="/img/a.jpg"><img src='img/b.png'></body></html>`)(w, r)
	})
	mux.HandleFunc("/about.html", html(`<a href="/">home</a> <a href="/#top">top</a>`))
	mux.HandleFunc("/css/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Write([]byte(`body{background:url("../img/bg.png")}`))
	})
	for _, p := range []string{"/img/a.jpg", "/img/b.png", "/img/bg.png", "/assets/x.js"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("data")) })
	}
	return httptest.NewServer(mux)
}

func TestMirror(t *testing.T) {
	srv := testSite()
	defer srv.Close()
	dir := t.TempDir()
	err := runMirror(srv.URL, MirrorOptions{Dir: dir, Reject: []string{"jpg"}, Exclude: []string{"/assets"}, Convert: true, Level: -1})
	if err != nil {
		t.Fatal(err)
	}
	host := strings.ReplaceAll(strings.TrimPrefix(srv.URL, "http://"), ":", "_")
	root := filepath.Join(dir, host)
	for _, f := range []string{"index.html", "about.html", "css/style.css", "img/b.png", "img/bg.png"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	for _, f := range []string{"img/a.jpg", "assets/x.js", "never"} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			t.Errorf("%s should not have been downloaded", f)
		}
	}
	idx, _ := os.ReadFile(filepath.Join(root, "index.html"))
	for _, want := range []string{`href="css/style.css"`, `href="about.html"`, `src='img/b.png'`, srv.URL + "/img/a.jpg", "https://other.example/y"} {
		if !strings.Contains(string(idx), want) {
			t.Errorf("index.html missing %q:\n%s", want, idx)
		}
	}
	css, _ := os.ReadFile(filepath.Join(root, "css/style.css"))
	if !strings.Contains(string(css), `url("../img/bg.png")`) {
		t.Errorf("css not converted correctly: %s", css)
	}
	about, _ := os.ReadFile(filepath.Join(root, "about.html"))
	if !strings.Contains(string(about), `href="index.html"`) || !strings.Contains(string(about), `href="index.html#top"`) {
		t.Errorf("about.html not converted: %s", about)
	}
}

// Images/styles served from another host (like Wikipedia's upload.wikimedia.org)
// must be downloaded, while <a> links to other hosts must not be followed.
func TestMirrorCrossHostAssets(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/style"):
			w.Header().Set("Content-Type", "text/css")
			w.Write([]byte(`body{background:url(/bg)}`))
		case r.URL.Path == "/bg" || r.URL.Path == "/logo" || r.URL.Path == "/logo2x":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("png"))
		case r.URL.Path == "/page":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("should not be followed"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdn.Close()
	long := strings.Repeat("x", 300)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<link rel="stylesheet" href="` + cdn.URL + `/style?a=` + long + `">
<img src="` + cdn.URL + `/logo" srcset="` + cdn.URL + `/logo 1x, ` + cdn.URL + `/logo2x 2x">
<a href="` + cdn.URL + `/page">other host</a> <a href="/two.html">two</a>`))
		case "/two.html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<img src="` + cdn.URL + `/logo"><a href="/three.html">3</a>`))
		case "/three.html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("three"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()

	count := func(root string) (n int, names []string) {
		filepath.Walk(root, func(p string, i os.FileInfo, err error) error {
			if err == nil && !i.IsDir() {
				n++
				names = append(names, strings.TrimPrefix(p, root))
				if len(i.Name()) > 150 {
					t.Errorf("file name too long: %d", len(i.Name()))
				}
			}
			return nil
		})
		return
	}

	// level 0: only the page + its requisites
	dir := t.TempDir()
	if err := runMirror(site.URL, MirrorOptions{Dir: dir, Convert: true, Level: 0}); err != nil {
		t.Fatal(err)
	}
	host := strings.ReplaceAll(strings.TrimPrefix(site.URL, "http://"), ":", "_")
	cdnHost := strings.ReplaceAll(strings.TrimPrefix(cdn.URL, "http://"), ":", "_")
	n, names := count(filepath.Join(dir, host))
	if _, err := os.Stat(filepath.Join(dir, host, "two.html")); err == nil {
		t.Error("two.html must not be downloaded with Level 0")
	}
	// index.html + stylesheet + bg + logo + logo2x = 5 files
	if n != 5 {
		t.Errorf("expected 5 files, got %d: %v", n, names)
	}
	idx, _ := os.ReadFile(filepath.Join(dir, host, "index.html"))
	if !strings.Contains(string(idx), cdnHost+"/logo.png") || !strings.Contains(string(idx), cdnHost+"/logo2x.png 2x") {
		t.Errorf("srcset/img not converted:\n%s", idx)
	}
	if !strings.Contains(string(idx), cdn.URL+"/page") {
		t.Errorf("link to another host must stay absolute:\n%s", idx)
	}
	if !strings.Contains(string(idx), ".css") {
		t.Errorf("style sheet should have a .css name:\n%s", idx)
	}

	// level 1: two.html is downloaded, three.html is not
	dir = t.TempDir()
	if err := runMirror(site.URL, MirrorOptions{Dir: dir, Level: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, host, "two.html")); err != nil {
		t.Error("two.html should be downloaded with Level 1")
	}
	if _, err := os.Stat(filepath.Join(dir, host, "three.html")); err == nil {
		t.Error("three.html must not be downloaded with Level 1")
	}
}

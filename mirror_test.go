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
	err := runMirror(srv.URL, MirrorOptions{Dir: dir, Reject: []string{"jpg"}, Exclude: []string{"/assets"}, Convert: true})
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

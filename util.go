package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	timeLayout = "2006-01-02 15:04:05"
	userAgent  = "Wget/1.21.4"
)

func timestamp() string { return time.Now().Format(timeLayout) }

// newClient returns an HTTP client suitable for large downloads: there is no
// overall timeout, only timeouts for connecting and receiving the headers.
func newClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}
}

func newRequest(rawURL string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	return req, nil
}

// normalizeURL adds a scheme when the user typed a bare host.
func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return raw
}

// expandHome expands a leading "~" to the user's home directory (the shell
// does not do it for values like -P=~/Downloads).
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// fileNameFromURL derives the local file name from the URL path.
func fileNameFromURL(u *url.URL) string {
	name := path.Base(u.Path)
	if name == "." || name == "/" || name == "" {
		return "index.html"
	}
	return name
}

// parseRate parses values such as "400k", "2M", "1.5m" or "100000" into bytes/s.
func parseRate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'k', 'K':
		mult, s = 1024, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1024*1024, s[:len(s)-1]
	case 'g', 'G':
		mult, s = 1024*1024*1024, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid rate limit")
	}
	return int64(v * mult), nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// humanSize renders the content length rounded to MB or GB, e.g. "~0.06MB".
func humanSize(n int64) string {
	if n >= 1e9 {
		return fmt.Sprintf("~%.2fGB", float64(n)/1e9)
	}
	return fmt.Sprintf("~%.2fMB", float64(n)/1e6)
}

// byteUnit picks KiB or MiB depending on the size.
func byteUnit(n int64) (float64, string) {
	if n >= 1<<20 {
		return 1 << 20, "MiB"
	}
	return 1 << 10, "KiB"
}

func fmtDuration(d time.Duration) string {
	secs := int(d.Seconds() + 0.5)
	h, m, s := secs/3600, (secs%3600)/60, secs%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// copyBody copies src to dst. When rate > 0 it throttles the transfer to at
// most rate bytes per second. onProgress (optional) is called after each chunk
// with the number of bytes copied so far.
func copyBody(dst io.Writer, src io.Reader, rate int64, onProgress func(done int64)) (int64, error) {
	bufSize := 32 * 1024
	if rate > 0 && rate < int64(bufSize) {
		bufSize = int(rate)
		if bufSize < 512 {
			bufSize = 512
		}
	}
	buf := make([]byte, bufSize)
	start := time.Now()
	var done int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if rate > 0 {
				expected := time.Duration(float64(done) / float64(rate) * float64(time.Second))
				if wait := expected - time.Since(start); wait > 0 {
					time.Sleep(wait)
				}
			}
			if onProgress != nil {
				onProgress(done)
			}
		}
		if err == io.EOF {
			return done, nil
		}
		if err != nil {
			return done, err
		}
	}
}

package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// downloadMulti reads URLs (one per line) from listFile and downloads all of
// them concurrently (-i flag).
func downloadMulti(listFile string, opt Options) error {
	data, err := os.ReadFile(listFile)
	if err != nil {
		return err
	}
	var urls []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, normalizeURL(line))
	}
	if len(urls) == 0 {
		return fmt.Errorf("no URLs found in %s", listFile)
	}

	fmt.Printf("start at %s\n", timestamp())
	client := newClient()

	type job struct {
		url  string
		resp *http.Response
		name string
		err  error
	}
	jobs := make([]*job, len(urls))

	// Phase 1: send all requests concurrently and wait for the headers.
	var wg sync.WaitGroup
	for i, raw := range urls {
		jobs[i] = &job{url: raw}
		wg.Add(1)
		go func(j *job) {
			defer wg.Done()
			req, err := newRequest(j.url)
			if err != nil {
				j.err = err
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				j.err = err
				return
			}
			if resp.StatusCode != http.StatusOK {
				j.err = fmt.Errorf("status %s", resp.Status)
				resp.Body.Close()
				return
			}
			j.resp = resp
		}(jobs[i])
	}
	wg.Wait()

	sizes := make([]string, len(jobs))
	used := map[string]int{}
	for i, j := range jobs {
		if j.err != nil {
			sizes[i] = "error"
			continue
		}
		sizes[i] = fmt.Sprint(j.resp.ContentLength)
		u, _ := url.Parse(j.url)
		name := fileNameFromURL(u)
		if n := used[name]; n > 0 { // avoid overwriting files with equal names
			ext := filepath.Ext(name)
			name = fmt.Sprintf("%s_%d%s", strings.TrimSuffix(name, ext), n+1, ext)
		}
		used[fileNameFromURL(u)]++
		j.name = name
	}
	fmt.Printf("content size: [%s]\n", strings.Join(sizes, ", "))

	// Phase 2: download every body at the same time.
	var mu sync.Mutex
	for _, j := range jobs {
		if j.err != nil {
			continue
		}
		wg.Add(1)
		go func(j *job) {
			defer wg.Done()
			defer j.resp.Body.Close()
			_, real := targetPaths(opt, j.name)
			err := os.MkdirAll(filepath.Dir(real), 0o755)
			if err == nil {
				var f *os.File
				if f, err = os.Create(real); err == nil {
					_, err = copyBody(f, j.resp.Body, opt.Rate, nil)
					if cerr := f.Close(); err == nil {
						err = cerr
					}
					if err != nil {
						os.Remove(real)
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				j.err = err
				return
			}
			fmt.Printf("finished %s\n", j.name)
		}(j)
	}
	wg.Wait()

	var ok []string
	failed := 0
	for _, j := range jobs {
		if j.err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "failed %s: %v\n", j.url, j.err)
			continue
		}
		ok = append(ok, j.url)
	}
	fmt.Println("\nDownload finished: ", ok)
	fmt.Printf("finished at %s\n", timestamp())
	if failed > 0 {
		return fmt.Errorf("%d of %d downloads failed", failed, len(jobs))
	}
	return nil
}

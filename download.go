package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Options holds the settings shared by the download modes.
type Options struct {
	Output string // -O: file name to save as
	Dir    string // -P: directory to save into
	Rate   int64  // --rate-limit in bytes/s (0 = unlimited)
	Quiet  bool   // no progress bar (used for the background log)
}

func targetPaths(opt Options, name string) (display, real string) {
	if filepath.IsAbs(name) {
		return name, name
	}
	if opt.Dir == "" {
		return "./" + name, name
	}
	return filepath.Join(opt.Dir, name), filepath.Join(expandHome(opt.Dir), name)
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// renderBar builds the progress line:
//
//	55.05 KiB / 55.05 KiB [=====...=====] 100.00% 1.24 MiB/s 0s
func renderBar(done, total int64, elapsed time.Duration) string {
	const width = 50
	ref := total
	if ref < 0 {
		ref = done
	}
	div, unit := byteUnit(ref)
	secs := elapsed.Seconds()
	if secs <= 0 {
		secs = 1e-9
	}
	speed := float64(done) / secs
	sdiv, sunit := byteUnit(int64(speed))

	if total < 0 {
		return fmt.Sprintf(" %.2f %s / ? [%s] ?%% %.2f %s/s ?   ",
			float64(done)/div, unit, strings.Repeat("-", width), speed/sdiv, sunit)
	}
	pct := 1.0
	if total > 0 {
		pct = float64(done) / float64(total)
	}
	filled := int(pct * width)
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", width-filled)
	if filled > 0 && filled < width {
		bar = bar[:filled-1] + ">" + bar[filled:]
	}
	var eta time.Duration
	if speed > 0 && done < total {
		eta = time.Duration(float64(total-done) / speed * float64(time.Second))
	}
	return fmt.Sprintf(" %.2f %s / %.2f %s [%s] %.2f%% %.2f %s/s %s   ",
		float64(done)/div, unit, float64(total)/div, unit, bar, pct*100, speed/sdiv, sunit, fmtDuration(eta))
}

// downloadSingle implements the normal wget usage (plus -O, -P and --rate-limit).
func downloadSingle(rawURL string, opt Options) error {
	rawURL = normalizeURL(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %v", err)
	}

	fmt.Printf("start at %s\n", timestamp())
	req, err := newRequest(rawURL)
	if err != nil {
		return err
	}
	fmt.Print("sending request, awaiting response... ")
	resp, err := newClient().Do(req)
	if err != nil {
		fmt.Println("failed")
		return err
	}
	defer resp.Body.Close()
	fmt.Printf("status %s\n", resp.Status)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download aborted: server responded with status %s", resp.Status)
	}

	total := resp.ContentLength
	if total >= 0 {
		fmt.Printf("content size: %d [%s]\n", total, humanSize(total))
	} else {
		fmt.Println("content size: unknown")
	}

	name := opt.Output
	if name == "" {
		name = fileNameFromURL(u)
	}
	display, real := targetPaths(opt, name)
	fmt.Printf("saving file to: %s\n", display)

	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		return err
	}
	f, err := os.Create(real)
	if err != nil {
		return err
	}

	start := time.Now()
	var onProgress func(int64)
	live := !opt.Quiet && isTerminal(os.Stdout)
	if live {
		var last time.Time
		onProgress = func(done int64) {
			if time.Since(last) < 100*time.Millisecond {
				return
			}
			last = time.Now()
			fmt.Print("\r" + renderBar(done, total, time.Since(start)))
		}
	}
	done, err := copyBody(f, resp.Body, opt.Rate, onProgress)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if live {
			fmt.Println()
		}
		os.Remove(real)
		return fmt.Errorf("download failed: %v", err)
	}

	if !opt.Quiet {
		final := total
		if final < 0 {
			final = done
		}
		if live {
			fmt.Print("\r")
		}
		fmt.Print(renderBar(done, final, time.Since(start)))
		fmt.Print("\n\n")
	}
	fmt.Printf("Downloaded [%s]\nfinished at %s\n", rawURL, timestamp())
	return nil
}

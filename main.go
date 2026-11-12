package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
)

const bgChildEnv = "WGET_BG_CHILD"

func usage() {
	fmt.Fprint(os.Stderr, `Usage: go run . [OPTIONS] URL...

Options:
  -O=NAME            save the file under a different name
  -P=DIR             save the file(s) into DIR
  -B                 download in the background, output goes to "wget-log"
  -i=FILE            download all the links listed in FILE (asynchronously)
  --rate-limit=RATE  limit the download speed, e.g. 400k or 2M
  --mirror           mirror a whole website into a folder named after its host
  -R, --reject=LIST  (mirror) comma separated file suffixes to skip, e.g. jpg,gif
  -X, --exclude=LIST (mirror) comma separated directories to skip, e.g. /js,/assets
  --convert-links    (mirror) convert links so the copy can be viewed offline
  -l, --level=N      (mirror) follow links at most N levels deep (default: unlimited,
                     0 = only the given page with its images, styles and scripts)
`)
}

func main() {
	fs := flag.NewFlagSet("wget", flag.ContinueOnError)
	fs.Usage = usage
	var (
		output, dir, rateStr, inputFile string
		reject, exclude                 string
		background, doMirror, convert   bool
		level                           int
	)
	fs.StringVar(&output, "O", "", "")
	fs.StringVar(&dir, "P", "", "")
	fs.StringVar(&rateStr, "rate-limit", "", "")
	fs.StringVar(&inputFile, "i", "", "")
	fs.BoolVar(&background, "B", false, "")
	fs.BoolVar(&doMirror, "mirror", false, "")
	fs.BoolVar(&convert, "convert-links", false, "")
	fs.IntVar(&level, "level", -1, "")
	fs.IntVar(&level, "l", -1, "")
	fs.StringVar(&reject, "reject", "", "")
	fs.StringVar(&reject, "R", "", "")
	fs.StringVar(&exclude, "exclude", "", "")
	fs.StringVar(&exclude, "X", "", "")

	// Parse flags that may appear before, between or after the URLs.
	var urls []string
	args := os.Args[1:]
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				os.Exit(0)
			}
			os.Exit(2)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		urls = append(urls, rest[0])
		args = rest[1:]
	}

	if len(urls) == 0 && inputFile == "" {
		usage()
		os.Exit(1)
	}
	rate, err := parseRate(rateStr)
	if err != nil {
		fail(fmt.Errorf("--rate-limit=%q: %v (use e.g. 400k or 2M)", rateStr, err))
	}

	child := os.Getenv(bgChildEnv) != ""
	if background && !child {
		if err := startBackground(); err != nil {
			fail(err)
		}
		return
	}

	opt := Options{Output: output, Dir: dir, Rate: rate, Quiet: child}
	var runErr error
	switch {
	case doMirror:
		mopt := MirrorOptions{Dir: dir, Rate: rate, Reject: splitList(reject), Exclude: splitList(exclude), Convert: convert, Level: level}
		for _, u := range urls {
			if err := runMirror(u, mopt); err != nil {
				runErr = err
			}
		}
	case inputFile != "":
		runErr = downloadMulti(inputFile, opt)
	default:
		for _, u := range urls {
			if err := downloadSingle(u, opt); err != nil {
				runErr = err
			}
		}
	}
	if runErr != nil {
		fail(runErr)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// startBackground re-launches this program detached from the terminal, with
// its output redirected to the "wget-log" file.
func startBackground() error {
	logFile, err := os.Create("wget-log")
	if err != nil {
		return err
	}
	defer logFile.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), bgChildEnv+"=1")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	fmt.Println(`Output will be written to "wget-log".`)
	return cmd.Process.Release()
}

# wget clone (Go)

Standard library only. Run with `go run . [flags] URL` or build with `go build -o wget .`

| Flag | Description |
|------|-------------|
| `-O=name` | save under a different name |
| `-P=dir` | save into a directory (`~` is expanded) |
| `-B` | background download, log in `wget-log` |
| `--rate-limit=400k` / `2M` | limit download speed |
| `-i=file` | download all links from a file concurrently |
| `--mirror` | mirror a site into a folder named after the host |
| `-R` / `--reject=jpg,gif` | (mirror) skip file suffixes |
| `-X` / `--exclude=/js,/assets` | (mirror) skip directories |
| `--convert-links` | (mirror) rewrite links for offline viewing |
| `-l` / `--level=N` | (mirror) link depth; `0` = only the page + its images/CSS/JS |

Run tests: `go test ./...`

## Offline copy of a single page (e.g. Wikipedia)

    ./mywget --mirror --convert-links --level=0 https://en.wikipedia.org/wiki/Main_Page

Images, style sheets and scripts are downloaded from any host (stored in
`<site>/<other-host>/...`), while links to other pages are followed only on the
site's own host. Open `en.wikipedia.org/wiki/Main_Page.html` in a browser.

Do not name the binary `wget` inside the project folder if you download URLs
ending in `/wget` - build it as `go build -o mywget .`

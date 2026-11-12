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

Run tests: `go test ./...`

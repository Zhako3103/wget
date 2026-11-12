package main

import (
	"html"
	"regexp"
	"strings"
)

// The mirror only needs to find (and optionally rewrite) the URLs referenced
// by <a>, <link>, <img> (and <script>) tags through their href/src attributes,
// plus url(...) / @import references inside CSS. A small regexp based scanner
// keeps the project dependency free (standard library only).
var (
	tagRe       = regexp.MustCompile(`(?is)<(a|link|img|script)\b[^>]*>`)
	attrRe      = regexp.MustCompile(`(?is)(?:^|[\s"'/])(?:href|src)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	relRe       = regexp.MustCompile(`(?is)\brel\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	styleRe     = regexp.MustCompile(`(?is)(<style\b[^>]*>)(.*?)(</style>)`)
	cssURLRe    = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s'"]+))\s*\)`)
	cssImportRe = regexp.MustCompile(`(?i)@import\s+(?:"([^"]+)"|'([^']+)')`)
)

// link relations that are not page resources and should not be fetched.
var skipRels = []string{"canonical", "alternate", "dns-prefetch", "preconnect", "prefetch", "shortlink", "pingback", "author"}

// rewriteRefs calls fn on the first participating capture group of every match
// of re and substitutes the result in place.
func rewriteRefs(s string, re *regexp.Regexp, fn func(string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		for g := 1; g < len(m)/2; g++ {
			if m[2*g] < 0 {
				continue
			}
			start, end := m[2*g], m[2*g+1]
			b.WriteString(s[last:start])
			b.WriteString(fn(s[start:end]))
			last = end
			break
		}
	}
	b.WriteString(s[last:])
	return b.String()
}

// processHTML applies fn to every reference found in the document and returns
// the (possibly rewritten) document. fn receives the unescaped attribute value.
func processHTML(s string, fn func(string) string) string {
	s = tagRe.ReplaceAllStringFunc(s, func(tag string) string {
		name := strings.ToLower(tagRe.FindStringSubmatch(tag)[1])
		if name == "link" {
			if m := relRe.FindStringSubmatch(tag); m != nil {
				rel := strings.ToLower(m[1] + m[2] + m[3])
				for _, skip := range skipRels {
					if strings.Contains(rel, skip) {
						return tag
					}
				}
			}
		}
		return rewriteRefs(tag, attrRe, func(val string) string {
			raw := html.UnescapeString(val)
			nv := fn(raw)
			if nv == raw {
				return val
			}
			return html.EscapeString(nv)
		})
	})
	return styleRe.ReplaceAllStringFunc(s, func(blk string) string {
		m := styleRe.FindStringSubmatch(blk)
		return m[1] + processCSS(m[2], fn) + m[3]
	})
}

// processCSS applies fn to every url(...) and @import reference in a style sheet.
func processCSS(s string, fn func(string) string) string {
	s = rewriteRefs(s, cssURLRe, fn)
	return rewriteRefs(s, cssImportRe, fn)
}

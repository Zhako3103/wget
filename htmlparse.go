package main

import (
	"html"
	"regexp"
	"strings"
)

// The mirror only needs to find (and optionally rewrite) the URLs referenced
// by <a>, <link>, <img> (and <script>/<source>) tags through their href/src/
// srcset attributes, plus url(...) / @import references inside CSS. A small
// regexp based scanner keeps the project dependency free (standard library only).
var (
	tagRe       = regexp.MustCompile(`(?is)<(a|link|img|script|source)\b[^>]*>`)
	attrRe      = regexp.MustCompile(`(?is)(?:^|[\s"'/])(?:href|src)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	srcsetRe    = regexp.MustCompile(`(?is)(?:^|[\s"'/])srcset\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	relRe       = regexp.MustCompile(`(?is)\brel\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	styleRe     = regexp.MustCompile(`(?is)(<style\b[^>]*>)(.*?)(</style>)`)
	cssURLRe    = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s'"]+))\s*\)`)
	cssImportRe = regexp.MustCompile(`(?i)@import\s+(?:"([^"]+)"|'([^']+)')`)
)

// Kinds of references reported to the callback of processHTML.
const (
	kindNav   = "nav"   // <a href>: a link to another page (followed only on the same host)
	kindRes   = "res"   // page requisite: image, style sheet, script, icon (any host)
	kindOther = "other" // <link> with another meaning (canonical, next, search...): not downloaded
)

// link relations that are real page resources.
var resRels = []string{"stylesheet", "icon", "preload", "apple-touch"}

func linkKind(tag string) string {
	if m := relRe.FindStringSubmatch(tag); m != nil {
		rel := strings.ToLower(m[1] + m[2] + m[3])
		for _, r := range resRels {
			if strings.Contains(rel, r) {
				return kindRes
			}
		}
	}
	return kindOther
}

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
// the (possibly rewritten) document. fn receives the unescaped attribute value
// and the kind of the reference.
func processHTML(s string, fn func(ref, kind string) string) string {
	s = tagRe.ReplaceAllStringFunc(s, func(tag string) string {
		name := strings.ToLower(tagRe.FindStringSubmatch(tag)[1])
		kind := kindRes
		switch name {
		case "a":
			kind = kindNav
		case "link":
			kind = linkKind(tag)
		}
		tag = rewriteRefs(tag, attrRe, func(val string) string {
			raw := html.UnescapeString(val)
			nv := fn(raw, kind)
			if nv == raw {
				return val
			}
			return html.EscapeString(nv)
		})
		return rewriteRefs(tag, srcsetRe, func(val string) string {
			raw := html.UnescapeString(val)
			var parts []string
			for _, cand := range strings.Split(raw, ",") {
				f := strings.Fields(cand)
				if len(f) == 0 {
					continue
				}
				f[0] = fn(f[0], kindRes)
				parts = append(parts, strings.Join(f, " "))
			}
			nv := strings.Join(parts, ", ")
			if nv == raw {
				return val
			}
			return html.EscapeString(nv)
		})
	})
	return styleRe.ReplaceAllStringFunc(s, func(blk string) string {
		m := styleRe.FindStringSubmatch(blk)
		return m[1] + processCSS(m[2], func(r string) string { return fn(r, kindRes) }) + m[3]
	})
}

// processCSS applies fn to every url(...) and @import reference in a style sheet.
func processCSS(s string, fn func(string) string) string {
	s = rewriteRefs(s, cssURLRe, fn)
	return rewriteRefs(s, cssImportRe, fn)
}

// Package normalize turns raw HTML into a stable text representation so that
// cosmetic changes (whitespace, scripts, boilerplate) don't trigger false
// positives.
package normalize

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// dropTags are elements whose contents carry no meaningful signal for change
// detection in this MVP.
var dropTags = map[string]bool{
	"script":   true,
	"style":    true,
	"noscript": true,
	"nav":      true,
	"header":   true,
	"footer":   true,
	"svg":      true,
	"form":     true,
	"iframe":   true,
}

// Text parses htmlBytes and returns normalized visible text: tag/boilerplate
// stripped, runs of whitespace collapsed to single spaces, lines trimmed.
func Text(htmlBytes []byte) string {
	doc, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		// Fall back to a crude tag-strip if the parser chokes.
		return stripTags(string(htmlBytes))
	}

	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && dropTags[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			s := strings.TrimSpace(n.Data)
			if s != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(s)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	return collapseWhitespace(b.String())
}

func collapseWhitespace(s string) string {
	// Collapse runs of spaces/tabs within a line; lines already split on \n.
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		lines[i] = strings.Join(fields, " ")
	}
	return strings.Join(lines, "\n")
}

func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return collapseWhitespace(b.String())
}
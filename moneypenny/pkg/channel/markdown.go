package channel

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// markdownToTeamsHTML converts a subset of Markdown to the HTML subset that
// Microsoft Teams renders (contentType=html). Teams supports <b>, <i>,
// <strike>, <ul>/<ol>/<li>, <pre>, <blockquote>, <a>, <br>, <p> and
// <codeblock class><code> — but not <h1>-<h6> or <div>. Agent output is plain
// Markdown, so we translate:
//   - headings (#..) -> bold paragraph (Teams has no heading tags)
//   - **bold**/__bold__, *italic*/_italic_, ~~strike~~
//   - `inline code` -> <code>, fenced ``` blocks -> <pre>
//   - > quotes -> <blockquote>
//   - -/*/+ and 1. lists -> <ul>/<ol>
//   - [text](url) links
//
// All literal text is HTML-escaped; content inside code spans/blocks is escaped
// but not otherwise formatted.
func markdownToTeamsHTML(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	var blocks []string

	i := 0
	for i < len(lines) {
		line := lines[i]

		// Fenced code block.
		if fence := fenceMarker(line); fence != "" {
			var body []string
			i++
			for i < len(lines) && fenceMarker(lines[i]) == "" {
				body = append(body, lines[i])
				i++
			}
			if i < len(lines) { // consume closing fence
				i++
			}
			escaped := html.EscapeString(strings.Join(body, "\n"))
			escaped = strings.ReplaceAll(escaped, "\n", "<br>")
			blocks = append(blocks, "<pre>"+escaped+"</pre>")
			continue
		}

		// Blank line: paragraph separator.
		if strings.TrimSpace(line) == "" {
			i++
			continue
		}

		// Blockquote: consecutive "> " lines.
		if isQuote(line) {
			var qs []string
			for i < len(lines) && isQuote(lines[i]) {
				qs = append(qs, inlineMarkdown(stripQuote(lines[i])))
				i++
			}
			blocks = append(blocks, "<blockquote>"+strings.Join(qs, "<br>")+"</blockquote>")
			continue
		}

		// Lists: consecutive list items of the same kind.
		if ordered, ok := listItem(line); ok {
			tag := "ul"
			if ordered {
				tag = "ol"
			}
			var items []string
			for i < len(lines) {
				o, ok := listItem(lines[i])
				if !ok || o != ordered {
					break
				}
				items = append(items, "<li>"+inlineMarkdown(stripListMarker(lines[i]))+"</li>")
				i++
			}
			blocks = append(blocks, "<"+tag+">"+strings.Join(items, "")+"</"+tag+">")
			continue
		}

		// Heading: render as a bold paragraph (Teams has no heading tags).
		if h := headingText(line); h != "" {
			blocks = append(blocks, "<p><b>"+inlineMarkdown(h)+"</b></p>")
			i++
			continue
		}

		// Paragraph: gather consecutive plain lines.
		var para []string
		for i < len(lines) {
			l := lines[i]
			if strings.TrimSpace(l) == "" || fenceMarker(l) != "" || isQuote(l) ||
				headingText(l) != "" {
				break
			}
			if _, ok := listItem(l); ok {
				break
			}
			para = append(para, inlineMarkdown(l))
			i++
		}
		blocks = append(blocks, "<p>"+strings.Join(para, "<br>")+"</p>")
	}

	return strings.Join(blocks, "")
}

func fenceMarker(line string) string {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "```") {
		return "```"
	}
	if strings.HasPrefix(t, "~~~") {
		return "~~~"
	}
	return ""
}

func isQuote(line string) bool {
	t := strings.TrimLeft(line, " ")
	return strings.HasPrefix(t, ">")
}

func stripQuote(line string) string {
	t := strings.TrimLeft(line, " ")
	t = strings.TrimPrefix(t, ">")
	return strings.TrimPrefix(t, " ")
}

var headingRe = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)

func headingText(line string) string {
	m := headingRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1]
}

var (
	ulRe = regexp.MustCompile(`^\s*[-*+]\s+`)
	olRe = regexp.MustCompile(`^\s*\d+[.)]\s+`)
)

// listItem reports whether line is a list item and, if so, whether it is
// ordered.
func listItem(line string) (ordered bool, ok bool) {
	if olRe.MatchString(line) {
		return true, true
	}
	if ulRe.MatchString(line) {
		return false, true
	}
	return false, false
}

func stripListMarker(line string) string {
	if olRe.MatchString(line) {
		return olRe.ReplaceAllString(line, "")
	}
	return ulRe.ReplaceAllString(line, "")
}

var (
	inlineCodeRe = regexp.MustCompile("`([^`]+)`")
	boldStarRe   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	boldUnderRe  = regexp.MustCompile(`__(.+?)__`)
	strikeRe     = regexp.MustCompile(`~~(.+?)~~`)
	italicStarRe = regexp.MustCompile(`\*(.+?)\*`)
	italicUnderRe = regexp.MustCompile(`(^|[^\w])_(.+?)_($|[^\w])`)
	linkRe       = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

const codePlaceholder = "\x00C%d\x00"

// inlineMarkdown formats inline Markdown within a single line and HTML-escapes
// all literal text. Inline code spans are extracted first so their contents are
// not treated as formatting, then restored as <code> after escaping.
func inlineMarkdown(s string) string {
	// Extract inline code spans before escaping so backticked content is left
	// verbatim (escaped) and not re-parsed for bold/italic.
	var codes []string
	s = inlineCodeRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := inlineCodeRe.FindStringSubmatch(m)[1]
		codes = append(codes, html.EscapeString(inner))
		ph := strings.Replace(codePlaceholder, "%d", strconv.Itoa(len(codes)-1), 1)
		return ph
	})

	s = html.EscapeString(s)

	// Links: [text](url). Escape the url minimally (already escaped by the pass
	// above except quotes handled by EscapeString).
	s = linkRe.ReplaceAllString(s, `<a href="$2">$1</a>`)

	s = boldStarRe.ReplaceAllString(s, "<b>$1</b>")
	s = boldUnderRe.ReplaceAllString(s, "<b>$1</b>")
	s = strikeRe.ReplaceAllString(s, "<strike>$1</strike>")
	s = italicStarRe.ReplaceAllString(s, "<i>$1</i>")
	s = italicUnderRe.ReplaceAllString(s, "$1<i>$2</i>$3")

	// Restore code spans.
	for idx, c := range codes {
		ph := strings.Replace(codePlaceholder, "%d", strconv.Itoa(idx), 1)
		s = strings.Replace(s, ph, "<code>"+c+"</code>", 1)
	}
	return s
}

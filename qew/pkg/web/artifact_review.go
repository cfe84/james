package web

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"james/moneypenny/pkg/artifact"
)

const (
	maxArtifactReviewDocument = 4 << 20
	maxArtifactReviewComments = 20
	maxArtifactReviewField    = 4000 // UTF-8 bytes
)

//go:embed artifact-viewer.html
var artifactViewerHTML string

var artifactViewer = template.Must(template.New("artifact-viewer").Parse(artifactViewerHTML))
var artifactReviewOperationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func artifactReviewable(format string) bool {
	switch format {
	case "markdown", "text", "html", "zip":
		return true
	}
	return false
}

func canReviewArtifact(meta artifact.Metadata) bool {
	format := artifact.ViewFormat(meta.Name)
	return artifactReviewable(format) && (format != "zip" || meta.View == "zip")
}

func (s *Server) serveArtifactViewer(w http.ResponseWriter, session, id string) {
	chunk, err := s.artifactChunk(session, id, 0, true)
	if err != nil {
		http.Error(w, "Artifact is unavailable", http.StatusBadGateway)
		return
	}
	base := "/artifacts/" + url.PathEscape(session) + "/" + url.PathEscape(id)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; frame-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if err := artifactViewer.Execute(w, struct {
		Base                  string
		Reviewable            bool
		MaxBytes, MaxComments int
	}{base, canReviewArtifact(chunk.Artifact), maxArtifactReviewField, maxArtifactReviewComments}); err != nil {
		s.vlog.Printf("artifact viewer response failed: %v", err)
	}
}

func serveArtifactReviewDocument(w http.ResponseWriter, target string, entry *artifactPreview, assetSources string) {
	in, err := os.Open(target)
	if err != nil {
		http.Error(w, "Cannot read artifact review document", http.StatusInternalServerError)
		return
	}
	defer in.Close()
	source, err := io.ReadAll(io.LimitReader(in, maxArtifactReviewDocument+1))
	if err != nil {
		http.Error(w, "Cannot read artifact review document", http.StatusInternalServerError)
		return
	}
	if len(source) > maxArtifactReviewDocument {
		http.Error(w, "This document is too large to review. You can still view or download it.", http.StatusUnprocessableEntity)
		return
	}
	var doc *html.Node
	if entry.format != "text" {
		doc, err = html.Parse(bytes.NewReader(source))
		if err != nil {
			http.Error(w, "Cannot parse artifact review document", http.StatusUnprocessableEntity)
			return
		}
	}
	markup := reviewMarkup{formatted: entry.format == "html" || entry.format == "zip", bundled: entry.format == "zip"}
	root := reviewChild(doc, "html")
	head := reviewChild(root, "head")
	body := reviewChild(root, "body")
	markup.WriteString("<!doctype html><html")
	markup.writeAttributes(root)
	markup.WriteString(`><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Review document</title>`)
	if markup.formatted {
		markup.writeChildren(head)
	} else {
		markup.WriteString(`<style>:root{color-scheme:light dark}body{max-width:85ch;margin:1.5rem auto;padding:0 1rem;font:1rem/1.6 sans-serif;overflow-wrap:anywhere}pre{white-space:pre-wrap}table{border-collapse:collapse}td,th{border:1px solid;padding:.25rem .5rem}</style>`)
	}
	markup.WriteString(`<style>::highlight(artifact-comment){background-color:#ffe287!important;color:#252014!important}</style></head><body`)
	markup.writeAttributes(body)
	fmt.Fprintf(&markup, ` data-sha="%s">`, template.HTMLEscapeString(entry.sha256))
	if entry.format == "text" {
		// The HTML parser strips the first newline following a pre start tag.
		markup.WriteString("<pre>\n")
		template.HTMLEscape(&markup, source)
		markup.WriteString("</pre>")
	} else {
		markup.writeChildren(body)
	}
	markup.WriteString("</body></html>")

	styles, images, fonts := "'unsafe-inline'", "'none'", "'none'"
	if markup.formatted {
		images, fonts = "data:", "data:"
	}
	if markup.bundled {
		styles += " " + assetSources
		images += " " + assetSources
		fonts += " " + assetSources
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox allow-same-origin; default-src 'none'; style-src "+styles+"; img-src "+images+"; font-src "+fonts+"; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
	io.WriteString(w, markup.String())
}

func reviewChild(parent *html.Node, tag string) *html.Node {
	if parent != nil {
		for node := parent.FirstChild; node != nil; node = node.NextSibling {
			if node.Type == html.ElementNode && node.Data == tag {
				return node
			}
		}
	}
	return nil
}

type reviewMarkup struct {
	strings.Builder
	formatted, bundled bool
}

func (out *reviewMarkup) writeChildren(node *html.Node) {
	if node != nil {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			out.writeNode(child)
		}
	}
}

func (out *reviewMarkup) writeNode(node *html.Node) {
	if node.Type == html.TextNode {
		out.WriteString(template.HTMLEscapeString(node.Data))
		return
	}
	if node.Type != html.ElementNode || node.Namespace != "" {
		return
	}
	switch tag := node.Data; tag {
	case "script", "template", "noscript", "iframe", "object", "title", "textarea", "select":
		return
	case "style":
		if out.formatted && node.FirstChild != nil && node.FirstChild.Type == html.TextNode {
			css := node.FirstChild.Data
			// A style element uses raw text, not HTML-escaped text.
			if !strings.Contains(strings.ToLower(css), "</style") {
				out.WriteString("<style")
				out.writeAttribute("media", node)
				out.WriteString(">" + css + "</style>")
			}
		}
	case "link":
		if out.bundled && strings.EqualFold(reviewAttribute(node, "rel"), "stylesheet") {
			if href := reviewBundleURL(reviewAttribute(node, "href")); href != "" {
				out.WriteString(`<link rel="stylesheet"`)
				out.attribute("href", href)
				out.writeAttribute("media", node)
				out.WriteByte('>')
			}
		}
	case "img":
		if !out.formatted {
			return
		}
		src := strings.TrimSpace(reviewAttribute(node, "src"))
		if !strings.HasPrefix(strings.ToLower(src), "data:image/") {
			if !out.bundled {
				src = ""
			} else {
				src = reviewBundleURL(src)
			}
		}
		out.WriteString("<img")
		if src != "" {
			out.attribute("src", src)
		}
		out.writeAttributes(node)
		out.WriteByte('>')
	case "br", "hr", "wbr", "col":
		out.WriteString("<" + tag)
		out.writeAttributes(node)
		out.WriteByte('>')
	case "article", "aside", "section", "div", "p", "h1", "h2", "h3", "h4", "h5", "h6",
		"header", "footer", "main", "nav", "span", "a", "address", "details", "summary",
		"blockquote", "pre", "code", "em", "strong", "b", "i", "s", "del", "u", "mark", "small",
		"sub", "sup", "samp", "kbd", "var", "abbr", "time", "ins",
		"ul", "ol", "li", "dl", "dt", "dd", "figure", "figcaption",
		"table", "caption", "colgroup", "thead", "tbody", "tfoot", "tr", "th", "td":
		out.WriteString("<" + tag)
		out.writeAttributes(node)
		out.WriteByte('>')
		if tag == "pre" && node.FirstChild != nil && strings.HasPrefix(node.FirstChild.Data, "\n") {
			out.WriteByte('\n')
		}
		out.writeChildren(node)
		out.WriteString("</" + tag + ">")
	default:
		out.writeChildren(node)
	}
}

func (out *reviewMarkup) writeAttributes(node *html.Node) {
	if !out.formatted || node == nil {
		return
	}
	for _, attr := range node.Attr {
		if attr.Namespace != "" {
			continue
		}
		switch attr.Key {
		case "id", "class", "style", "title", "lang", "dir", "role", "hidden",
			"alt", "width", "height", "colspan", "rowspan", "span", "scope", "headers",
			"start", "reversed", "value", "open":
			out.attribute(attr.Key, attr.Val)
		default:
			if (strings.HasPrefix(attr.Key, "aria-") || strings.HasPrefix(attr.Key, "data-")) &&
				attr.Key != "data-sha" && strings.Trim(attr.Key, "abcdefghijklmnopqrstuvwxyz0123456789-_") == "" {
				out.attribute(attr.Key, attr.Val)
			}
		}
	}
}

func reviewAttribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Namespace == "" && attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func (out *reviewMarkup) writeAttribute(key string, node *html.Node) {
	if value := reviewAttribute(node, key); value != "" {
		out.attribute(key, value)
	}
}

func (out *reviewMarkup) attribute(key, value string) {
	fmt.Fprintf(out, ` %s="%s"`, key, template.HTMLEscapeString(value))
}

func reviewBundleURL(value string) string {
	ref, err := url.Parse(strings.TrimSpace(value))
	if err != nil || ref.IsAbs() || ref.Host != "" || path.IsAbs(ref.Path) || strings.ContainsAny(ref.Path, "\\:\x00") {
		return ""
	}
	clean := path.Clean(ref.Path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return (&url.URL{Path: "./" + clean, Fragment: ref.Fragment}).String()
}

type artifactReview struct {
	SHA256         string `json:"sha256"`
	OperationID    string `json:"operation_id"`
	GeneralComment string `json:"general_comment"`
	Comments       []struct {
		Quote   string `json:"quote"`
		Comment string `json:"comment"`
	} `json:"comments"`
}

func (s *Server) submitArtifactReview(w http.ResponseWriter, r *http.Request, session, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var review artifactReview
	// JSON escaping can consume six bytes per byte of input.
	r.Body = http.MaxBytesReader(w, r.Body, 6*(maxArtifactReviewComments*2+1)*maxArtifactReviewField+4096)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&review); err != nil || dec.Decode(new(any)) != io.EOF ||
		len(review.Comments) > maxArtifactReviewComments ||
		(len(review.Comments) == 0 && strings.TrimSpace(review.GeneralComment) == "") {
		http.Error(w, "Invalid artifact review", http.StatusBadRequest)
		return
	}
	if len(review.GeneralComment) > maxArtifactReviewField {
		http.Error(w, "General comment exceeds 4000 bytes", http.StatusBadRequest)
		return
	}
	if hash, err := hex.DecodeString(review.SHA256); err != nil || len(hash) != 32 ||
		!artifactReviewOperationID.MatchString(review.OperationID) {
		http.Error(w, "Invalid artifact review identity", http.StatusBadRequest)
		return
	}
	for _, c := range review.Comments {
		if strings.TrimSpace(c.Quote) == "" || strings.TrimSpace(c.Comment) == "" ||
			len(c.Quote) > maxArtifactReviewField || len(c.Comment) > maxArtifactReviewField {
			http.Error(w, "Each quote and comment must contain 1–4000 bytes of text", http.StatusBadRequest)
			return
		}
	}
	chunk, err := s.artifactChunk(session, id, 0, true)
	if err != nil {
		http.Error(w, "Artifact is unavailable", http.StatusBadGateway)
		return
	}
	if !canReviewArtifact(chunk.Artifact) {
		http.Error(w, "This artifact cannot be reviewed", http.StatusUnsupportedMediaType)
		return
	}
	if review.SHA256 != chunk.Artifact.SHA256 {
		http.Error(w, "The artifact was updated. Copy your comments before reloading to review the latest version.", http.StatusConflict)
		return
	}
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Artifact review: %s\nArtifact ID: %s\nReviewed SHA-256: %s\n\n", chunk.Artifact.Name, id, review.SHA256)
	if general := strings.TrimSpace(review.GeneralComment); general != "" {
		fmt.Fprintf(&prompt, "### General comment\n%s\n\n", general)
	}
	for i, c := range review.Comments {
		quote := strings.ReplaceAll(strings.TrimSpace(c.Quote), "\r\n", "\n")
		quote = strings.ReplaceAll(quote, "\r", "\n")
		fmt.Fprintf(&prompt, "### Comment %d\n> %s\n\n%s\n\n", i+1, strings.ReplaceAll(quote, "\n", "\n> "), strings.TrimSpace(c.Comment))
	}
	resp, err := s.hem.Send(&Request{Verb: "continue", Noun: "session", Args: []string{
		"--async", "--operation-id", review.OperationID, "--", session, prompt.String(),
	}})
	if err != nil || resp.Status != "ok" {
		s.vlog.Printf("artifact review was not confirmed by Hem")
		http.Error(w, "The agent did not confirm receipt. Retry to send the same review.", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

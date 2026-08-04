package channel

import "testing"

func TestMarkdownToTeamsHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{"# Title", "<p><b>Title</b></p>"},
		{"## Sub title", "<p><b>Sub title</b></p>"},
		{"**bold** and *italic*", "<p><b>bold</b> and <i>italic</i></p>"},
		{"__bold__ and _italic_", "<p><b>bold</b> and <i>italic</i></p>"},
		{"~~gone~~", "<p><strike>gone</strike></p>"},
		{"use `code` here", "<p>use <code>code</code> here</p>"},
		{"> quoted line", "<blockquote>quoted line</blockquote>"},
		{"> line one\n> line two", "<blockquote>line one<br>line two</blockquote>"},
		{"- a\n- b", "<ul><li>a</li><li>b</li></ul>"},
		{"1. a\n2. b", "<ol><li>a</li><li>b</li></ol>"},
		{"[link](https://x.com)", `<p><a href="https://x.com">link</a></p>`},
		{"line one\nline two", "<p>line one<br>line two</p>"},
		{"para one\n\npara two", "<p>para one</p><p>para two</p>"},
		{"```\na < b\nc\n```", "<pre>a &lt; b<br>c</pre>"},
		{"`<b>x</b>`", "<p><code>&lt;b&gt;x&lt;/b&gt;</code></p>"},
	}
	for _, c := range cases {
		if got := markdownToTeamsHTML(c.in); got != c.want {
			t.Errorf("markdownToTeamsHTML(%q) =\n  %q\nwant\n  %q", c.in, got, c.want)
		}
	}
}

// Code spans must not be reinterpreted as formatting, and asterisks inside code
// must survive verbatim.
func TestMarkdownCodePreservesMarkers(t *testing.T) {
	got := markdownToTeamsHTML("call `a * b` now")
	want := "<p>call <code>a * b</code> now</p>"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

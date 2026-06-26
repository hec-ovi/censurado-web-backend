package content

import (
	"strings"
	"testing"
)

func TestRenderMarkdown_RendersSafeFormatting(t *testing.T) {
	out, err := RenderMarkdown("# Title\n\nSome **bold** and a [link](https://example.com).")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"<h1", "Title", "<strong>bold</strong>", `href="https://example.com"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\ngot: %s", want, out)
		}
	}
}

// The sanitizer corpus: every malicious input must produce HTML with no
// executable surface. These are the attacks an agent-authored body could carry.
func TestRenderMarkdown_NeutralizesInjection(t *testing.T) {
	cases := map[string]string{
		"raw script tag":       "Hello <script>alert(1)</script> world",
		"img onerror handler":  "![x](data:image/png;base64,xx)\n\n<img src=x onerror=alert(1)>",
		"javascript link":      "[click](javascript:alert(1))",
		"inline event handler": "<a href=\"#\" onclick=\"alert(1)\">x</a>",
		"style tag":            "<style>body{display:none}</style>text",
		"svg onload":           "<svg/onload=alert(1)>",
		"iframe":               "<iframe src=\"https://evil.example\"></iframe>",
		"data uri html":        "[x](data:text/html,<script>alert(1)</script>)",
	}
	// Raw HTML is escaped to entities (so no real <script>/<svg>/<style>/<iframe>
	// tags survive), and dangerous URL schemes are stripped. Escaped text that
	// merely contains a word like "onload" is inert, so we assert on the
	// executable forms (real tags, dangerous schemes), not on bare keywords.
	forbidden := []string{"<script", "<svg", "<iframe", "<style", "javascript:", "data:text/html"}
	for name, in := range cases {
		out, err := RenderMarkdown(in)
		if err != nil {
			t.Fatalf("%s: render: %v", name, err)
		}
		low := strings.ToLower(out)
		for _, bad := range forbidden {
			if strings.Contains(low, bad) {
				t.Errorf("%s: sanitized output still contains %q\ngot: %s", name, bad, out)
			}
		}
	}
}

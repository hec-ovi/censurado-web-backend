// Package content renders untrusted author Markdown into safe HTML. Two layers
// guard against injection: goldmark runs WITHOUT the unsafe option, so raw HTML
// in the source is escaped rather than passed through, and the result is run
// through a strict allowlist sanitizer as defense in depth. The same renderer is
// used at publish time (to reject unrenderable input) and at generation time (to
// emit page HTML), so what is validated is exactly what is served.
package content

import (
	"bytes"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// md renders GitHub-flavored Markdown. The absence of html.WithUnsafe means raw
// HTML and dangerous URLs in the source are not rendered.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// policy is the allowlist applied to the rendered HTML. UGCPolicy permits common
// formatting (headings, lists, links, code, tables) but no script, style, or
// event-handler attributes; links are marked nofollow.
var policy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.RequireNoFollowOnLinks(true)
	return p
}()

// RenderMarkdown converts Markdown into sanitized HTML that is safe to serve.
func RenderMarkdown(markdown string) (string, error) {
	var buf bytes.Buffer
	if err := md.Convert([]byte(markdown), &buf); err != nil {
		return "", err
	}
	return policy.Sanitize(buf.String()), nil
}

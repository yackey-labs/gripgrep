package parity

import (
	"fmt"
	"strings"
)

// beginMarker and endMarker are the HTML comment markers docs/rg-parity.md
// uses to bracket its two generated regions ("score" and "tables"). Every
// byte outside these markers is hand-written prose and must survive
// Splice/the drift test byte-for-byte.
func beginMarker(name string) string { return "<!-- BEGIN GENERATED: " + name + " -->" }

const endMarker = "<!-- END GENERATED -->"

// Splice replaces the content of the "score" and "tables" GENERATED
// regions in doc with the freshly generated result, leaving everything
// else (including the marker comments themselves) untouched. "score" is
// spliced inline (markers sit on the same line, mid-paragraph); "tables"
// is spliced as its own block (blank line, then content, then blank line)
// since it sits between two headings.
func Splice(doc string, result Result) (string, error) {
	doc, err := replaceRegion(doc, "score", result.ScoreLine, false)
	if err != nil {
		return "", err
	}
	doc, err = replaceRegion(doc, "tables", result.TablesMarkdown, true)
	if err != nil {
		return "", err
	}
	return doc, nil
}

func replaceRegion(doc, name, content string, block bool) (string, error) {
	begin := beginMarker(name)
	startIdx := strings.Index(doc, begin)
	if startIdx == -1 {
		return "", fmt.Errorf("marker %q not found", begin)
	}
	contentStart := startIdx + len(begin)
	endIdx := strings.Index(doc[contentStart:], endMarker)
	if endIdx == -1 {
		return "", fmt.Errorf("no matching %q after %q", endMarker, begin)
	}
	endIdx += contentStart

	replacement := content
	if block {
		replacement = "\n\n" + content + "\n\n"
	}

	return doc[:contentStart] + replacement + doc[endIdx:], nil
}

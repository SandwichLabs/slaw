package llm

import (
	"regexp"
	"strings"
)

// maskMarkdown returns text with every span a number must not be read from
// replaced by spaces of the same length: fenced and inline code, link targets,
// autolinks, bare URLs and list markers. The panel never decorates those (it
// decorates text nodes outside code and links), so a number only a code block
// holds is never marked here either.
func maskMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	fence := ""
	for i, line := range lines {
		trim := strings.TrimLeft(line, " ")
		if len(line)-len(trim) <= 3 {
			if f := fenceOf(trim); f != "" {
				switch {
				case fence == "":
					fence = f
					lines[i] = blank(line)
					continue
				case f[0] == fence[0] && len(f) >= len(fence) && strings.TrimSpace(trim[len(f):]) == "":
					fence = ""
					lines[i] = blank(line)
					continue
				}
			}
		}
		if fence != "" {
			lines[i] = blank(line)
			continue
		}
		lines[i] = maskLine(line)
	}
	return strings.Join(lines, "\n")
}

// fenceOf is the code fence a line opens or closes (three or more backticks or
// tildes), or "".
func fenceOf(trim string) string {
	if len(trim) < 3 || (trim[0] != '`' && trim[0] != '~') {
		return ""
	}
	n := 0
	for n < len(trim) && trim[n] == trim[0] {
		n++
	}
	if n < 3 {
		return ""
	}
	return trim[:n]
}

func blank(s string) string { return strings.Repeat(" ", len(s)) }

var (
	listMarkerRE = regexp.MustCompile(`^\s*(?:[-*+]|\d{1,9}[.)])\s`)
	linkRE       = regexp.MustCompile(`!?\[[^\]]*\]\([^)]*\)`) // the text and the target: the panel decorates neither
	autolinkRE   = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9+.-]*:[^>\s]*>`)
	bareURLRE    = regexp.MustCompile(`(?i)\b(?:https?|ftp)://\S+|\bwww\.\S+`)
)

// maskLine masks one line outside a fence.
func maskLine(line string) string {
	b := []byte(line)
	maskIn := func(start, end int) {
		for i := start; i < end; i++ {
			b[i] = ' '
		}
	}
	if m := listMarkerRE.FindStringIndex(line); m != nil {
		maskIn(m[0], m[1])
	}
	// Inline code first, so a backtick inside a link title cannot hide a number.
	for i := 0; i < len(b); {
		if b[i] != '`' {
			i++
			continue
		}
		n := 0
		for i+n < len(b) && b[i+n] == '`' {
			n++
		}
		closeAt := findBackticks(b, i+n, n)
		if closeAt < 0 {
			i += n
			continue
		}
		maskIn(i, closeAt+n)
		i = closeAt + n
	}
	for _, re := range []*regexp.Regexp{linkRE, autolinkRE, bareURLRE} {
		for _, m := range re.FindAllIndex(b, -1) {
			maskIn(m[0], m[1])
		}
	}
	return string(b)
}

// findBackticks is the index of the next run of exactly n backticks at or after
// from, or -1.
func findBackticks(b []byte, from, n int) int {
	for i := from; i < len(b); {
		if b[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < len(b) && b[i+run] == '`' {
			run++
		}
		if run == n {
			return i
		}
		i += run
	}
	return -1
}

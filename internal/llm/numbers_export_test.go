package llm

// Numbers returns the numbers of text, in order, by the rules of numbermatch.go,
// leaving out code, link targets, URLs and list markers. Test-only: the product
// path is MarkNumbers.
func Numbers(text string) []string {
	var out []string
	for _, n := range tokenize(maskMarkdown(text)) {
		out = append(out, n.text)
	}
	return out
}

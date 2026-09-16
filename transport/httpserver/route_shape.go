package httpserver

import (
	"fmt"
	"strings"
)

// routeShape follows chi's parameter boundaries and regexp anchoring, but
// removes parameter names. It deliberately does not compare regexp languages.
func routeShape(pattern string) (string, error) {
	var result strings.Builder
	for {
		start := strings.IndexByte(pattern, '{')
		if start < 0 {
			result.WriteString(pattern)
			return result.String(), nil
		}
		result.WriteString(pattern[:start])
		depth, end := 1, start+1
		for ; end < len(pattern) && depth > 0; end++ {
			switch pattern[end] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth != 0 {
			return "", fmt.Errorf("route parameter closing delimiter is missing in %q", pattern)
		}
		_, constraint, regexp := strings.Cut(pattern[start+1:end-1], ":")
		result.WriteByte('{')
		if regexp {
			result.WriteByte(':')
			if constraint != "" {
				if !strings.HasPrefix(constraint, "^") {
					constraint = "^" + constraint
				}
				if !strings.HasSuffix(constraint, "$") {
					constraint += "$"
				}
			}
			result.WriteString(constraint)
		}
		result.WriteByte('}')
		pattern = pattern[end:]
	}
}

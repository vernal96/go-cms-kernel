package resource

import (
	"strings"
	"unicode"
)

var russianSlugLetters = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// GenerateSlug converts Russian and Latin titles into a stable URL code.
func GenerateSlug(title string) string {
	var result strings.Builder
	separator := false
	for _, raw := range strings.ToLower(strings.TrimSpace(title)) {
		if replacement, exists := russianSlugLetters[raw]; exists {
			if replacement != "" {
				if separator && result.Len() > 0 {
					result.WriteByte('-')
				}
				result.WriteString(replacement)
				separator = false
			}
			continue
		}
		if (raw >= 'a' && raw <= 'z') || (raw >= '0' && raw <= '9') {
			if separator && result.Len() > 0 {
				result.WriteByte('-')
			}
			result.WriteRune(raw)
			separator = false
			continue
		}
		if unicode.IsSpace(raw) || unicode.IsPunct(raw) || unicode.IsSymbol(raw) {
			separator = result.Len() > 0
		}
	}
	return result.String()
}

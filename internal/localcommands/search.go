package localcommands

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

func commandMatchScore(name, query string) int {
	lower := cases.Lower(language.AmericanEnglish)
	value, term := lower.String(name), lower.String(query)
	if term == "" || value == term {
		return 0
	}
	if strings.HasPrefix(value, term) {
		return 1
	}
	if strings.Contains(value, term) {
		return 2
	}
	for _, character := range term {
		i := strings.IndexRune(value, character)
		if i < 0 {
			return 4
		}
		value = value[i+len(string(character)):]
	}
	return 3
}

func commandScore(c Command, query string) int {
	score := 4
	for _, name := range commandNames(c) {
		score = min(score, commandMatchScore(name, query))
	}
	return score
}

func commandComparator(query string) func(Command, Command) int {
	order := collate.New(language.AmericanEnglish)
	lower := cases.Lower(language.AmericanEnglish)
	return func(a, b Command) int {
		if d := commandScore(a, query) - commandScore(b, query); d != 0 {
			return d
		}
		if d := order.CompareString(lower.String(a.Name), lower.String(b.Name)); d != 0 {
			return d
		}
		return strings.Compare(a.ID, b.ID)
	}
}

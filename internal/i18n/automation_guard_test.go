package i18n

import "testing"

func TestPublicOperationNameIsNotATranslationKey(t *testing.T) {
	source := []byte(`package example
import "example/automation"
var definition=automation.Definition{Name:"desktop.retry",Description:"Retry desktop"}
`)
	errs, refs := checkGoFileAST("example.go", source, nil, map[string]bool{}, nil)
	if len(errs) != 0 || len(refs) != 0 {
		t.Fatal(errs, refs)
	}
	source = []byte(`package example
import "example/automation"
var definition=automation.Definition{Name:"desktop.retry",Description:"desktop.unknown_label"}
`)
	errs, _ = checkGoFileAST("example.go", source, nil, map[string]bool{}, nil)
	if len(errs) != 1 {
		t.Fatal("non-name literals escaped translation guard", errs)
	}
}

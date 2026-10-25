package llm

import "testing"

func TestProviderContract(t *testing.T) {
	for _, d := range Available() {
		p, err := Resolve(d.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, model := range d.Models {
			s := Selection{Provider: d.ID, Model: model, CredentialRef: "contract"}
			if err := p.Validate(s); err != nil {
				t.Fatal(err)
			}
			s.Model = "foreign/model"
			if p.Validate(s) == nil {
				t.Fatal("accepted foreign model")
			}
		}
	}
	s := Default()
	s.Provider = "unknown"
	if Validate(s) == nil {
		t.Fatal("silently fell back to DeepSeek")
	}
	s = Default()
	s.Endpoint = "https://example.invalid"
	if Validate(s) == nil {
		t.Fatal("unsupported endpoint silently ignored")
	}
}

package packaging

import "testing"

func TestBundleDescriptionRejectsIncompleteAndAmbiguousTargets(t *testing.T) {
	for name, change := range map[string]func(*Catalog){
		"empty components":    func(c *Catalog) { c.Components = nil },
		"duplicate component": func(c *Catalog) { c.Components = append(c.Components, c.Components[0]) },
		"unknown manager":     func(c *Catalog) { c.Components[0].Manager = "unsupported" },
		"empty targets":       func(c *Catalog) { c.Bundle.Targets = nil },
		"duplicate target":    func(c *Catalog) { c.Bundle.Targets = append(c.Bundle.Targets, c.Bundle.Targets[0]) },
		"missing default":     func(c *Catalog) { c.Bundle.DefaultTarget = "missing" },
		"missing model":       func(c *Catalog) { c.Bundle.Targets[0].Model = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := Dependencies()
			change(&c)
			if c.Validate() == nil {
				t.Fatal("invalid description accepted")
			}
		})
	}
	c := Dependencies()
	c.Bundle = Bundle{DefaultTarget: "foreign", Targets: []Target{{ID: "foreign", Runtime: "foreign-agent", Provider: "foreign-provider", Model: "foreign-model", CredentialRef: "foreign-key"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.DefaultTarget().Runtime != "foreign-agent" {
		t.Fatal("default target hardcoded")
	}
}

package webmodules

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestClosureFollowsDeclarationsWithoutTreatingVendorProseAsImports(t *testing.T) {
	files := fstest.MapFS{
		"index.html": {Data: []byte(`<script type="module" src="entry.mjs"></script>`)},
		"entry.mjs":  {Data: []byte("import './side.mjs';\nimport {\n value\n} from './vendor.mjs';\nexport {value} from './export.mjs';")},
		"side.mjs":   {Data: []byte("// from \"not-a-module\"\nconst text = 'from \"neither\"';")},
		"vendor.mjs": {Data: []byte("/**\n * from \"extension\" will be copied.\n */\nexport default {};")},
		"export.mjs": {Data: []byte("export const value = 1;")},
	}
	got := Closure(t, files, "index.html")
	want := []string{"entry.mjs", "export.mjs", "side.mjs", "vendor.mjs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("closure = %v, want %v", got, want)
	}
}

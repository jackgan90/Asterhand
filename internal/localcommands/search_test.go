package localcommands

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"slices"
	"testing"
)

func TestCommandSearchMatchesQuickInput(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for shared search cases")
	}
	entries := []Command{{ID: "a", Name: "Build Release", Aliases: []string{"br"}}, {ID: "b", Name: "build"}, {ID: "c", Name: "Rebuild"}, {ID: "d", Name: "版 本"}, {ID: "e", Name: "版本"}, {ID: "f", Name: "İstanbul"}, {ID: "g", Name: "Alpha_1"}, {ID: "h", Name: "Alpha-1"}, {ID: "i", Name: "éclair"}, {ID: "j", Name: "Eclair"}, {ID: "k", Name: "😀build"}}
	for _, q := range []string{"", "b", "br", "build", "版", "i", "i̇"} {
		got := []Command{}
		for _, c := range entries {
			if commandScore(c, q) < 4 {
				got = append(got, c)
			}
		}
		slices.SortFunc(got, commandComparator(q))
		ids := []string{}
		for _, c := range got {
			ids = append(ids, c.ID)
		}
		body, _ := json.Marshal(map[string]any{"entries": entries, "query": q})
		cmd := exec.Command(node, "--input-type=module", "-e", `import {filterCommands} from './internal/quickinput/assets/command-model.mjs';let s='';process.stdin.on('data',c=>s+=c);process.stdin.on('end',()=>{const x=JSON.parse(s);process.stdout.write(JSON.stringify(filterCommands(x.entries,x.query).map(x=>x.id)));});`)
		cmd.Dir = "../.."
		cmd.Stdin = bytes.NewReader(body)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		var want []string
		if err = json.Unmarshal(out, &want); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("query %q got %v want %v", q, ids, want)
		}
	}
}

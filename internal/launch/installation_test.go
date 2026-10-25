package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func installationFixture(t *testing.T) (string, Installation) {
	t.Helper()
	base := t.TempDir()
	program := filepath.Join(base, "程序 空间")
	executable := filepath.Join(program, "versions", "v1", "asterhand.exe")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("test fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	installation := Installation{Schema: 1, Executable: executable, Data: filepath.Join(base, "节点 数据")}
	path := filepath.Join(program, "installation.json")
	writeJSON(t, path, installation)
	return path, installation
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationAllowsBOMAndAdditionalMetadata(t *testing.T) {
	path, expected := installationFixture(t)
	b, _ := json.Marshal(map[string]any{"schema": 1, "executable": expected.Executable, "data": expected.Data, "desktop_shortcut": true, "version": "test"})
	if err := os.WriteFile(path, append([]byte{0xef, 0xbb, 0xbf}, b...), 0600); err != nil {
		t.Fatal(err)
	}
	actual, err := ReadInstallation(path)
	if err != nil || !samePath(actual.Executable, expected.Executable) || !samePath(actual.Data, expected.Data) {
		t.Fatalf("unexpected installation: %+v %v", actual, err)
	}
	if _, err := os.Stat(expected.Data); !os.IsNotExist(err) {
		t.Fatal("reading installation created the node home")
	}
}

func TestInstallationRejectsUntrustedPaths(t *testing.T) {
	for _, name := range []string{"relative-program", "outside-program", "relative-data", "program-data", "parent-data", "root-data", "schema"} {
		t.Run(name, func(t *testing.T) {
			path, installation := installationFixture(t)
			switch name {
			case "relative-program":
				installation.Executable = "versions/v1/asterhand.exe"
			case "outside-program":
				installation.Executable = filepath.Join(filepath.Dir(path), "versions", "..", "asterhand.exe")
			case "relative-data":
				installation.Data = "data"
			case "program-data":
				installation.Data = filepath.Join(filepath.Dir(path), "data")
			case "parent-data":
				installation.Data = filepath.Dir(filepath.Dir(path))
			case "root-data":
				installation.Data = filepath.VolumeName(path) + string(filepath.Separator)
			case "schema":
				installation.Schema = 9
			}
			writeJSON(t, path, installation)
			if _, err := ReadInstallation(path); err == nil {
				t.Fatal("invalid installation accepted")
			}
		})
	}
}

func TestPreparationDoesNotAdoptUnrelatedHome(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareHome(home); err == nil {
		t.Fatal("unrelated directory accepted")
	}
	files, _ := os.ReadDir(home)
	if len(files) != 1 {
		t.Fatal("preparation wrote into unrelated directory")
	}
}

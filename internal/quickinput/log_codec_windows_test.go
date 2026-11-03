//go:build windows

package quickinput

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/model"
)

type logCodecExecution struct{ out, err io.ReadCloser }

func (e *logCodecExecution) Stdout() io.ReadCloser { return e.out }
func (e *logCodecExecution) Stderr() io.ReadCloser { return e.err }
func (e *logCodecExecution) ForceStop() error      { return nil }
func (e *logCodecExecution) Close() error          { return nil }
func (e *logCodecExecution) Wait(context.Context) (localcommands.ExitEvidence, error) {
	code := uint32(0)
	return localcommands.ExitEvidence{Kind: "exited", Code: &code}, nil
}

type logCodecBackend struct{ data []byte }

func (b *logCodecBackend) Available() bool                { return true }
func (b *logCodecBackend) Reconcile(string) (bool, error) { return true, nil }
func (b *logCodecBackend) Start(localcommands.ScriptSpec, string) (localcommands.Execution, error) {
	return &logCodecExecution{io.NopCloser(bytes.NewReader(b.data)), io.NopCloser(bytes.NewReader(nil))}, nil
}

// Exercise actual stored page contexts with the production module in WebView2.
// Node/ICU throws on some malformed GB18030 streaming fragments even in
// replacement mode, so those cases require the product's browser decoder.
func checkNativeLogCodecs(t *testing.T, s *nativeSurface) {
	t.Helper()
	type sample struct {
		Encoding       string
		Prefix, Suffix []byte
		Context        map[string][]localcommands.LogChunk
	}
	cases := []sample{
		{Encoding: "gbk", Prefix: []byte{0x81}, Suffix: []byte{0x30, 0x81, 0x30}},
		{Encoding: "gbk", Prefix: []byte{0x81, 0x30}, Suffix: []byte{0x81, 0x30}},
		{Encoding: "gbk", Prefix: []byte{0x81, 0x30, 0x81}, Suffix: []byte{0x30}},
		{Encoding: "gbk", Prefix: []byte{0xbf, 0x30}, Suffix: []byte{0x0f, 0xb5, 0xcd, 0xf1, 0x2b}},
		{Encoding: "gbk", Prefix: []byte{0x81, 0x30, 0x81}, Suffix: []byte{0x20}},
		{Encoding: "gbk", Prefix: []byte{0x81, 0x30, 0x81}, Suffix: []byte{0xfe, 0x81}},
		{Encoding: "big5", Prefix: []byte{0xa4}, Suffix: []byte{0xa4, 0xa4, 0xe5}},
		{Encoding: "utf-8", Prefix: []byte{0xe4, 0xb8}, Suffix: []byte{0xad, 0xe6, 0x96, 0x87}},
		{Encoding: "utf-16le", Prefix: []byte{0x3d, 0xd8, 0x00}, Suffix: []byte{0xde, 0x2d, 0x4e}},
	}
	backend := &logCodecBackend{}
	home := t.TempDir()
	scriptPath := filepath.Join(home, "codec.cmd")
	if err := os.WriteFile(scriptPath, []byte("@exit /b 0\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := localcommands.NewRunManager(home, backend, nil)
	defer manager.Close(context.Background())
	for i := range cases {
		sample := &cases[i]
		prefix := append(bytes.Repeat([]byte{'A'}, 32*1024-len(sample.Prefix)), sample.Prefix...)
		backend.data = append(prefix, sample.Suffix...)
		command := localcommands.Command{ID: model.ID(), Name: "codec", Graph: commandTestGraph(scriptPath)}
		run, err := manager.Start(command)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for manager.Busy(command.ID) {
			if time.Now().After(deadline) {
				t.Fatal("codec fixture did not finish")
			}
			time.Sleep(time.Millisecond)
		}
		first, err := manager.Read(command.ID, run.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		page, err := manager.Read(command.ID, run.ID, first.Next)
		if err != nil {
			t.Fatal(err)
		}
		sample.Context = page.Context
		for _, chunks := range sample.Context {
			for i := range chunks {
				chunks[i].NodeID = ""
			}
		}
	}
	payload, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	script := `window.codecResult=null;import('./log-model.mjs').then(({logDecoder})=>{
        const rows=` + string(payload) + `;
        const bytes=value=>Uint8Array.from(atob(value),c=>c.charCodeAt(0));
        for(const row of rows){
            const prefix=bytes(row.Prefix),suffix=bytes(row.Suffix);
            const continuous=new TextDecoder(row.Encoding);
            continuous.decode(new Uint8Array(32768-prefix.length).fill(65),{stream:true});
            continuous.decode(prefix,{stream:true});
            const expected=continuous.decode(suffix);
            const decoder=logDecoder(row.Encoding);decoder.seed(row.Context?.[row.Encoding]??[]);
            const actual=decoder.decode({stream:'stdout',data:row.Suffix}).text+decoder.finish().find(chunk=>chunk.stream==='stdout').text;
            if(actual!==expected)throw new Error(JSON.stringify({row,actual,expected}));
        }
        window.codecResult={passed:rows.length};
    }).catch(error=>{window.codecResult={error:String(error)}})`
	evaluate(t, s, script)
	deadline := time.Now().Add(5 * time.Second)
	for {
		result := evaluate(t, s, "window.codecResult")
		if result != "null" {
			if result != `{"passed":9}` {
				t.Fatal(result)
			}
			t.Log("native stored-page codec cases", result)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("native decoder did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

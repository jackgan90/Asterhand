//go:build windows

package executor

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestManagedProcessHelper(t *testing.T) {
	mode := os.Getenv("ASTERHAND_PROCESS_TEST")
	if mode == "" {
		return
	}
	if mode == "parent" {
		bufio.NewReader(os.Stdin).ReadString('\n')
		child := exec.Command(os.Args[0], "-test.run=^TestManagedProcessHelper$")
		child.Env = append(os.Environ(), "ASTERHAND_PROCESS_TEST=child")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		fmt.Println(child.Process.Pid)
	}
	for {
		time.Sleep(time.Second)
	}
}
func TestManagedProcessCloseTerminatesDescendants(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestManagedProcessHelper$")
	cmd.Env = append(os.Environ(), "ASTERHAND_PROCESS_TEST=parent")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stop, err := startManaged(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if _, err = fmt.Fprintln(input, "start"); err != nil {
		t.Fatal(err)
	}
	input.Close()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	stop()
	cmd.Wait()
	state, err := windows.WaitForSingleObject(handle, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant survived job close: %d %v", state, err)
	}
}

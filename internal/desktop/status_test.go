package desktop

import "testing"

func TestTrayReportsNodeLifecycleWithoutModelPrerequisites(t *testing.T) {
	if (State{Name: "new node"}).text() != "节点运行中" || (State{Stopping: true}).text() != "正在退出" {
		t.Fatal("tray confused node lifecycle with model verification")
	}
}

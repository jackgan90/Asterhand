package desktop

import "testing"

func TestTrayReportsNodeLifecycleWithoutModelPrerequisites(t *testing.T) {
	if (State{Name: "new node"}).textFor("zh-CN") != "节点运行中" || (State{Stopping: true}).textFor("zh-CN") != "正在退出" {
		t.Fatal("tray confused node lifecycle with model verification (zh-CN)")
	}
	if (State{Name: "new node"}).textFor("en-US") != "Node running" || (State{Stopping: true}).textFor("en-US") != "Exiting" {
		t.Fatal("tray confused node lifecycle with model verification (en-US)")
	}
}

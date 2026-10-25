//go:build windows

package quickinput

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processCost struct {
	Count   int    `json:"process_count"`
	Commit  uint64 `json:"private_commit_bytes"`
	CPU     uint64 `json:"cpu_100ns"`
	samples map[uint32]uint64
}

func processTreeCost() processCost {
	var entries []windows.ProcessEntry32
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return processCost{}
	}
	defer windows.CloseHandle(snapshot)
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &e); err == nil; err = windows.Process32Next(snapshot, &e) {
		entries = append(entries, e)
	}
	owned := map[uint32]bool{uint32(os.Getpid()): true}
	for changed := true; changed; {
		changed = false
		for _, p := range entries {
			if owned[p.ParentProcessID] && !owned[p.ProcessID] {
				owned[p.ProcessID] = true
				changed = true
			}
		}
	}
	total := processCost{samples: map[uint32]uint64{}}
	for id := range owned {
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, id)
		if err != nil {
			continue
		}
		var counters struct {
			Size, Faults                                                                                          uint32
			PeakWorkingSet, WorkingSet, PeakPaged, Paged, PeakNonPaged, NonPaged, Pagefile, PeakPagefile, Private uintptr
		}
		counters.Size = uint32(unsafe.Sizeof(counters))
		ok, _, _ := windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo").Call(uintptr(h), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
		if ok != 0 {
			total.Commit += uint64(counters.Private)
			total.Count++
		}
		var created, exited, kernel, user windows.Filetime
		if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) == nil {
			cpu := (uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)) + (uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime))
			total.CPU += cpu
			total.samples[id] = cpu
		}
		windows.CloseHandle(h)
	}
	return total
}

func TestNativeSurfaceCostAndRepeatedCleanup(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("ASTERHAND_SURFACE_CYCLES"))
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" || n < 1 {
		t.Skip("explicit desktop and cycle budget required")
	}
	if n > 100 {
		t.Fatal("cycle budget exceeds 100")
	}
	var cold, warm, closeTimes []float64
	var peakAdded uint64
	var idlePercent float64
	base := processTreeCost()
	for i := 0; i < n; i++ {
		start := time.Now()
		s, err := startNativeSurface(t.TempDir(), nil, func(err error) { t.Errorf("surface: %v", err) })
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		cold = append(cold, float64(time.Since(start).Microseconds())/1000)
		live := processTreeCost()
		if live.Commit > base.Commit {
			peakAdded = max(peakAdded, live.Commit-base.Commit)
		}
		start = time.Now()
		if err = s.Snapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
		warm = append(warm, float64(time.Since(start).Microseconds())/1000)
		if i == 0 {
			before := processTreeCost()
			started := time.Now()
			time.Sleep(2 * time.Second)
			after := processTreeCost()
			idlePercent = float64(after.CPU-before.CPU) / float64(time.Since(started).Nanoseconds()/100) * 100
			if os.Getenv("ASTERHAND_IDLE_DIAGNOSTIC") == "1" {
				t.Logf("initial CPU %.3f%%, processes %d -> %d", idlePercent, before.Count, after.Count)
				for id, value := range after.samples {
					if old, ok := before.samples[id]; ok {
						t.Logf("pid %d (self=%v) initial cpu_ms %.3f", id, id == uint32(os.Getpid()), float64(value-old)/10000)
					}
				}
				for sample := 0; sample < 4; sample++ {
					before = processTreeCost()
					started = time.Now()
					time.Sleep(2 * time.Second)
					after = processTreeCost()
					t.Logf("idle diagnostic %d: %.3f%%, processes %d -> %d", sample+1, float64(after.CPU-before.CPU)/float64(time.Since(started).Nanoseconds()/100)*100, before.Count, after.Count)
				}
			}
		}
		start = time.Now()
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		closeTimes = append(closeTimes, float64(time.Since(start).Microseconds())/1000)
		if cost := processTreeCost(); cost.Count > base.Count {
			t.Fatalf("owned browser process survived cycle %d: baseline=%d current=%d", i+1, base.Count, cost.Count)
		}
		if (i+1)%10 == 0 {
			t.Logf("completed %d/%d surface lifecycles", i+1, n)
		}
	}
	p95 := func(values []float64) float64 { sort.Float64s(values); return values[(len(values)-1)*95/100] }
	report := map[string]any{"cycles": n, "cold_p95_ms": p95(cold), "bridge_roundtrip_p95_ms": p95(warm), "close_p95_ms": p95(closeTimes), "peak_added_commit_bytes": peakAdded, "hidden_cpu_percent_one_core": idlePercent, "orphaned_processes": 0, "budgets": map[string]any{"cold_p95_ms": 10000, "bridge_roundtrip_p95_ms": 200, "added_commit_bytes": 350 * 1024 * 1024, "hidden_cpu_percent_one_core": 1}}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(encoded))
	if path := os.Getenv("ASTERHAND_SURFACE_REPORT"); path != "" {
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if p95(cold) > 10000 || p95(warm) > 200 || peakAdded > 350*1024*1024 || idlePercent > 1 {
		t.Fatal("frozen engineering budget failed; inspect measured report")
	}
}

package quickinput

import "testing"

func TestCompactHeightRangeAndWorkArea(t *testing.T) {
	work := windowBounds{0, 0, 1920, 2160}
	for _, height := range []int{minimumSurfaceHeight, 178, 431, 760, maximumSurfaceHeight} {
		if !validSurfaceHeight(height) {
			t.Fatalf("rejected usable height %d", height)
		}
		bounds := fitWindow(work, 144, height, nil)
		if bounds.Bottom-bounds.Top != scaled(int32(height), 144) {
			t.Fatalf("height %d was not respected: %+v", height, bounds)
		}
	}
	for _, height := range []int{-1, 0, minimumSurfaceHeight - 1, maximumSurfaceHeight + 1} {
		if validSurfaceHeight(height) {
			t.Fatalf("accepted invalid height %d", height)
		}
	}
	for _, height := range []int{0, 178, 431, 760, 10000} {
		bounds := fitWindow(windowBounds{0, 0, 340, 210}, 192, height, nil)
		if bounds.Bottom > 210 || bounds.Right > 340 || bounds.Top < 0 || bounds.Left < 0 {
			t.Fatalf("clamped compact layout outside work area: %+v", bounds)
		}
	}
}

func TestWindowPlacementPreservesDragAcrossResizeAndDPI(t *testing.T) {
	work := windowBounds{-1920, 0, 0, 1080}
	origin := windowPoint{-1800, 80}
	compact := fitWindow(work, 96, 240, &origin)
	expanded := fitWindow(work, 96, 760, &origin)
	if compact.Left != expanded.Left || compact.Top != expanded.Top {
		t.Fatal("resize moved the user's anchor")
	}
	saved := rememberWindow(work, 96, compact)
	other := windowBounds{0, 60, 2560, 1500}
	point := saved.point(other, 144)
	if point != (windowPoint{180, 180}) {
		t.Fatal("DPI-relative restoration", point)
	}
	clamped := fitWindow(windowBounds{0, 0, 320, 240}, 192, 760, &origin)
	if clamped.Left < 0 || clamped.Top < 0 || clamped.Right > 320 || clamped.Bottom > 240 {
		t.Fatal("off-screen window", clamped)
	}
	if clamped.Right <= clamped.Left || clamped.Bottom <= clamped.Top {
		t.Fatal("empty layout")
	}
}

func TestWindowPlacementKeepsVisibleControlsOnWorkAreaChanges(t *testing.T) {
	for _, work := range []windowBounds{{0, 40, 1920, 1080}, {-1280, -720, 0, 0}, {0, 0, 1, 1}} {
		for _, dpi := range []uint32{0, 96, 144, 192} {
			for _, origin := range []*windowPoint{nil, {-100000, -100000}, {100000, 100000}} {
				bounds := fitWindow(work, dpi, 760, origin)
				if bounds.Left < work.Left || bounds.Top < work.Top || bounds.Right > work.Right || bounds.Bottom > work.Bottom {
					t.Fatalf("window outside work area: %+v %+v", work, bounds)
				}
			}
		}
	}
}

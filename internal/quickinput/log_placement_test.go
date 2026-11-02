package quickinput

import "testing"

func TestLogWindowFitsPhysicalWorkArea(t *testing.T) {
	for _, tc := range []struct {
		name   string
		work   windowBounds
		dpi    uint32
		size   windowPoint
		origin *windowPoint
	}{
		{"negative", windowBounds{-2560, -600, 0, 840}, 144, windowPoint{}, nil},
		{"taskbar", windowBounds{0, 40, 1920, 1080}, 96, windowPoint{}, nil},
		{"small", windowBounds{-600, 0, 0, 400}, 192, windowPoint{}, nil},
		{"tiny", windowBounds{0, 0, 1, 1}, 96, windowPoint{}, nil},
		{"offscreen", windowBounds{1920, 0, 3840, 1080}, 96, windowPoint{920, 640}, &windowPoint{-5000, -5000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fitLogWindow(tc.work, tc.dpi, tc.size, tc.origin)
			if got.Left < tc.work.Left || got.Top < tc.work.Top || got.Right > tc.work.Right || got.Bottom > tc.work.Bottom || got.Right <= got.Left || got.Bottom <= got.Top {
				t.Fatalf("%+v outside %+v", got, tc.work)
			}
		})
	}
	work := windowBounds{0, 0, 2560, 1440}
	want := windowBounds{120, 100, 1040, 740}
	if got := fitLogWindow(work, 96, windowPoint{920, 640}, &windowPoint{120, 100}); got != want {
		t.Fatalf("same monitor position changed: %+v", got)
	}
	got := fitLogWindow(work, 144, windowPoint{}, nil)
	if got.Right-got.Left != 1380 || got.Bottom-got.Top != 960 {
		t.Fatalf("DPI dimensions: %+v", got)
	}
}

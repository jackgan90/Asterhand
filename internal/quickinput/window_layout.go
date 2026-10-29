package quickinput

// Layout uses device-independent saved offsets and an adapter-provided work
// area. OS handles and moving/activation protocols stay in the native adapter.
type windowBounds struct{ Left, Top, Right, Bottom int32 }
type windowPoint struct{ X, Y int32 }
type windowPlacement struct{ X, Y int32 }

func scaled(value int32, dpi uint32) int32 {
	return int32(int64(value) * int64(dpi) / 96)
}

func fitWindow(work windowBounds, dpi uint32, height int, origin *windowPoint) windowBounds {
	if dpi == 0 {
		dpi = 96
	}
	marginX := min(scaled(8, dpi), max(0, (work.Right-work.Left-1)/2))
	marginY := min(scaled(8, dpi), max(0, (work.Bottom-work.Top-1)/2))
	width := min(scaled(680, dpi), max(1, work.Right-work.Left-2*marginX))
	h := min(scaled(int32(max(240, min(800, height))), dpi), max(1, work.Bottom-work.Top-2*marginY))
	x := work.Left + (work.Right-work.Left-width)/2
	y := work.Top + (work.Bottom-work.Top-h)/3
	if origin != nil {
		x, y = origin.X, origin.Y
	}
	x = max(work.Left+marginX, min(x, work.Right-marginX-width))
	y = max(work.Top+marginY, min(y, work.Bottom-marginY-h))
	return windowBounds{x, y, x + width, y + h}
}

func rememberWindow(work windowBounds, dpi uint32, bounds windowBounds) windowPlacement {
	if dpi == 0 {
		dpi = 96
	}
	return windowPlacement{int32(int64(bounds.Left-work.Left) * 96 / int64(dpi)), int32(int64(bounds.Top-work.Top) * 96 / int64(dpi))}
}

func (p windowPlacement) point(work windowBounds, dpi uint32) windowPoint {
	if dpi == 0 {
		dpi = 96
	}
	return windowPoint{work.Left + scaled(p.X, dpi), work.Top + scaled(p.Y, dpi)}
}

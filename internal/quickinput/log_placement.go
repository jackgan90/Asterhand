package quickinput

import (
	"context"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type logAnchorContextKey struct{}

func logOpenAnchor(ctx context.Context) *hostipc.WindowAnchor {
	if anchor, ok := ctx.Value(logAnchorContextKey{}).(hostipc.WindowAnchor); ok && anchor.Valid() {
		return &anchor
	}
	return defaultLogAnchor()
}

func fitLogWindow(work windowBounds, dpi uint32, size windowPoint, origin *windowPoint) windowBounds {
	if dpi == 0 {
		dpi = 96
	}
	if size.X <= 0 || size.Y <= 0 {
		size = windowPoint{scaled(920, dpi), scaled(640, dpi)}
	}
	marginX := min(scaled(8, dpi), max(0, (work.Right-work.Left-1)/2))
	marginY := min(scaled(8, dpi), max(0, (work.Bottom-work.Top-1)/2))
	w := min(size.X, max(1, work.Right-work.Left-2*marginX))
	h := min(size.Y, max(1, work.Bottom-work.Top-2*marginY))
	x, y := work.Left+(work.Right-work.Left-w)/2, work.Top+(work.Bottom-work.Top-h)/2
	if origin != nil {
		x, y = origin.X, origin.Y
	}
	x = max(work.Left+marginX, min(x, work.Right-marginX-w))
	y = max(work.Top+marginY, min(y, work.Bottom-marginY-h))
	return windowBounds{x, y, x + w, y + h}
}

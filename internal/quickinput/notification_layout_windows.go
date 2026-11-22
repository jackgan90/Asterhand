//go:build windows

package quickinput

import "github.com/jackgan90/Asterhand/internal/notifications"

const (
	notificationMinHeight     = 80
	notificationMaxHeight     = 160
	notificationInitialHeight = 104
)

func notificationStackBounds(work windowBounds, dpi uint32, cards []notifications.Card, capacity int, heights map[uint64]int32) []windowBounds {
	return notificationStackBoundsWithBase(work, dpi, cards, capacity, heights, 0)
}

func notificationStackBoundsWithBase(work windowBounds, dpi uint32, cards []notifications.Card, capacity int, heights map[uint64]int32, baseBelow int32) []windowBounds {
	bounds := make([]windowBounds, len(cards))
	below := baseBelow
	for i := len(cards) - 1; i >= 0; i-- {
		height := heights[cards[i].ID]
		if height == 0 {
			height = notificationInitialHeight
		}
		bounds[i] = notificationBounds(work, dpi, height, below)
		if i < capacity {
			below += height + 10
		}
	}
	return bounds
}

func workerActionBounds(work windowBounds, dpi uint32, height int32) windowBounds {
	scale := func(v int32) int32 { return int32((int64(v)*int64(dpi) + 95) / 96) }
	margin := min(scale(16), max(0, (work.Right-work.Left)/8), max(0, (work.Bottom-work.Top)/8))
	w := min(scale(workerActionWidth), max(1, work.Right-work.Left-2*margin))
	h := min(scale(height), max(1, work.Bottom-work.Top-2*margin))
	right := work.Right - margin
	bottom := work.Bottom - margin
	return windowBounds{Left: right - w, Right: right, Top: max(work.Top, bottom-h), Bottom: max(work.Top+1, bottom)}
}

// The current native window must receive a fresh layout after a height change
// before it can reveal. Obsolete replies cannot resize or reveal a reused slot.
func (d *notificationDesktop) measured(slot int, id uint64, height int32) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ids[slot] != id {
		return false
	}
	if d.heights[id] == height {
		return true
	}
	if d.heights == nil {
		d.heights = make(map[uint64]int32)
	}
	d.heights[id] = height
	d.renderLocked()
	return false
}

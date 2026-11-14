//go:build windows

package quickinput

import "github.com/jackgan90/Asterhand/internal/notifications"

const (
	notificationMinHeight     = 80
	notificationMaxHeight     = 160
	notificationInitialHeight = 104
)

func notificationStackBounds(work windowBounds, dpi uint32, cards []notifications.Card, capacity int, heights map[uint64]int32) []windowBounds {
	bounds := make([]windowBounds, len(cards))
	below := int32(0)
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

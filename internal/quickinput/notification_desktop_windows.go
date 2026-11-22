//go:build windows

package quickinput

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/notifications"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func NewNotificationPresenter(home string) notifications.Presenter {
	return newNotificationHost(home, func() (hostProcess, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return launchWindowsHostKind(filepath.Dir(exe), "--notification-host")
	})
}

type notificationDesktop struct {
	mu              sync.Mutex
	peer            *hostPeer
	surfaces        [notifications.MaxVisible]*nativeSurface
	action          *nativeSurface
	actionHeight    int32
	ids             [notifications.MaxVisible]uint64
	heights         map[uint64]int32
	hover           [notifications.MaxVisible]bool
	snapshot        notifications.Snapshot
	monitor         uintptr
	paused, reduced bool
	suspended       bool
	sessionVisible  func() bool
	capacity        int
	duration        int64
}

func serveNotificationHost(input io.Reader, output io.Writer, initial hostipc.Message) error {
	if err := checkHostRuntimePolicy(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &notificationDesktop{peer: newHostPeer(ctx, input, output, initial.Session), sessionVisible: notificationSessionVisible}
	defer d.close()
	for i := range d.surfaces {
		s := newWindowSurface(initial.Home, nil, d.peer.fail, nil, nil)
		s.theme, s.locale = initial.Theme, initial.Locale
		s.notification = &notificationSurface{desktop: d, slot: i}
		d.surfaces[i] = s
		go s.run()
	}
	act := newWindowSurface(initial.Home, nil, d.peer.fail, nil, nil)
	act.theme, act.locale = initial.Theme, initial.Locale
	act.workerAction = &workerActionSurface{desktop: d}
	d.action = act
	go act.run()
	deadline := time.NewTimer(14 * time.Second)
	defer deadline.Stop()
	allSurfaces := append(append([]*nativeSurface(nil), d.surfaces[:]...), d.action)
	for _, s := range allSurfaces {
		select {
		case err := <-s.ready:
			if err != nil {
				return err
			}
		case err := <-d.peer.failures:
			return err
		case <-deadline.C:
			return context.DeadlineExceeded
		}
	}
	if err := d.peer.send(hostipc.Message{Kind: hostipc.Ready, AppVersion: config.Version, Source: config.SourceDigest, Runtime: d.surfaces[0].version}); err != nil {
		return err
	}
	d.refreshEnvironment()
	for {
		select {
		case err := <-d.peer.failures:
			return err
		case m := <-d.peer.in:
			if m.Kind == hostipc.Quit {
				return nil
			}
			if m.Kind != hostipc.NotificationFrame {
				return hostipc.ErrProtocol
			}
			d.apply(*m.Notifications)
		}
	}
}

func (d *notificationDesktop) close() {
	// All controllers sharing this browser must close before waiting for its
	// BrowserProcessExited event. Never wait for one while the others stay open.
	for _, s := range d.surfaces {
		if s != nil {
			_ = s.dispatch(context.Background(), s.beginClose)
		}
	}
	if d.action != nil {
		_ = d.action.dispatch(context.Background(), d.action.beginClose)
	}
	for _, s := range d.surfaces {
		if s != nil {
			_ = s.Close()
		}
	}
	if d.action != nil {
		_ = d.action.Close()
	}
}
func (d *notificationDesktop) emit(e notifications.Event) {
	if err := d.peer.send(hostipc.Message{Kind: hostipc.NotificationEvent, Notification: &e}); err != nil {
		d.peer.fail(err)
	}
}

func (d *notificationDesktop) actionMeasured(height int32) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.actionHeight == height {
		return true
	}
	d.actionHeight = height
	d.renderLocked()
	return false
}

func (d *notificationDesktop) apply(snapshot notifications.Snapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if snapshot.Revision <= d.snapshot.Revision {
		return
	}
	wasEmpty := len(d.snapshot.Cards) == 0 && len(d.snapshot.Interactions) == 0
	nowActive := len(snapshot.Cards) > 0 || len(snapshot.Interactions) > 0
	if wasEmpty && nowActive {
		d.monitor = foregroundMonitor()
		d.updateEnvironmentLocked()
	}
	if len(snapshot.Interactions) == 0 {
		d.actionHeight = 0
	}
	d.snapshot = snapshot
	d.renderLocked()
}

func (d *notificationDesktop) renderLocked() {
	monitor, ok := monitorDetails(d.monitor)
	if !ok {
		d.monitor = foregroundMonitor()
		monitor, ok = monitorDetails(d.monitor)
	}
	if !ok {
		return
	}
	retained := map[uint64]bool{}
	hasShown := false
	for _, id := range d.ids {
		if id != 0 {
			hasShown = true
		}
	}
	for _, c := range d.snapshot.Cards {
		retained[c.ID] = true
	}
	for id := range d.heights {
		if !retained[id] {
			delete(d.heights, id)
		}
	}
	for i, id := range d.ids {
		if id != 0 && !retained[id] {
			d.ids[i] = 0
			d.hover[i] = false
			_ = d.surfaces[i].dispatch(context.Background(), func() { d.surfaces[i].hideNotification() })
		}
	}
	baseBelow := int32(0)
	if len(d.snapshot.Interactions) > 0 && !d.paused {
		ah := d.actionHeight
		if ah == 0 {
			ah = workerActionInitialHeight
		}
		baseBelow = ah + workerActionGap
	}
	if d.action != nil {
		ah := d.actionHeight
		if ah == 0 {
			ah = workerActionInitialHeight
		}
		ab := workerActionBounds(monitor.work, monitor.dpi, ah)
		items := append([]notifications.WorkerInteraction(nil), d.snapshot.Interactions...)
		paused, theme, locale, dpi := d.paused, d.snapshot.Theme, d.snapshot.Locale, monitor.dpi
		if err := d.action.dispatch(context.Background(), func() {
			d.action.theme, d.action.locale = theme, locale
			d.action.workerAction.dpi = dpi
			d.action.applyWorkerActions(items, ab, paused)
		}); err != nil {
			d.peer.fail(err)
		}
	}
	placements := notificationStackBoundsWithBase(monitor.work, monitor.dpi, d.snapshot.Cards, d.capacity, d.heights, baseBelow)
	for index, card := range d.snapshot.Cards {
		slot := -1
		for i, id := range d.ids {
			if id == card.ID {
				slot = i
				break
			}
		}
		if slot < 0 {
			for i, id := range d.ids {
				if id == 0 {
					slot = i
					d.ids[i] = card.ID
					break
				}
			}
		}
		if slot < 0 {
			continue
		}
		s := d.surfaces[slot]
		bounds := placements[index]
		paused := d.paused || index >= d.capacity
		reduced, theme, locale := d.reduced, d.snapshot.Theme, d.snapshot.Locale
		if err := s.dispatch(context.Background(), func() {
			s.theme, s.locale = theme, locale
			s.notification.dpi = monitor.dpi
			if hasShown && s.notification.card.ID != card.ID && !reduced {
				s.notification.revealAfter = time.Now().Add(200 * time.Millisecond)
			}
			s.applyNotification(card, bounds, paused, reduced)
		}); err != nil {
			d.peer.fail(err)
		}
	}
}

func notificationBounds(work windowBounds, dpi uint32, height, below int32) windowBounds {
	scale := func(v int32) int32 { return int32((int64(v)*int64(dpi) + 95) / 96) }
	margin := min(scale(16), max(0, (work.Right-work.Left)/8), max(0, (work.Bottom-work.Top)/8))
	w, h := min(scale(360), max(1, work.Right-work.Left-2*margin)), scale(height)
	right := work.Right - margin
	bottom := work.Bottom - margin - scale(below)
	return windowBounds{Left: right - w, Right: right, Top: max(work.Top, bottom-h), Bottom: max(work.Top+1, bottom)}
}

func (d *notificationDesktop) refreshEnvironment() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.updateEnvironmentLocked()
	d.renderLocked()
}

func (d *notificationDesktop) updateEnvironmentLocked() {
	d.paused = d.suspended || !d.sessionVisible()
	if d.paused {
		d.hover = [notifications.MaxVisible]bool{}
		d.emit(notifications.Event{Kind: "hover"})
	}
	if d.monitor == 0 {
		d.monitor = foregroundMonitor()
	}
	monitor, ok := monitorDetails(d.monitor)
	if !ok {
		d.monitor = foregroundMonitor()
		monitor, ok = monitorDetails(d.monitor)
	}
	capacity := 0
	if ok {
		height := int64(monitor.work.Bottom-monitor.work.Top) * 96 / int64(monitor.dpi)
		// Reserve room for the tallest accepted card; actual stacks use measured heights.
		capacity = max(0, min(notifications.MaxVisible, int((height-32+10)/(notificationMaxHeight+10))))
	}
	var animations uint32 = 1
	uiDLL.NewProc("SystemParametersInfoW").Call(0x1042, 0, uintptr(unsafe.Pointer(&animations)), 0) // SPI_GETCLIENTAREAANIMATION
	var seconds uint32
	uiDLL.NewProc("SystemParametersInfoW").Call(0x2016, 0, uintptr(unsafe.Pointer(&seconds)), 0) // SPI_GETMESSAGEDURATION
	duration := int64(3000)
	if seconds > 5 {
		duration = min(86400000, int64(seconds)*1000)
	}
	d.capacity, d.duration, d.reduced = capacity, duration, animations == 0
	d.emit(notifications.Event{Kind: "environment", Capacity: capacity, DurationMS: duration, Paused: d.paused})
}

func (d *notificationDesktop) suspend(paused bool) {
	d.mu.Lock()
	d.suspended = paused
	d.mu.Unlock()
	d.refreshEnvironment()
}

func (d *notificationDesktop) setHover(slot int, value bool) {
	d.mu.Lock()
	d.hover[slot] = value
	paused := false
	for _, v := range d.hover {
		paused = paused || v
	}
	d.mu.Unlock()
	d.emit(notifications.Event{Kind: "hover", Paused: paused})
}

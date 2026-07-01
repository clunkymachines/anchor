package db

import (
	"context"
	"strconv"
	"sync"
)

type deviceEventNotifier struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

func newDeviceEventNotifier() *deviceEventNotifier {
	return &deviceEventNotifier{
		subscribers: make(map[string]map[chan struct{}]struct{}),
	}
}

func (n *deviceEventNotifier) subscribe(ctx context.Context, deviceID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	n.mu.Lock()
	if n.subscribers[deviceID] == nil {
		n.subscribers[deviceID] = make(map[chan struct{}]struct{})
	}
	n.subscribers[deviceID][ch] = struct{}{}
	n.mu.Unlock()

	unsubscribe := func() {
		n.mu.Lock()
		if subscribers := n.subscribers[deviceID]; subscribers != nil {
			delete(subscribers, ch)
			if len(subscribers) == 0 {
				delete(n.subscribers, deviceID)
			}
		}
		n.mu.Unlock()
	}

	go func() {
		<-ctx.Done()
		unsubscribe()
	}()

	return ch, unsubscribe
}

func (n *deviceEventNotifier) publish(deviceID string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	for ch := range n.subscribers[deviceID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Store) SubscribeDeviceEvents(ctx context.Context, deviceID string) (<-chan struct{}, func()) {
	return s.events.subscribe(ctx, deviceID)
}

func (s *Store) SubscribeDeviceTasks(ctx context.Context, deviceID string) (<-chan struct{}, func()) {
	return s.tasks.subscribe(ctx, deviceID)
}

func (s *Store) SubscribeReleaseCVEScans(ctx context.Context, organisationID int64, releaseID int64) (<-chan struct{}, func()) {
	if s.scans == nil {
		s.scans = newDeviceEventNotifier()
	}
	return s.scans.subscribe(ctx, releaseScanEventKey(organisationID, releaseID))
}

func (s *Store) publishReleaseCVEScan(organisationID int64, releaseID int64) {
	if s.scans == nil {
		return
	}
	s.scans.publish(releaseScanEventKey(organisationID, releaseID))
}

func releaseScanEventKey(organisationID int64, releaseID int64) string {
	return strconv.FormatInt(organisationID, 10) + ":" + strconv.FormatInt(releaseID, 10)
}

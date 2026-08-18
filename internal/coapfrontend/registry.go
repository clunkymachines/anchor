package coapfrontend

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"anchor/internal/coapapi"
	"github.com/plgd-dev/go-coap/v3/udp/client"
)

type Association struct {
	DeviceID           string
	Generation         uint64
	CredentialRevision int64
	CIDNegotiated      bool
	CIDLength          int
	PeerAddress        net.Addr
	LastActivity       time.Time
	ExpectedHeartbeat  time.Duration
	Cancel             context.CancelFunc
	Conn               *client.Conn
	Dispatch           *sync.Mutex
}

type Registry struct {
	mu             sync.Mutex
	max            int
	nextGeneration uint64
	associations   map[string]*Association
}

func NewRegistry(max int) *Registry {
	if max <= 0 {
		max = 1000
	}
	return &Registry{max: max, associations: make(map[string]*Association)}
}
func (r *Registry) Install(a Association) error {
	r.mu.Lock()
	if _, ok := r.associations[a.DeviceID]; !ok && len(r.associations) >= r.max {
		r.mu.Unlock()
		return errors.New("association capacity reached")
	}
	r.nextGeneration++
	a.Generation = r.nextGeneration
	if a.LastActivity.IsZero() {
		a.LastActivity = time.Now()
	}
	if a.Dispatch == nil {
		a.Dispatch = &sync.Mutex{}
	}
	old, ok := r.associations[a.DeviceID]
	r.associations[a.DeviceID] = &a
	r.mu.Unlock()
	if ok && old.Cancel != nil {
		old.Cancel()
	}
	if ok && old.Conn != nil {
		_ = old.Conn.Close()
	}
	return nil
}
func (r *Registry) Remove(deviceID string, generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.associations[deviceID]
	if !ok || (generation != 0 && a.Generation != generation) {
		return false
	}
	delete(r.associations, deviceID)
	if a.Cancel != nil {
		a.Cancel()
	}
	return true
}
func (r *Registry) Touch(deviceID string, generation uint64, peer net.Addr) (updated, peerChanged bool, previousPeer net.Addr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.associations[deviceID]
	if !ok || a.Generation != generation {
		return false, false, nil
	}
	a.LastActivity = time.Now()
	if peer != nil {
		previousPeer = a.PeerAddress
		peerChanged = addrString(previousPeer) != addrString(peer)
		a.PeerAddress = peer
	}
	return true, peerChanged, previousPeer
}
func (r *Registry) Get(deviceID string) (*Association, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.associations[deviceID]
	if !ok {
		return nil, false
	}
	snapshot := *a
	return &snapshot, true
}
func (r *Registry) Snapshot() []Association {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Association, 0, len(r.associations))
	for _, a := range r.associations {
		out = append(out, *a)
	}
	return out
}
func (r *Registry) Status(deviceID string) coapapi.AssociationStatus {
	a, ok := r.Get(deviceID)
	if !ok {
		return coapapi.AssociationStatus{DeviceID: deviceID}
	}
	return coapapi.AssociationStatus{DeviceID: deviceID, Connected: true, Generation: a.Generation, CredentialRevision: a.CredentialRevision, CIDNegotiated: a.CIDNegotiated, CIDLength: a.CIDLength, LastActivityMS: a.LastActivity.UnixMilli(), PeerAddress: addrString(a.PeerAddress)}
}

func (r *Registry) Invalidate(deviceID string, revision int64, force bool) bool {
	r.mu.Lock()
	a, ok := r.associations[deviceID]
	if !ok || (!force && a.CredentialRevision >= revision) {
		r.mu.Unlock()
		return false
	}
	delete(r.associations, deviceID)
	r.mu.Unlock()
	if a.Cancel != nil {
		a.Cancel()
	}
	if a.Conn != nil {
		_ = a.Conn.Close()
	}
	return true
}

func (r *Registry) Sweep(now time.Time) int {
	r.mu.Lock()
	var expired []*Association
	for id, a := range r.associations {
		idle := 3 * a.ExpectedHeartbeat
		if idle < time.Hour {
			idle = time.Hour
		}
		if idle > 30*24*time.Hour {
			idle = 30 * 24 * time.Hour
		}
		if now.Sub(a.LastActivity) > idle {
			delete(r.associations, id)
			expired = append(expired, a)
		}
	}
	r.mu.Unlock()
	for _, a := range expired {
		if a.Cancel != nil {
			a.Cancel()
		}
		if a.Conn != nil {
			_ = a.Conn.Close()
		}
	}
	return len(expired)
}
func addrString(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}
func RandomCID(length int, used func([]byte) bool) ([]byte, error) {
	if length < 0 || length > 32 {
		return nil, fmt.Errorf("invalid CID length %d", length)
	}
	for attempt := 0; attempt < 100; attempt++ {
		cid := make([]byte, length)
		if _, err := rand.Read(cid); err != nil {
			return nil, err
		}
		if used == nil || !used(cid) {
			return cid, nil
		}
	}
	return nil, errors.New("could not allocate unique CID")
}

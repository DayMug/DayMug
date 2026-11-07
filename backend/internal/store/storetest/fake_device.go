package storetest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// fakeDevices backs the device store. It has its own lock because devices are
// touched from the device hub's connection goroutines, not just handlers.
type fakeDevices struct {
	mu       sync.Mutex
	devices  []store.Device
	bindings map[string]store.ConversationDevice
}

func (m *Fake) deviceState() *fakeDevices {
	m.devicesOnce.Do(func() { m.devices = &fakeDevices{bindings: map[string]store.ConversationDevice{}} })
	return m.devices
}

func (m *Fake) CreateDevice(_ context.Context, d store.Device) (store.Device, error) {
	st := m.deviceState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if d.ID == "" {
		d.ID = uuid.New().String()
	}
	for _, existing := range st.devices {
		if existing.ID == d.ID {
			return store.Device{}, fmt.Errorf("UNIQUE constraint failed: devices.id")
		}
		if existing.TokenHash == d.TokenHash {
			return store.Device{}, fmt.Errorf("UNIQUE constraint failed: devices.token_hash")
		}
	}
	d.CreatedAt = fakeNow()
	d.LastSeenAt = d.CreatedAt
	st.devices = append(st.devices, d)
	return d, nil
}

func (m *Fake) findDevice(match func(store.Device) bool) (store.Device, error) {
	st := m.deviceState()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, d := range st.devices {
		if match(d) {
			return d, nil
		}
	}
	return store.Device{}, store.ErrNotFound
}

func (m *Fake) GetDevice(_ context.Context, id string) (store.Device, error) {
	return m.findDevice(func(d store.Device) bool { return d.ID == id })
}

func (m *Fake) GetDeviceByTokenHash(_ context.Context, tokenHash string) (store.Device, error) {
	if tokenHash == "" {
		return store.Device{}, store.ErrNotFound
	}
	return m.findDevice(func(d store.Device) bool { return d.TokenHash == tokenHash })
}

func (m *Fake) ListDevices(_ context.Context, userID string) ([]store.Device, error) {
	st := m.deviceState()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []store.Device
	for _, d := range st.devices {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *Fake) DeleteDevice(_ context.Context, userID, id string) error {
	st := m.deviceState()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i, d := range st.devices {
		if d.ID == id && d.UserID == userID {
			st.devices = append(st.devices[:i], st.devices[i+1:]...)
			for conv, b := range st.bindings {
				if b.DeviceID == id {
					delete(st.bindings, conv)
				}
			}
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) TouchDevice(_ context.Context, id string, at time.Time) error {
	st := m.deviceState()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.devices {
		if st.devices[i].ID == id {
			st.devices[i].LastSeenAt = at.UTC().Truncate(time.Second)
		}
	}
	return nil
}

func (m *Fake) GetConversationDevice(_ context.Context, conversationID string) (store.ConversationDevice, error) {
	st := m.deviceState()
	st.mu.Lock()
	defer st.mu.Unlock()
	b, ok := st.bindings[conversationID]
	if !ok {
		return store.ConversationDevice{}, store.ErrNotFound
	}
	return b, nil
}

func (m *Fake) SetConversationDevice(_ context.Context, b store.ConversationDevice) error {
	m.QueueMu.Lock()
	live := m.liveConversationLocked(b.ConversationID)
	m.QueueMu.Unlock()
	st := m.deviceState()
	st.mu.Lock()
	defer st.mu.Unlock()
	found := false
	for _, d := range st.devices {
		if d.ID == b.DeviceID {
			found = true
		}
	}
	if !live || !found {
		return fmt.Errorf("FOREIGN KEY constraint failed")
	}
	st.bindings[b.ConversationID] = b
	return nil
}

func (m *Fake) ClearConversationDevice(_ context.Context, conversationID string) error {
	st := m.deviceState()
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.bindings, conversationID)
	return nil
}

func (m *Fake) ListDeviceConversations(_ context.Context, deviceID string) ([]string, error) {
	st := m.deviceState()
	st.mu.Lock()
	defer st.mu.Unlock()
	ids := []string{}
	for id, b := range st.bindings {
		if b.DeviceID == deviceID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

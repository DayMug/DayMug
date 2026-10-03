package residentpool

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

type member struct {
	name string
	busy bool
	idle time.Time
}

func (m *member) Busy() bool           { return m.busy }
func (m *member) IdleSince() time.Time { return m.idle }

func names(ms []*member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.name
	}
	return out
}

func TestEvictOverCap(t *testing.T) {
	base := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }
	for name, tc := range map[string]struct {
		members []*member
		limit   int
		keep    string
		evicted []string
		left    []string
	}{
		"within the cap evicts nothing": {
			members: []*member{{name: "a", idle: at(1)}, {name: "b", idle: at(2)}},
			limit:   2,
			left:    []string{"a", "b"},
		},
		"oldest idle goes first": {
			members: []*member{{name: "a", idle: at(3)}, {name: "b", idle: at(1)}, {name: "c", idle: at(2)}},
			limit:   1,
			evicted: []string{"b", "c"},
			left:    []string{"a"},
		},
		"keep is exempt even when oldest": {
			members: []*member{{name: "a", idle: at(1)}, {name: "b", idle: at(2)}, {name: "c", idle: at(3)}},
			limit:   2,
			keep:    "a",
			evicted: []string{"b"},
			left:    []string{"a", "c"},
		},
		"busy is exempt even when oldest": {
			members: []*member{{name: "a", busy: true, idle: at(1)}, {name: "b", idle: at(2)}, {name: "c", idle: at(3)}},
			limit:   2,
			evicted: []string{"b"},
			left:    []string{"a", "c"},
		},
		"stays over the cap rather than touch exempt members": {
			members: []*member{{name: "a", busy: true, idle: at(1)}, {name: "b", busy: true, idle: at(2)}, {name: "c", idle: at(3)}},
			limit:   1,
			keep:    "c",
			left:    []string{"a", "b", "c"},
		},
		"never-idle zero time counts as oldest": {
			members: []*member{{name: "a", idle: at(1)}, {name: "b"}},
			limit:   1,
			evicted: []string{"b"},
			left:    []string{"a"},
		},
		"zero limit empties everything evictable": {
			members: []*member{{name: "a", idle: at(2)}, {name: "b", idle: at(1)}, {name: "c", busy: true}},
			limit:   0,
			evicted: []string{"b", "a"},
			left:    []string{"c"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			entries := map[string]*member{}
			for _, m := range tc.members {
				entries[m.name] = m
			}
			evicted := EvictOverCap(entries, tc.limit, tc.keep)
			if got := names(evicted); !reflect.DeepEqual(got, append([]string{}, tc.evicted...)) {
				t.Fatalf("evicted = %v, want %v", got, tc.evicted)
			}
			var left []string
			for key := range entries {
				left = append(left, key)
			}
			sort.Strings(left)
			if !reflect.DeepEqual(left, tc.left) {
				t.Fatalf("left = %v, want %v", left, tc.left)
			}
		})
	}
}

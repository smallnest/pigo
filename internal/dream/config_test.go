package dream

import "testing"

func boolPtr(b bool) *bool { return &b }

func TestNewConfigDefaults(t *testing.T) {
	// Missing [dream] table: nil enabled, zero ints → all defaults.
	c := NewConfig(nil, 0, 0)
	if !c.Enabled {
		t.Errorf("Enabled = false, want true (nil → true)")
	}
	if c.IntervalDays != DefaultIntervalDays {
		t.Errorf("IntervalDays = %d, want %d", c.IntervalDays, DefaultIntervalDays)
	}
	if c.RecentSessions != DefaultRecentSessions {
		t.Errorf("RecentSessions = %d, want %d", c.RecentSessions, DefaultRecentSessions)
	}
}

func TestNewConfigEnabledSemantics(t *testing.T) {
	tests := []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{"nil treated as true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewConfig(tt.enabled, 0, 0).Enabled; got != tt.want {
				t.Errorf("Enabled = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewConfigNonPositiveFallback(t *testing.T) {
	tests := []struct {
		name                   string
		interval, recent       int
		wantInterval, wantRcnt int
	}{
		{"zero falls back", 0, 0, DefaultIntervalDays, DefaultRecentSessions},
		{"negative falls back", -3, -1, DefaultIntervalDays, DefaultRecentSessions},
		{"positive preserved", 14, 50, 14, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewConfig(nil, tt.interval, tt.recent)
			if c.IntervalDays != tt.wantInterval {
				t.Errorf("IntervalDays = %d, want %d", c.IntervalDays, tt.wantInterval)
			}
			if c.RecentSessions != tt.wantRcnt {
				t.Errorf("RecentSessions = %d, want %d", c.RecentSessions, tt.wantRcnt)
			}
		})
	}
}

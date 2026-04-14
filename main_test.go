package main

import (
	"testing"
	"time"
)

func TestParsePortSpec(t *testing.T) {
	tests := []struct {
		spec        string
		defaultHost string
		want        portSpec
	}{
		// Basic port
		{"3025", "default", portSpec{3025, 3025, "default", ""}},
		// Local:remote
		{"3025:8080", "default", portSpec{3025, 8080, "default", ""}},
		// Named
		{"3030=rfx-engine", "default", portSpec{3030, 3030, "default", "rfx-engine"}},
		// Local:remote + name
		{"3030:8080=api", "default", portSpec{3030, 8080, "default", "api"}},
		// Host override
		{"3030@server1", "default", portSpec{3030, 3030, "server1", ""}},
		// Host override + name
		{"3030@server1=api", "default", portSpec{3030, 3030, "server1", "api"}},
		// Full spec
		{"3030:8080@server1=api", "default", portSpec{3030, 8080, "server1", "api"}},
		// User@host syntax
		{"3030@cw@server1=api", "default", portSpec{3030, 3030, "cw@server1", "api"}},
		// No default host, with @host
		{"3030@server1", "", portSpec{3030, 3030, "server1", ""}},
		// No default host, no @host
		{"3030", "", portSpec{3030, 3030, "", ""}},
		// Name with hyphens
		{"5432=my-postgres-db", "h", portSpec{5432, 5432, "h", "my-postgres-db"}},
	}

	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			got := parsePortSpec(tt.spec, tt.defaultHost)
			if got != tt.want {
				t.Errorf("parsePortSpec(%q, %q)\n  got  %+v\n  want %+v", tt.spec, tt.defaultHost, got, tt.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name   string
		tunnel tunnel
		want   string
	}{
		{
			name:   "explicit name wins",
			tunnel: tunnel{RemotePort: 5432, Name: "my-db"},
			want:   "my-db",
		},
		{
			name:   "well-known port fallback",
			tunnel: tunnel{RemotePort: 5432},
			want:   "postgres",
		},
		{
			name:   "well-known redis",
			tunnel: tunnel{RemotePort: 6379},
			want:   "redis",
		},
		{
			name:   "well-known mysql",
			tunnel: tunnel{RemotePort: 3306},
			want:   "mysql",
		},
		{
			name:   "well-known mongo",
			tunnel: tunnel{RemotePort: 27017},
			want:   "mongo",
		},
		{
			name:   "well-known prometheus",
			tunnel: tunnel{RemotePort: 9090},
			want:   "prometheus",
		},
		{
			name:   "unknown port returns empty",
			tunnel: tunnel{RemotePort: 3025},
			want:   "",
		},
		{
			name:   "explicit name overrides well-known",
			tunnel: tunnel{RemotePort: 5432, Name: "bridge"},
			want:   "bridge",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.tunnel.displayName()
			if got != tt.want {
				t.Errorf("displayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLabel(t *testing.T) {
	tests := []struct {
		name   string
		tunnel tunnel
		want   string
	}{
		{
			name:   "same ports",
			tunnel: tunnel{LocalPort: 3025, RemotePort: 3025},
			want:   ":3025",
		},
		{
			name:   "different ports",
			tunnel: tunnel{LocalPort: 3025, RemotePort: 8080},
			want:   ":3025 → :8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.tunnel.label()
			if got != tt.want {
				t.Errorf("label() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUptime(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		want    string
	}{
		{"seconds", 45 * time.Second, "45s"},
		{"minutes", 5 * time.Minute, "5m"},
		{"hours", 2*time.Hour + 15*time.Minute, "2h15m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tun := tunnel{StartedAt: time.Now().Add(-tt.elapsed)}
			got := tun.uptime()
			if got != tt.want {
				t.Errorf("uptime() = %q, want %q", got, tt.want)
			}
		})
	}
}

// helper to populate a tunnelManager without spawning SSH
func seedManager(tunnels ...tunnel) *tunnelManager {
	tm := newTunnelManager()
	tm.tunnels = append(tm.tunnels, tunnels...)
	return tm
}

func TestTunnelManagerList(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, RemotePort: 3025, Host: "h1", Active: true, PID: 0},
		tunnel{LocalPort: 5432, RemotePort: 5432, Host: "h2", Active: true, PID: 0},
	)

	list := tm.list()
	if len(list) != 2 {
		t.Fatalf("expected 2 tunnels, got %d", len(list))
	}
	if list[0].LocalPort != 3025 {
		t.Errorf("first tunnel port = %d, want 3025", list[0].LocalPort)
	}
	if list[1].Host != "h2" {
		t.Errorf("second tunnel host = %q, want %q", list[1].Host, "h2")
	}
}

func TestTunnelManagerListEmpty(t *testing.T) {
	tm := newTunnelManager()
	list := tm.list()
	if len(list) != 0 {
		t.Fatalf("expected 0 tunnels, got %d", len(list))
	}
}

func TestTunnelManagerListReturnsCopy(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, RemotePort: 3025, Host: "h1", Active: true},
	)
	list := tm.list()
	list[0].Host = "mutated"

	// Original should be unchanged
	original := tm.list()
	if original[0].Host != "h1" {
		t.Error("list() did not return a copy — mutation leaked through")
	}
}

func TestTunnelManagerRemove(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, Host: "h1", Active: true, PID: 0},
		tunnel{LocalPort: 5432, Host: "h2", Active: true, PID: 0},
		tunnel{LocalPort: 6379, Host: "h3", Active: true, PID: 0},
	)

	// Remove middle
	if err := tm.remove(1); err != nil {
		t.Fatalf("remove(1) error: %v", err)
	}
	list := tm.list()
	if len(list) != 2 {
		t.Fatalf("expected 2 tunnels after remove, got %d", len(list))
	}
	if list[0].LocalPort != 3025 || list[1].LocalPort != 6379 {
		t.Errorf("wrong tunnels remaining: %d, %d", list[0].LocalPort, list[1].LocalPort)
	}
}

func TestTunnelManagerRemoveFirst(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, Host: "h1", PID: 0},
		tunnel{LocalPort: 5432, Host: "h2", PID: 0},
	)

	if err := tm.remove(0); err != nil {
		t.Fatalf("remove(0) error: %v", err)
	}
	list := tm.list()
	if len(list) != 1 || list[0].LocalPort != 5432 {
		t.Errorf("expected [5432], got %v", list)
	}
}

func TestTunnelManagerRemoveLast(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, Host: "h1", PID: 0},
		tunnel{LocalPort: 5432, Host: "h2", PID: 0},
	)

	if err := tm.remove(1); err != nil {
		t.Fatalf("remove(1) error: %v", err)
	}
	list := tm.list()
	if len(list) != 1 || list[0].LocalPort != 3025 {
		t.Errorf("expected [3025], got %v", list)
	}
}

func TestTunnelManagerRemoveInvalidIndex(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, Host: "h1", PID: 0},
	)

	if err := tm.remove(-1); err == nil {
		t.Error("expected error for negative index")
	}
	if err := tm.remove(1); err == nil {
		t.Error("expected error for out-of-range index")
	}
	if err := tm.remove(99); err == nil {
		t.Error("expected error for way-out-of-range index")
	}
}

func TestTunnelManagerRemoveAll(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, PID: 0},
		tunnel{LocalPort: 5432, PID: 0},
		tunnel{LocalPort: 6379, PID: 0},
	)

	tm.removeAll()
	list := tm.list()
	if len(list) != 0 {
		t.Errorf("expected 0 tunnels after removeAll, got %d", len(list))
	}
}

func TestTunnelManagerRemoveAllEmpty(t *testing.T) {
	tm := newTunnelManager()
	tm.removeAll() // should not panic
	if len(tm.list()) != 0 {
		t.Error("expected empty list")
	}
}

func TestTunnelManagerDuplicatePortDetection(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, RemotePort: 3025, Host: "h1", Active: true, PID: 0},
	)

	// Simulating what add() checks: active tunnel on same local port
	// We can't call add() (it spawns ssh), so verify the check logic directly
	var found bool
	for _, tun := range tm.list() {
		if tun.LocalPort == 3025 && tun.Active {
			found = true
		}
	}
	if !found {
		t.Error("expected to find active tunnel on port 3025")
	}
}

func TestTunnelManagerMultiHost(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, Host: "server1", Name: "bridge", Active: true, PID: 0},
		tunnel{LocalPort: 5432, Host: "server2", Name: "db", Active: true, PID: 0},
		tunnel{LocalPort: 8080, Host: "server1", Name: "api", Active: true, PID: 0},
	)

	list := tm.list()
	if len(list) != 3 {
		t.Fatalf("expected 3 tunnels, got %d", len(list))
	}

	// Verify different hosts are preserved
	hosts := map[string]int{}
	for _, tun := range list {
		hosts[tun.Host]++
	}
	if hosts["server1"] != 2 || hosts["server2"] != 1 {
		t.Errorf("unexpected host distribution: %v", hosts)
	}
}

func TestCountActive(t *testing.T) {
	tunnels := []tunnel{
		{Active: true},
		{Active: false},
		{Active: true},
		{Active: true},
		{Active: false},
	}
	if got := countActive(tunnels); got != 3 {
		t.Errorf("countActive() = %d, want 3", got)
	}
}

func TestUniqueHosts(t *testing.T) {
	tests := []struct {
		name    string
		tunnels []tunnel
		want    []string
	}{
		{"empty", nil, nil},
		{"single", []tunnel{{Host: "h1"}}, []string{"h1"}},
		{"deduped", []tunnel{{Host: "h1"}, {Host: "h1"}, {Host: "h2"}}, []string{"h1", "h2"}},
		{"preserves order", []tunnel{{Host: "b"}, {Host: "a"}, {Host: "b"}}, []string{"b", "a"}},
		{"skips empty host", []tunnel{{Host: ""}, {Host: "h1"}}, []string{"h1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uniqueHosts(tt.tunnels)
			if len(got) != len(tt.want) {
				t.Fatalf("uniqueHosts() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("uniqueHosts()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRecentHosts(t *testing.T) {
	m := model{
		defaultHost: "default-host",
		tunnels: []tunnel{
			{Host: "default-host"}, // same as default, should not duplicate
			{Host: "other-host"},
		},
	}

	got := m.recentHosts()
	want := []string{"default-host", "other-host"}
	if len(got) != len(want) {
		t.Fatalf("recentHosts() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("recentHosts()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRecentHostsNoDefault(t *testing.T) {
	m := model{
		tunnels: []tunnel{
			{Host: "server1"},
			{Host: "server2"},
		},
	}

	got := m.recentHosts()
	if len(got) != 2 || got[0] != "server1" || got[1] != "server2" {
		t.Errorf("recentHosts() = %v, want [server1, server2]", got)
	}
}

func TestRecentHostsEmpty(t *testing.T) {
	m := model{}
	got := m.recentHosts()
	if len(got) != 0 {
		t.Errorf("recentHosts() = %v, want empty", got)
	}
}

func TestTunnelManagerRename(t *testing.T) {
	tm := seedManager(
		tunnel{LocalPort: 3025, Host: "h1", Name: "old-name", PID: 0},
		tunnel{LocalPort: 5432, Host: "h2", Name: "", PID: 0},
	)

	// Rename first tunnel
	if err := tm.rename(0, "new-name"); err != nil {
		t.Fatalf("rename(0) error: %v", err)
	}
	if tm.list()[0].Name != "new-name" {
		t.Errorf("name = %q, want %q", tm.list()[0].Name, "new-name")
	}

	// Rename second tunnel (was empty)
	if err := tm.rename(1, "postgres"); err != nil {
		t.Fatalf("rename(1) error: %v", err)
	}
	if tm.list()[1].Name != "postgres" {
		t.Errorf("name = %q, want %q", tm.list()[1].Name, "postgres")
	}

	// Clear name
	if err := tm.rename(0, ""); err != nil {
		t.Fatalf("rename(0, empty) error: %v", err)
	}
	if tm.list()[0].Name != "" {
		t.Errorf("name = %q, want empty", tm.list()[0].Name)
	}

	// Invalid index
	if err := tm.rename(-1, "x"); err == nil {
		t.Error("expected error for negative index")
	}
	if err := tm.rename(99, "x"); err == nil {
		t.Error("expected error for out-of-range index")
	}
}

func TestCountActiveEmpty(t *testing.T) {
	if got := countActive(nil); got != 0 {
		t.Errorf("countActive(nil) = %d, want 0", got)
	}
}

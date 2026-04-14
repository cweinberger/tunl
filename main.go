package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Styles — cyberpunk neon palette (matching tmux-connect)
// ---------------------------------------------------------------------------

var (
	colorPrimary   = lipgloss.Color("#00E5FF") // electric cyan
	colorSecondary = lipgloss.Color("#BD00FF") // neon purple
	colorAccent    = lipgloss.Color("#39FF14") // neon green
	colorDim       = lipgloss.Color("#4A5568") // dark gray
	colorMuted     = lipgloss.Color("#718096") // muted gray
	colorHighlight = lipgloss.Color("#1A1F3D") // highlight bg
	colorWhite     = lipgloss.Color("#E2E8F0") // soft white
	colorRed       = lipgloss.Color("#FF0055") // hot pink-red
	colorYellow    = lipgloss.Color("#FFE600") // electric yellow
	colorCyan      = lipgloss.Color("#00E5FF") // cyan
	colorMagenta   = lipgloss.Color("#FF00FF") // magenta
	colorElecBlue  = lipgloss.Color("#0080FF") // electric blue
	colorHotPink   = lipgloss.Color("#FF2D95") // hot pink

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary)

	tunnelBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.ThickBorder()).
			BorderForeground(colorSecondary).
			Padding(0, 1)

	tunnelItemStyle = lipgloss.NewStyle().
			Foreground(colorWhite).
			Padding(0, 1)

	tunnelSelectedStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Background(colorHighlight).
				Padding(0, 1)

	cursorStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	shortcutStyle = lipgloss.NewStyle().
			Foreground(colorYellow)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(colorMagenta)

	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(colorSecondary).
			Padding(0, 1).
			MarginTop(1)

	quitBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(colorRed).
			Padding(1, 2).
			MarginTop(1)

	activeDot = lipgloss.NewStyle().
			Foreground(colorAccent).
			Render("\u25C9") // ◉

	failedDot = lipgloss.NewStyle().
			Foreground(colorRed).
			Render("\u25C9") // ◉
)

// ---------------------------------------------------------------------------
// Tunnel data
// ---------------------------------------------------------------------------

type tunnel struct {
	LocalPort  int
	RemotePort int
	Host       string
	Name       string
	PID        int
	Active     bool
	StartedAt  time.Time
}

// wellKnownPorts maps common dev ports to human-readable names.
var wellKnownPorts = map[int]string{
	80:    "http",
	443:   "https",
	1433:  "mssql",
	3000:  "dev-server",
	3306:  "mysql",
	5432:  "postgres",
	5672:  "rabbitmq",
	6379:  "redis",
	8080:  "http-alt",
	8443:  "https-alt",
	9090:  "prometheus",
	9200:  "elasticsearch",
	15672: "rabbitmq-mgmt",
	27017: "mongo",
}

func (t tunnel) displayName() string {
	if t.Name != "" {
		return t.Name
	}
	if name, ok := wellKnownPorts[t.RemotePort]; ok {
		return name
	}
	return ""
}

func (t tunnel) label() string {
	if t.LocalPort == t.RemotePort {
		return fmt.Sprintf(":%d", t.LocalPort)
	}
	return fmt.Sprintf(":%d → :%d", t.LocalPort, t.RemotePort)
}

func (t tunnel) uptime() string {
	d := time.Since(t.StartedAt)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// ---------------------------------------------------------------------------
// State file — persists tunnel metadata across restarts
// ---------------------------------------------------------------------------

type tunnelState struct {
	PID        int    `json:"pid"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	Host       string `json:"host"`
	Name       string `json:"name,omitempty"`
}

func stateFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tunl.json")
}

func loadState() map[int]tunnelState {
	data, err := os.ReadFile(stateFilePath())
	if err != nil {
		return nil
	}
	var states []tunnelState
	if err := json.Unmarshal(data, &states); err != nil {
		return nil
	}
	m := make(map[int]tunnelState, len(states))
	for _, s := range states {
		m[s.PID] = s
	}
	return m
}

func saveState(tunnels []tunnel) {
	var states []tunnelState
	for _, t := range tunnels {
		if t.PID > 0 && t.Active {
			states = append(states, tunnelState{
				PID:        t.PID,
				LocalPort:  t.LocalPort,
				RemotePort: t.RemotePort,
				Host:       t.Host,
				Name:       t.Name,
			})
		}
	}
	data, err := json.Marshal(states)
	if err != nil {
		return
	}
	_ = os.WriteFile(stateFilePath(), data, 0600)
}

// ---------------------------------------------------------------------------
// Tunnel manager — tracks spawned SSH processes
// ---------------------------------------------------------------------------

type tunnelManager struct {
	mu      sync.Mutex
	tunnels []tunnel
}

func newTunnelManager() *tunnelManager {
	return &tunnelManager{}
}

func (tm *tunnelManager) list() []tunnel {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Check which tunnels are still alive
	for i := range tm.tunnels {
		if tm.tunnels[i].PID > 0 {
			process, err := os.FindProcess(tm.tunnels[i].PID)
			if err != nil {
				tm.tunnels[i].Active = false
				continue
			}
			err = process.Signal(syscall.Signal(0))
			tm.tunnels[i].Active = err == nil
		}
	}

	result := make([]tunnel, len(tm.tunnels))
	copy(result, tm.tunnels)
	return result
}

func (tm *tunnelManager) add(localPort, remotePort int, host, name string) (tunnel, error) {
	// Check if port is already in use by our tunnels
	tm.mu.Lock()
	for _, t := range tm.tunnels {
		if t.LocalPort == localPort && t.Active {
			tm.mu.Unlock()
			return tunnel{}, fmt.Errorf("port %d already forwarded", localPort)
		}
	}
	tm.mu.Unlock()

	// Start SSH tunnel
	cmd := exec.Command("ssh",
		"-o", "ConnectTimeout=5",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-N", // no remote command
		"-L", fmt.Sprintf("%d:localhost:%d", localPort, remotePort),
		host,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return tunnel{}, fmt.Errorf("failed to start tunnel: %w", err)
	}

	t := tunnel{
		LocalPort:  localPort,
		RemotePort: remotePort,
		Host:       host,
		Name:       name,
		PID:        cmd.Process.Pid,
		Active:     true,
		StartedAt:  time.Now(),
	}

	tm.mu.Lock()
	tm.tunnels = append(tm.tunnels, t)
	saveState(tm.tunnels)
	tm.mu.Unlock()

	// Wait for process in background so it gets reaped
	go cmd.Wait()

	return t, nil
}

func (tm *tunnelManager) remove(idx int) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if idx < 0 || idx >= len(tm.tunnels) {
		return fmt.Errorf("invalid tunnel index")
	}

	t := tm.tunnels[idx]
	if t.PID > 0 {
		if p, err := os.FindProcess(t.PID); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}

	tm.tunnels = append(tm.tunnels[:idx], tm.tunnels[idx+1:]...)
	saveState(tm.tunnels)
	return nil
}

func (tm *tunnelManager) rename(idx int, name string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if idx < 0 || idx >= len(tm.tunnels) {
		return fmt.Errorf("invalid tunnel index")
	}
	tm.tunnels[idx].Name = name
	saveState(tm.tunnels)
	return nil
}

func (tm *tunnelManager) removeAll() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	for _, t := range tm.tunnels {
		if t.PID > 0 {
			if p, err := os.FindProcess(t.PID); err == nil {
				_ = p.Signal(syscall.SIGTERM)
			}
		}
	}
	tm.tunnels = nil
	saveState(tm.tunnels)
}

// getProcessStartTime reads the start time of a process from ps
func getProcessStartTime(pid int) time.Time {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Now()
	}
	// lstart format: "Mon Jan  2 15:04:05 2006" (local time)
	s := strings.TrimSpace(string(out))
	t, err := time.ParseInLocation("Mon Jan  2 15:04:05 2006", s, time.Local)
	if err != nil {
		t, err = time.ParseInLocation("Mon Jan 2 15:04:05 2006", s, time.Local)
		if err != nil {
			return time.Now()
		}
	}
	return t
}

// detectExisting finds ssh -L tunnels already running on the system
func (tm *tunnelManager) detectExisting() {
	out, err := exec.Command("ps", "-eo", "pid,args").Output()
	if err != nil {
		return
	}

	re := regexp.MustCompile(`(\d+)\s+ssh\s.*-L\s+(\d+):localhost:(\d+)\s+(\S+)`)
	myPid := os.Getpid()
	saved := loadState()

	tm.mu.Lock()
	defer tm.mu.Unlock()

	for _, line := range strings.Split(string(out), "\n") {
		matches := re.FindStringSubmatch(strings.TrimSpace(line))
		if matches == nil {
			continue
		}
		pid, _ := strconv.Atoi(matches[1])
		if pid == myPid {
			continue
		}

		localPort, _ := strconv.Atoi(matches[2])
		remotePort, _ := strconv.Atoi(matches[3])
		host := matches[4]

		// Skip if already tracked
		alreadyTracked := false
		for _, t := range tm.tunnels {
			if t.PID == pid {
				alreadyTracked = true
				break
			}
		}
		if alreadyTracked {
			continue
		}

		// Restore name from saved state if available
		name := ""
		if s, ok := saved[pid]; ok {
			name = s.Name
		}

		tm.tunnels = append(tm.tunnels, tunnel{
			LocalPort:  localPort,
			RemotePort: remotePort,
			Host:       host,
			Name:       name,
			PID:        pid,
			Active:     true,
			StartedAt:  getProcessStartTime(pid),
		})
	}
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type tunnelsRefreshedMsg struct{}
type statusMsg string
type clearStatusMsg struct{}
type tickMsg time.Time
type tunnelAddedMsg struct{ t tunnel }
type tunnelErrorMsg struct{ err error }

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type viewMode int

const (
	viewNormal viewMode = iota
	viewAddTunnel
	viewConfirmKill
	viewQuit
	viewRename
)

type inputField int

const (
	fieldLocalPort inputField = iota
	fieldRemotePort
	fieldHost
	fieldName
)

const (
	menuAdd = iota
	menuRefresh
	menuKillAll
	menuQuit
)

type menuItem struct {
	id    int
	label string
	key   string
}

type visibleItem struct {
	kind      string // "tunnel", "menu"
	tunnelIdx int
	menuIdx   int
}

type model struct {
	manager     *tunnelManager
	tunnels     []tunnel
	cursor      int
	spinner     spinner.Model
	status      string
	statusErr   bool
	mode        viewMode
	quitIdx     int
	width       int
	height      int
	quote       string
	defaultHost string

	// Add tunnel inputs
	inputLocal  textinput.Model
	inputRemote textinput.Model
	inputHost   textinput.Model
	inputName   textinput.Model
	activeField inputField
	hostPickIdx int // cycles through recent hosts with ctrl+j/ctrl+k

	// Rename input
	inputRename  textinput.Model
	renamingIdx  int // tunnel index being renamed
}

// recentHosts returns unique hosts from active tunnels + default host.
func (m model) recentHosts() []string {
	seen := map[string]bool{}
	var hosts []string
	// Default host first
	if m.defaultHost != "" {
		seen[m.defaultHost] = true
		hosts = append(hosts, m.defaultHost)
	}
	for _, t := range m.tunnels {
		if t.Host != "" && !seen[t.Host] {
			seen[t.Host] = true
			hosts = append(hosts, t.Host)
		}
	}
	return hosts
}

func initialModel(defaultHost string, mgr *tunnelManager) model {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	s.Style = spinnerStyle

	il := textinput.New()
	il.Placeholder = "local port"
	il.CharLimit = 5
	il.Width = 12
	il.PromptStyle = lipgloss.NewStyle().Foreground(colorMagenta)
	il.TextStyle = lipgloss.NewStyle().Foreground(colorPrimary)

	ir := textinput.New()
	ir.Placeholder = "remote port (same if empty)"
	ir.CharLimit = 5
	ir.Width = 28
	ir.PromptStyle = lipgloss.NewStyle().Foreground(colorMagenta)
	ir.TextStyle = lipgloss.NewStyle().Foreground(colorPrimary)

	ih := textinput.New()
	if defaultHost != "" {
		ih.Placeholder = defaultHost
	} else {
		ih.Placeholder = "user@host (required)"
	}
	ih.CharLimit = 128
	ih.Width = 30
	ih.PromptStyle = lipgloss.NewStyle().Foreground(colorMagenta)
	ih.TextStyle = lipgloss.NewStyle().Foreground(colorPrimary)

	in := textinput.New()
	in.Placeholder = "optional label"
	in.CharLimit = 30
	in.Width = 30
	in.PromptStyle = lipgloss.NewStyle().Foreground(colorMagenta)
	in.TextStyle = lipgloss.NewStyle().Foreground(colorPrimary)

	iren := textinput.New()
	iren.Placeholder = "tunnel name"
	iren.CharLimit = 30
	iren.Width = 30
	iren.PromptStyle = lipgloss.NewStyle().Foreground(colorMagenta)
	iren.TextStyle = lipgloss.NewStyle().Foreground(colorPrimary)

	return model{
		manager:     mgr,
		tunnels:     mgr.list(),
		spinner:     s,
		width:       80,
		quote:       quotes[rand.Intn(len(quotes))],
		defaultHost: defaultHost,
		inputLocal:  il,
		inputRemote: ir,
		inputHost:   ih,
		inputName:   in,
		inputRename: iren,
	}
}

func (m model) menuItems() []menuItem {
	items := []menuItem{
		{menuAdd, "New tunnel", "n"},
		{menuRefresh, "Refresh", "r"},
	}
	if len(m.tunnels) > 0 {
		items = append(items, menuItem{menuKillAll, "Kill all", "K"})
	}
	items = append(items, menuItem{menuQuit, "Quit", "q"})
	return items
}

func (m model) visibleItems() []visibleItem {
	var items []visibleItem
	for i := range m.tunnels {
		items = append(items, visibleItem{kind: "tunnel", tunnelIdx: i})
	}
	for i := range m.menuItems() {
		items = append(items, visibleItem{kind: "menu", menuIdx: i})
	}
	return items
}

func (m model) totalItems() int {
	return len(m.visibleItems())
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

func refreshTunnels(mgr *tunnelManager) tea.Cmd {
	return func() tea.Msg {
		mgr.detectExisting()
		return tunnelsRefreshedMsg{}
	}
}

func addTunnel(mgr *tunnelManager, localPort, remotePort int, host, name string) tea.Cmd {
	return func() tea.Msg {
		t, err := mgr.add(localPort, remotePort, host, name)
		if err != nil {
			return tunnelErrorMsg{err: err}
		}
		// Brief wait to let SSH establish
		time.Sleep(500 * time.Millisecond)
		return tunnelAddedMsg{t: t}
	}
}

func autoRefreshTick() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func clearStatusAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return clearStatusMsg{}
	})
}

// ---------------------------------------------------------------------------
// Init / Update / View
// ---------------------------------------------------------------------------

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		refreshTunnels(m.manager),
		autoRefreshTick(),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tunnelsRefreshedMsg:
		m.tunnels = m.manager.list()
		if m.cursor >= m.totalItems() {
			m.cursor = max(0, m.totalItems()-1)
		}
		return m, nil

	case tickMsg:
		m.tunnels = m.manager.list()
		if m.cursor >= m.totalItems() {
			m.cursor = max(0, m.totalItems()-1)
		}
		return m, autoRefreshTick()

	case tunnelAddedMsg:
		m.tunnels = m.manager.list()
		label := fmt.Sprintf(":%d → %s:%d", msg.t.LocalPort, msg.t.Host, msg.t.RemotePort)
		if name := msg.t.displayName(); name != "" {
			label = fmt.Sprintf("%s (%s)", label, name)
		}
		m.status = fmt.Sprintf("Tunnel %s opened", label)
		m.statusErr = false
		return m, clearStatusAfter(3 * time.Second)

	case tunnelErrorMsg:
		m.status = msg.err.Error()
		m.statusErr = true
		// Reopen add form so user can edit and retry (inputs are preserved)
		if m.inputLocal.Value() != "" {
			m.mode = viewAddTunnel
			m.inputLocal.Focus()
			return m, tea.Batch(clearStatusAfter(5*time.Second), m.inputLocal.Cursor.BlinkCmd())
		}
		return m, clearStatusAfter(5 * time.Second)

	case statusMsg:
		m.status = string(msg)
		m.statusErr = false
		m.tunnels = m.manager.list()
		return m, clearStatusAfter(3 * time.Second)

	case clearStatusMsg:
		m.status = ""
		m.statusErr = false
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		switch m.mode {

		case viewAddTunnel:
			return m.updateAddTunnel(msg)

		case viewConfirmKill:
			switch msg.String() {
			case "y", "enter":
				vis := m.visibleItems()
				if m.cursor >= 0 && m.cursor < len(vis) {
					item := vis[m.cursor]
					if item.kind == "tunnel" {
						name := m.tunnels[item.tunnelIdx].label()
						_ = m.manager.remove(item.tunnelIdx)
						m.tunnels = m.manager.list()
						m.status = fmt.Sprintf("Killed tunnel %s", name)
						m.statusErr = false
						if m.cursor >= m.totalItems() {
							m.cursor = max(0, m.totalItems()-1)
						}
					}
				}
				m.mode = viewNormal
				return m, clearStatusAfter(3 * time.Second)
			case "n", "esc":
				m.mode = viewNormal
				return m, nil
			}
			return m, nil

		case viewRename:
			switch msg.String() {
			case "esc":
				m.mode = viewNormal
				return m, nil
			case "enter":
				newName := strings.TrimSpace(m.inputRename.Value())
				_ = m.manager.rename(m.renamingIdx, newName)
				m.tunnels = m.manager.list()
				m.mode = viewNormal
				if newName != "" {
					m.status = fmt.Sprintf("Renamed to %s", newName)
				} else {
					m.status = "Name cleared"
				}
				m.statusErr = false
				return m, clearStatusAfter(3 * time.Second)
			default:
				var cmd tea.Cmd
				m.inputRename, cmd = m.inputRename.Update(msg)
				return m, cmd
			}

		case viewQuit:
			switch msg.String() {
			case "esc":
				m.mode = viewNormal
				return m, nil
			case "up", "k":
				if m.quitIdx > 0 {
					m.quitIdx--
				}
				return m, nil
			case "down", "j":
				if m.quitIdx < 2 {
					m.quitIdx++
				}
				return m, nil
			case "enter":
				switch m.quitIdx {
				case 0: // Kill all & quit
					m.manager.removeAll()
					return m, tea.Quit
				case 1: // Just quit (tunnels keep running)
					return m, tea.Quit
				case 2: // Cancel
					m.mode = viewNormal
					return m, nil
				}
			case "q":
				m.manager.removeAll()
				return m, tea.Quit
			}
			return m, nil

		case viewNormal:
			total := m.totalItems()
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				} else {
					m.cursor = total - 1
				}
				return m, nil
			case "down", "j":
				if m.cursor < total-1 {
					m.cursor++
				} else {
					m.cursor = 0
				}
				return m, nil
			case "enter":
				return m.handleEnter()
			case "n":
				m.mode = viewAddTunnel
				m.activeField = fieldLocalPort
				m.inputLocal.Reset()
				m.inputRemote.Reset()
				m.inputHost.Reset()
				m.inputName.Reset()
				m.hostPickIdx = 0
				m.inputLocal.Focus()
				return m, m.inputLocal.Cursor.BlinkCmd()
			case "r":
				m.manager.detectExisting()
				m.tunnels = m.manager.list()
				m.status = "Refreshed"
				m.statusErr = false
				return m, clearStatusAfter(2 * time.Second)
			case "x", "delete", "backspace":
				vis := m.visibleItems()
				if m.cursor >= 0 && m.cursor < len(vis) && vis[m.cursor].kind == "tunnel" {
					m.mode = viewConfirmKill
				}
				return m, nil
			case "e":
				vis := m.visibleItems()
				if m.cursor >= 0 && m.cursor < len(vis) && vis[m.cursor].kind == "tunnel" {
					m.renamingIdx = vis[m.cursor].tunnelIdx
					m.inputRename.SetValue(m.tunnels[m.renamingIdx].Name)
					m.inputRename.CursorEnd()
					m.inputRename.Focus()
					m.mode = viewRename
					return m, m.inputRename.Cursor.BlinkCmd()
				}
				return m, nil
			case "K":
				if len(m.tunnels) > 0 {
					m.manager.removeAll()
					m.tunnels = m.manager.list()
					m.status = "All tunnels killed"
					m.statusErr = false
					m.cursor = 0
					return m, clearStatusAfter(3 * time.Second)
				}
				return m, nil
			case "o":
				// Open in browser
				vis := m.visibleItems()
				if m.cursor >= 0 && m.cursor < len(vis) && vis[m.cursor].kind == "tunnel" {
					t := m.tunnels[vis[m.cursor].tunnelIdx]
					_ = exec.Command("open", fmt.Sprintf("http://localhost:%d", t.LocalPort)).Start()
					m.status = fmt.Sprintf("Opened http://localhost:%d", t.LocalPort)
					m.statusErr = false
					return m, clearStatusAfter(3 * time.Second)
				}
				return m, nil
			case "q":
				m.mode = viewQuit
				m.quitIdx = 0
				return m, nil
			default:
				// Number keys 1-9 for quick open in browser
				if len(msg.String()) == 1 && msg.String()[0] >= '1' && msg.String()[0] <= '9' {
					idx := int(msg.String()[0] - '1')
					if idx < len(m.tunnels) && m.tunnels[idx].Active {
						t := m.tunnels[idx]
						_ = exec.Command("open", fmt.Sprintf("http://localhost:%d", t.LocalPort)).Start()
						m.status = fmt.Sprintf("Opened http://localhost:%d", t.LocalPort)
						m.statusErr = false
						return m, clearStatusAfter(3 * time.Second)
					}
				}
			}
		}
	}

	return m, nil
}

func (m model) updateAddTunnel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = viewNormal
		return m, nil
	case "tab", "down":
		switch m.activeField {
		case fieldLocalPort:
			m.activeField = fieldRemotePort
			m.inputLocal.Blur()
			m.inputRemote.Focus()
			return m, m.inputRemote.Cursor.BlinkCmd()
		case fieldRemotePort:
			m.activeField = fieldHost
			m.inputRemote.Blur()
			m.inputHost.Focus()
			return m, m.inputHost.Cursor.BlinkCmd()
		case fieldHost:
			m.activeField = fieldName
			m.inputHost.Blur()
			m.inputName.Focus()
			return m, m.inputName.Cursor.BlinkCmd()
		case fieldName:
			m.activeField = fieldLocalPort
			m.inputName.Blur()
			m.inputLocal.Focus()
			return m, m.inputLocal.Cursor.BlinkCmd()
		}
	case "shift+tab", "up":
		switch m.activeField {
		case fieldLocalPort:
			m.activeField = fieldName
			m.inputLocal.Blur()
			m.inputName.Focus()
			return m, m.inputName.Cursor.BlinkCmd()
		case fieldRemotePort:
			m.activeField = fieldLocalPort
			m.inputRemote.Blur()
			m.inputLocal.Focus()
			return m, m.inputLocal.Cursor.BlinkCmd()
		case fieldHost:
			m.activeField = fieldRemotePort
			m.inputHost.Blur()
			m.inputRemote.Focus()
			return m, m.inputRemote.Cursor.BlinkCmd()
		case fieldName:
			m.activeField = fieldHost
			m.inputName.Blur()
			m.inputHost.Focus()
			return m, m.inputHost.Cursor.BlinkCmd()
		}
	case "ctrl+j", "ctrl+k":
		// Cycle through recent hosts when host field is active
		if m.activeField == fieldHost {
			recent := m.recentHosts()
			if len(recent) > 0 {
				if msg.String() == "ctrl+j" {
					m.hostPickIdx = (m.hostPickIdx + 1) % len(recent)
				} else {
					m.hostPickIdx = (m.hostPickIdx - 1 + len(recent)) % len(recent)
				}
				m.inputHost.SetValue(recent[m.hostPickIdx])
				m.inputHost.CursorEnd()
			}
		}
		return m, nil
	case "enter":
		localStr := strings.TrimSpace(m.inputLocal.Value())
		if localStr == "" {
			return m, nil
		}
		localPort, err := strconv.Atoi(localStr)
		if err != nil || localPort < 1 || localPort > 65535 {
			m.status = "Invalid local port"
			m.statusErr = true
			return m, clearStatusAfter(3 * time.Second)
		}

		remotePort := localPort
		remoteStr := strings.TrimSpace(m.inputRemote.Value())
		if remoteStr != "" {
			remotePort, err = strconv.Atoi(remoteStr)
			if err != nil || remotePort < 1 || remotePort > 65535 {
				m.status = "Invalid remote port"
				m.statusErr = true
				return m, clearStatusAfter(3 * time.Second)
			}
		}

		host := strings.TrimSpace(m.inputHost.Value())
		if host == "" {
			host = m.defaultHost
		}
		if host == "" {
			m.status = "Host required (set TUNL_DEFAULT_HOST or enter one)"
			m.statusErr = true
			return m, clearStatusAfter(3 * time.Second)
		}

		name := strings.TrimSpace(m.inputName.Value())

		m.mode = viewNormal
		return m, tea.Batch(m.spinner.Tick, addTunnel(m.manager, localPort, remotePort, host, name))
	default:
		var cmd tea.Cmd
		switch m.activeField {
		case fieldLocalPort:
			m.inputLocal, cmd = m.inputLocal.Update(msg)
		case fieldRemotePort:
			m.inputRemote, cmd = m.inputRemote.Update(msg)
		case fieldHost:
			m.inputHost, cmd = m.inputHost.Update(msg)
		case fieldName:
			m.inputName, cmd = m.inputName.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

func (m model) handleEnter() (tea.Model, tea.Cmd) {
	vis := m.visibleItems()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return m, nil
	}
	item := vis[m.cursor]

	switch item.kind {
	case "tunnel":
		// Open in browser
		t := m.tunnels[item.tunnelIdx]
		if t.Active {
			_ = exec.Command("open", fmt.Sprintf("http://localhost:%d", t.LocalPort)).Start()
			m.status = fmt.Sprintf("Opened http://localhost:%d", t.LocalPort)
			m.statusErr = false
			return m, clearStatusAfter(3 * time.Second)
		}
		return m, nil
	case "menu":
		menus := m.menuItems()
		if item.menuIdx < 0 || item.menuIdx >= len(menus) {
			return m, nil
		}
		switch menus[item.menuIdx].id {
		case menuAdd:
			m.mode = viewAddTunnel
			m.activeField = fieldLocalPort
			m.inputLocal.Reset()
			m.inputRemote.Reset()
			m.inputHost.Reset()
			m.inputName.Reset()
			m.hostPickIdx = 0
			m.inputLocal.Focus()
			return m, m.inputLocal.Cursor.BlinkCmd()
		case menuRefresh:
			m.manager.detectExisting()
			m.tunnels = m.manager.list()
			m.status = "Refreshed"
			m.statusErr = false
			return m, clearStatusAfter(2 * time.Second)
		case menuKillAll:
			m.manager.removeAll()
			m.tunnels = m.manager.list()
			m.status = "All tunnels killed"
			m.statusErr = false
			m.cursor = 0
			return m, clearStatusAfter(3 * time.Second)
		case menuQuit:
			m.mode = viewQuit
			m.quitIdx = 0
			return m, nil
		}
	}
	return m, nil
}

func (m model) View() string {
	var b strings.Builder

	lineWidth := max(10, m.width-4)

	// ── Header — ANSI Shadow figlet logo ──
	logo := []string{
		"████████╗██╗   ██╗███╗   ██╗██╗     ",
		"╚══██╔══╝██║   ██║████╗  ██║██║     ",
		"   ██║   ██║   ██║██╔██╗ ██║██║     ",
		"   ██║   ██║   ██║██║╚██╗██║██║     ",
		"   ██║   ╚██████╔╝██║ ╚████║███████╗",
		"   ╚═╝    ╚═════╝ ╚═╝  ╚═══╝╚══════╝",
	}
	logoColors := []lipgloss.Color{"#00E5FF", "#00C0FF", "#8050FF", "#BD00FF", "#E000D0", "#FF2D95"}

	b.WriteString("\n")
	for i, line := range logo {
		b.WriteString(lipgloss.NewStyle().Foreground(logoColors[i]).Bold(true).Render(line) + "\n")
	}
	b.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render("  ── local port forward manager ──") + "\n\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#4A5568")).Render("  "+m.quote) + "\n")
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(colorSecondary).Render("  "+strings.Repeat("─", lineWidth)) + "\n")
	b.WriteString("\n")

	// Host info bar — show unique hosts from active tunnels
	hosts := uniqueHosts(m.tunnels)
	var hostDisplay string
	switch {
	case len(hosts) == 0 && m.defaultHost != "":
		hostDisplay = m.defaultHost
	case len(hosts) == 0:
		hostDisplay = "no host"
	case len(hosts) == 1:
		hostDisplay = hosts[0]
	default:
		hostDisplay = fmt.Sprintf("%d hosts", len(hosts))
	}

	hostLine := lipgloss.JoinHorizontal(lipgloss.Center,
		lipgloss.NewStyle().Foreground(colorDim).Render("  \u25C8 "),
		titleStyle.Render(hostDisplay),
		lipgloss.NewStyle().Foreground(colorDim).Render("  \u2502 "),
		lipgloss.NewStyle().Foreground(colorMagenta).Render("ssh -L"),
		lipgloss.NewStyle().Foreground(colorDim).Render("  \u2502 "),
		lipgloss.NewStyle().Foreground(colorElecBlue).Render(fmt.Sprintf("%d active", countActive(m.tunnels))),
		lipgloss.NewStyle().Foreground(colorDim).Render("  \u25C8"),
	)
	b.WriteString(hostLine + "\n")

	thinSep := lipgloss.NewStyle().Foreground(colorDim).Render(
		"  " + strings.Repeat("\u2508", max(5, lineWidth-4)))
	b.WriteString(thinSep + "\n\n")

	boxWidth := max(10, m.width-6)
	vis := m.visibleItems()

	// ── Tunnel list ──
	if len(m.tunnels) == 0 {
		emptyMsg := lipgloss.NewStyle().
			Foreground(colorDim).
			Padding(1, 2).
			Render("\u25B3 No active tunnels. Press " +
				lipgloss.NewStyle().Foreground(colorYellow).Render("[n]") +
				lipgloss.NewStyle().Foreground(colorDim).Render(" to create one."))
		b.WriteString(tunnelBoxStyle.Width(boxWidth).Render(emptyMsg) + "\n")
	} else {
		var boxItems []string

		sectionHeader := lipgloss.NewStyle().Foreground(colorMagenta).Render("\u2590") +
			lipgloss.NewStyle().Foreground(colorPrimary).
				Render(fmt.Sprintf(" ACTIVE TUNNELS \u00AB%d\u00BB", len(m.tunnels)))
		boxItems = append(boxItems, sectionHeader)

		innerSep := lipgloss.NewStyle().Foreground(colorDim).Render(
			strings.Repeat("\u2500", max(5, boxWidth-6)))
		boxItems = append(boxItems, innerSep)

		for visIdx, vi := range vis {
			if vi.kind != "tunnel" {
				break
			}

			t := m.tunnels[vi.tunnelIdx]
			i := vi.tunnelIdx

			shortcut := "   "
			if i < 9 {
				shortcut = shortcutStyle.Render(fmt.Sprintf("[%d]", i+1))
			}

			isSelected := visIdx == m.cursor && (m.mode == viewNormal || m.mode == viewConfirmKill)
			cursor := "  "
			style := tunnelItemStyle
			if isSelected {
				cursor = cursorStyle.Render("\u25B8 ")
				style = tunnelSelectedStyle
			}

			dot := failedDot
			if t.Active {
				dot = activeDot
			}

			portInfo := lipgloss.NewStyle().Foreground(colorWhite).Bold(true).Render(t.label())
			nameInfo := ""
			if name := t.displayName(); name != "" {
				nameInfo = lipgloss.NewStyle().Foreground(colorHotPink).Bold(true).Render(fmt.Sprintf("  %s", name))
			}
			hostColor := colorDim
			if len(hosts) > 1 {
				hostColor = colorElecBlue
			}
			hostInfo := lipgloss.NewStyle().Foreground(hostColor).Render(fmt.Sprintf("  → %s", t.Host))
			uptimeInfo := ""
			if t.Active {
				uptimeInfo = lipgloss.NewStyle().Foreground(colorYellow).Render(fmt.Sprintf("  ⏱ %s", t.uptime()))
			} else {
				uptimeInfo = lipgloss.NewStyle().Foreground(colorRed).Render("  ✗ dead")
			}

			urlInfo := lipgloss.NewStyle().Foreground(colorCyan).Render(
				fmt.Sprintf("  http://localhost:%d", t.LocalPort))

			prefix := lipgloss.NewStyle().Foreground(colorDim).Render("\u2502")
			line := prefix + " " + style.Render(fmt.Sprintf("%s %s %s %s%s%s%s%s", shortcut, cursor, dot, portInfo, nameInfo, hostInfo, uptimeInfo, urlInfo))
			boxItems = append(boxItems, line)
		}

		// Legend
		boxItems = append(boxItems, lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("\u2500", max(5, boxWidth-6))))
		legend := lipgloss.NewStyle().Foreground(colorDim).Render(
			fmt.Sprintf("  %s connected  %s dead  ⏱ uptime", activeDot, failedDot))
		boxItems = append(boxItems, legend)

		box := tunnelBoxStyle.Width(boxWidth).Render(strings.Join(boxItems, "\n"))
		b.WriteString(box + "\n")
	}

	// ── Confirm kill ──
	if m.mode == viewConfirmKill {
		vis := m.visibleItems()
		if m.cursor >= 0 && m.cursor < len(vis) && vis[m.cursor].kind == "tunnel" {
			t := m.tunnels[vis[m.cursor].tunnelIdx]
			confirmHeader := lipgloss.NewStyle().Foreground(colorRed).Render("  \u26A0 TERMINATE TUNNEL")
			confirmMsg := lipgloss.NewStyle().Foreground(colorHotPink).Render(
				fmt.Sprintf("  \u2192 %s → %s", t.label(), t.Host))
			confirmHint := lipgloss.NewStyle().Foreground(colorDim).
				Render("  " + lipgloss.NewStyle().Foreground(colorAccent).Render("[y]") + " confirm  " +
					lipgloss.NewStyle().Foreground(colorRed).Render("[n]") + " abort")
			confirmContent := confirmHeader + "\n" + confirmMsg + "\n\n" + confirmHint
			killBox := lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(colorRed).
				Padding(0, 1).
				MarginTop(1).
				Width(min(55, m.width-6)).
				Render(confirmContent)
			b.WriteString(killBox + "\n")
		}
	}

	// ── Rename input ──
	if m.mode == viewRename && m.renamingIdx < len(m.tunnels) {
		t := m.tunnels[m.renamingIdx]
		renameHeader := lipgloss.NewStyle().Foreground(colorMagenta).
			Render("  ▐ RENAME TUNNEL") + "\n" +
			lipgloss.NewStyle().Foreground(colorDim).Render("  "+strings.Repeat("─", 20))
		tunnelInfo := lipgloss.NewStyle().Foreground(colorCyan).Render(
			fmt.Sprintf("  %s → %s", t.label(), t.Host))
		nameLabel := lipgloss.NewStyle().Foreground(colorDim).Render("  Name: ")

		renameContent := renameHeader + "\n" + tunnelInfo + "\n\n" +
			cursorStyle.Render("▸ ") + nameLabel + m.inputRename.View() + "\n\n" +
			lipgloss.NewStyle().Foreground(colorDim).Render("  ") +
			lipgloss.NewStyle().Foreground(colorAccent).Render("enter") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" save  │  ") +
			lipgloss.NewStyle().Foreground(colorRed).Render("esc") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" cancel")

		renameBox := inputBoxStyle.Width(min(55, m.width-6)).Render(renameContent)
		b.WriteString(renameBox + "\n")
	}

	// ── Add tunnel input ──
	if m.mode == viewAddTunnel {
		inputHeader := lipgloss.NewStyle().Foreground(colorMagenta).
			Render("  \u2590 NEW TUNNEL") + "\n" +
			lipgloss.NewStyle().Foreground(colorDim).Render("  "+strings.Repeat("\u2500", 20))

		localLabel := lipgloss.NewStyle().Foreground(colorDim).Render("  Local port:  ")
		remoteLabel := lipgloss.NewStyle().Foreground(colorDim).Render("  Remote port: ")
		hostLabel := lipgloss.NewStyle().Foreground(colorDim).Render("  Host:        ")
		nameLabel := lipgloss.NewStyle().Foreground(colorDim).Render("  Name:        ")

		fieldIndicator := func(f inputField) string {
			if m.activeField == f {
				return cursorStyle.Render("\u25B8 ")
			}
			return "  "
		}

		// Recent hosts hint (shown below host field when it's active)
		hostHint := ""
		if m.activeField == fieldHost {
			recent := m.recentHosts()
			if len(recent) > 0 {
				var parts []string
				for i, h := range recent {
					style := lipgloss.NewStyle().Foreground(colorDim)
					if i == m.hostPickIdx {
						style = lipgloss.NewStyle().Foreground(colorPrimary)
					}
					parts = append(parts, style.Render(h))
				}
				hostHint = "\n" + lipgloss.NewStyle().Foreground(colorDim).Render("               ") +
					lipgloss.NewStyle().Foreground(colorDim).Render("  ↕ ") +
					lipgloss.NewStyle().Foreground(colorAccent).Render("ctrl+j/k") +
					lipgloss.NewStyle().Foreground(colorDim).Render(" ") +
					strings.Join(parts, lipgloss.NewStyle().Foreground(colorDim).Render(" · "))
			}
		}

		inputContent := inputHeader + "\n\n" +
			fieldIndicator(fieldLocalPort) + localLabel + m.inputLocal.View() + "\n" +
			fieldIndicator(fieldRemotePort) + remoteLabel + m.inputRemote.View() + "\n" +
			fieldIndicator(fieldHost) + hostLabel + m.inputHost.View() + hostHint + "\n" +
			fieldIndicator(fieldName) + nameLabel + m.inputName.View() + "\n\n" +
			lipgloss.NewStyle().Foreground(colorDim).Render("  ") +
			lipgloss.NewStyle().Foreground(colorAccent).Render("tab") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" next field  \u2502  ") +
			lipgloss.NewStyle().Foreground(colorAccent).Render("enter") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" connect  \u2502  ") +
			lipgloss.NewStyle().Foreground(colorRed).Render("esc") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" cancel")

		b.WriteString(inputBoxStyle.Width(min(60, m.width-6)).Render(inputContent) + "\n")
	}

	// ── Quit menu ──
	if m.mode == viewQuit {
		quitTitle := lipgloss.NewStyle().Foreground(colorRed).
			Render("  \u2590 EXIT PROTOCOL")

		opts := []string{
			"Kill all tunnels & quit",
			"Just quit (tunnels keep running)",
			"Cancel",
		}
		optIcons := []string{"\u2612", "\u2190", "\u21BA"}
		var quitItems []string
		quitItems = append(quitItems, quitTitle)
		quitItems = append(quitItems, lipgloss.NewStyle().Foreground(colorDim).Render("  "+strings.Repeat("\u2500", 35)))
		for i, opt := range opts {
			qCursor := "  "
			style := lipgloss.NewStyle().Foreground(colorWhite).Padding(0, 1)
			if i == m.quitIdx {
				qCursor = cursorStyle.Render("\u25B8 ")
				style = lipgloss.NewStyle().Foreground(colorRed).Background(lipgloss.Color("#1A0010")).Padding(0, 1)
			}
			icon := lipgloss.NewStyle().Foreground(colorDim).Render(optIcons[i])
			quitItems = append(quitItems, fmt.Sprintf("  %s%s %s", qCursor, icon, style.Render(opt)))
		}
		quitItems = append(quitItems, "")
		quitItems = append(quitItems, lipgloss.NewStyle().Foreground(colorDim).Render("  ")+
			lipgloss.NewStyle().Foreground(colorAccent).Render("enter")+
			lipgloss.NewStyle().Foreground(colorDim).Render(" select  \u2502  ")+
			lipgloss.NewStyle().Foreground(colorRed).Render("esc")+
			lipgloss.NewStyle().Foreground(colorDim).Render(" back"))

		b.WriteString(quitBoxStyle.Width(min(55, m.width-6)).Render(strings.Join(quitItems, "\n")) + "\n")
	}

	// ── Status ──
	if m.status != "" {
		if m.statusErr {
			b.WriteString(lipgloss.NewStyle().Foreground(colorRed).MarginTop(1).Render("  \u2718 "+m.status) + "\n")
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(colorAccent).MarginTop(1).Render("  \u2714 "+m.status) + "\n")
		}
	}

	topContent := b.String()
	topHeight := strings.Count(topContent, "\n")

	// ── Bottom bar ──
	var bottom strings.Builder
	if m.mode == viewNormal {
		var menuParts []string
		menus := m.menuItems()
		for mi, item := range menus {
			globalIdx := -1
			for vi, v := range vis {
				if v.kind == "menu" && v.menuIdx == mi {
					globalIdx = vi
					break
				}
			}

			isSelected := globalIdx == m.cursor
			keyStyle := lipgloss.NewStyle().Foreground(colorDim)
			labelStyle := lipgloss.NewStyle().Foreground(colorMuted)

			if item.id == menuKillAll {
				keyStyle = lipgloss.NewStyle().Foreground(colorRed)
				labelStyle = lipgloss.NewStyle().Foreground(colorRed)
			}

			var part string
			if isSelected {
				keyStyle = lipgloss.NewStyle().Foreground(colorPrimary)
				labelStyle = lipgloss.NewStyle().Foreground(colorWhite)
				if item.id == menuKillAll {
					keyStyle = lipgloss.NewStyle().Foreground(colorRed)
					labelStyle = lipgloss.NewStyle().Foreground(colorRed)
				}
				part = cursorStyle.Render("\u25B8") + keyStyle.Render(item.key) + " " + labelStyle.Render(item.label)
			} else {
				part = keyStyle.Render(item.key) + " " + labelStyle.Render(item.label)
			}
			menuParts = append(menuParts, part)
		}

		sep := lipgloss.NewStyle().Foreground(colorDim).Render("  \u2502  ")
		barLine := "  " + strings.Join(menuParts, sep)

		bottom.WriteString(lipgloss.NewStyle().Foreground(colorSecondary).Render("  "+strings.Repeat("\u2500", lineWidth)) + "\n")
		bottom.WriteString(barLine + "\n")

		hintLine := lipgloss.NewStyle().Foreground(colorDim).Render("  ") +
			lipgloss.NewStyle().Foreground(colorCyan).Render("\u2191\u2193") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" navigate  ") +
			lipgloss.NewStyle().Foreground(colorCyan).Render("enter/o") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" open in browser  ") +
			lipgloss.NewStyle().Foreground(colorCyan).Render("x") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" kill  ") +
			lipgloss.NewStyle().Foreground(colorCyan).Render("e") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" rename  ") +
			lipgloss.NewStyle().Foreground(colorCyan).Render("1-9") +
			lipgloss.NewStyle().Foreground(colorDim).Render(" quick open")
		bottom.WriteString(hintLine + "\n")
	}
	bottomContent := bottom.String()
	bottomHeight := strings.Count(bottomContent, "\n")

	gap := m.height - topHeight - bottomHeight - 1
	if gap < 1 {
		gap = 1
	}

	return topContent + strings.Repeat("\n", gap) + bottomContent
}

func uniqueHosts(tunnels []tunnel) []string {
	seen := map[string]bool{}
	var hosts []string
	for _, t := range tunnels {
		if t.Host != "" && !seen[t.Host] {
			seen[t.Host] = true
			hosts = append(hosts, t.Host)
		}
	}
	return hosts
}

func countActive(tunnels []tunnel) int {
	n := 0
	for _, t := range tunnels {
		if t.Active {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	defaultHost := os.Getenv("TUNL_DEFAULT_HOST")
	args := os.Args[1:]

	// Parse flags and optional initial tunnels
	var initialPorts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			if i+1 < len(args) {
				defaultHost = args[i+1]
				i++
			}
		case "-h", "--help":
			fmt.Println(helpText())
			os.Exit(0)
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", args[i])
				os.Exit(1)
			}
			initialPorts = append(initialPorts, args[i])
		}
	}

	mgr := newTunnelManager()
	mgr.detectExisting()

	// Open any initial tunnels specified on command line
	for _, spec := range initialPorts {
		parsed := parsePortSpec(spec, defaultHost)

		if parsed.host == "" {
			fmt.Fprintf(os.Stderr, "Error: no host for port %d. Use --host or @host syntax\n", parsed.localPort)
			os.Exit(1)
		}

		if parsed.localPort > 0 {
			_, _ = mgr.add(parsed.localPort, parsed.remotePort, parsed.host, parsed.name)
		}
	}

	p := tea.NewProgram(
		initialModel(defaultHost, mgr),
		tea.WithAltScreen(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print("\033[2J\033[H")
}

// portSpec holds the parsed result of a port[:remoteport][@host][=name] string.
type portSpec struct {
	localPort  int
	remotePort int
	host       string
	name       string
}

// parsePortSpec parses a spec like "3030:8080@server=api" into its parts.
// defaultHost is used when no @host is present in the spec.
func parsePortSpec(spec, defaultHost string) portSpec {
	var name string
	if eqIdx := strings.LastIndex(spec, "="); eqIdx != -1 {
		name = spec[eqIdx+1:]
		spec = spec[:eqIdx]
	}

	// Split port part from host part.
	// The port spec is digits (and optionally :digits), so find the first @
	// after the port portion. We locate the boundary by finding where the
	// numeric port spec ends.
	var localPort, remotePort int
	host := defaultHost
	portPart := spec

	// Find the first @ that follows the port numbers
	if atIdx := strings.IndexByte(spec, '@'); atIdx != -1 {
		// Verify everything before @ looks like a port spec (digits and colon)
		candidate := spec[:atIdx]
		isPortSpec := true
		for _, c := range candidate {
			if c != ':' && (c < '0' || c > '9') {
				isPortSpec = false
				break
			}
		}
		if isPortSpec {
			host = spec[atIdx+1:]
			portPart = candidate
		}
	}

	if strings.Contains(portPart, ":") {
		parts := strings.SplitN(portPart, ":", 2)
		localPort, _ = strconv.Atoi(parts[0])
		remotePort, _ = strconv.Atoi(parts[1])
	} else {
		localPort, _ = strconv.Atoi(portPart)
		remotePort = localPort
	}

	return portSpec{
		localPort:  localPort,
		remotePort: remotePort,
		host:       host,
		name:       name,
	}
}

func helpText() string {
	return `tunl - SSH local port forward manager

Usage:
  tunl [--host user@remote] [port[:remoteport][@host][=name] ...]

Options:
  --host   SSH host (overrides TUNL_DEFAULT_HOST env var)
  -h       Show this help

Port spec:
  port                       Local = remote, default host
  port:remoteport            Different local/remote ports
  port@host                  Per-tunnel host override
  port:remoteport@host       Remote port + host override
  port=name                  Named tunnel
  port:remoteport@host=name  Full spec

Examples:
  tunl                                  # TUI only, no initial tunnels
  tunl 3025                             # Open tunnel :3025 and launch TUI
  tunl 3025:8080 5432                   # Open two tunnels and launch TUI
  tunl --host cw@server 3025            # Custom host
  tunl 3030=rfx-engine 3025=bridge      # Named tunnels
  tunl 3030@server1=api 5432@server2=db # Per-tunnel hosts with names

  Well-known ports (e.g. 5432, 6379) get auto-named if no name is given.

Controls:
  Up/Down    Navigate tunnels
  Enter/o    Open tunnel URL in browser
  n          New tunnel
  e          Rename selected tunnel
  x          Kill selected tunnel
  K          Kill all tunnels
  r          Refresh (detect existing tunnels)
  1-9        Quick open in browser
  q          Quit menu

Tunnels keep running when you "Just quit".
Auto-detects existing ssh -L tunnels on startup.`
}

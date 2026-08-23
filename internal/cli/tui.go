package cli

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
	"github.com/deungjaho/hydra/internal/provider"
	"github.com/deungjaho/hydra/internal/proxy"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("36"))
	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("36"))
	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))
	panelTitleActive = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("36"))
	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("236"))
	grayStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	greenStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellowStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	redStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	magentaStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	blueStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	boldStyle      = lipgloss.NewStyle().Bold(true)
	helpStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	helpKeyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("36")).Bold(true)
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("238")).
			Foreground(lipgloss.Color("252"))
)

type tab int

const (
	tabAccounts tab = iota
	tabProviders
	tabLogs
	tabModels
	tabKeys
	tabStatus
)

var tabNames = []string{"Accounts", "Providers", "Logs", "Models", "Keys", "Status"}

type stats struct {
	accountCount     int
	activeAccounts   int
	disabledAccounts int
	requestCount     int
	successCount     int
	errorCount       int
	rateLimitedCount int
	promptTokens     int64
	completionTokens int64
	cachedTokens     int64
	thoughtTokens    int64
	modelUsage       map[string]int
}

func computeStats(accounts []*account.Account, logs []*account.RequestLog) stats {
	var s stats
	s.accountCount = len(accounts)
	for _, a := range accounts {
		if !a.Disabled() {
			s.activeAccounts++
		}
	}
	s.disabledAccounts = s.accountCount - s.activeAccounts
	s.requestCount = len(logs)
	for _, l := range logs {
		if l.Status >= 200 && l.Status < 300 {
			s.successCount++
		} else if l.Status >= 400 && l.Status != 429 {
			s.errorCount++
		} else if l.Status == 429 {
			s.rateLimitedCount++
		}
		if l.PromptTokens != nil {
			s.promptTokens += *l.PromptTokens
		}
		if l.CompletionTokens != nil {
			s.completionTokens += *l.CompletionTokens
		}
		if l.CachedTokens != nil {
			s.cachedTokens += *l.CachedTokens
		}
		if l.ThoughtTokens != nil {
			s.thoughtTokens += *l.ThoughtTokens
		}
		if l.Model != nil {
			if s.modelUsage == nil {
				s.modelUsage = make(map[string]int)
			}
			s.modelUsage[*l.Model]++
		}
	}
	return s
}

type refreshMsg struct{ text string }
type tickMsg struct{}

// inputMode tracks inline input state for TUI operations.
type inputMode int

const (
	inputNone            inputMode = iota
	inputAddKey                    // typing label for new key
	inputAddProviderName           // typing provider name
	inputAddProviderURL            // typing provider base URL
	inputAddProviderKey            // typing provider API key
	inputOAuthDevice               // OAuth device flow: waiting for user
)

type tuiModel struct {
	db             *db.Db
	tab            tab
	cursor         int
	logScroll      int
	modelsCur      int
	keys           []*account.ApiKey
	accounts       []*account.Account
	providers      []*provider.Provider
	logs           []*account.RequestLog
	models         []string
	providerModels []*provider.ModelMapping
	stats          stats
	statusMsg      string
	statusTime     time.Time
	width          int
	height     int
	quitting   bool
	// Inline input
	inputMode inputMode
	textInput textinput.Model
	// Key detail view
	showFullKey   string // non-empty = showing full key overlay
	showFullKeyID int64
	// Log detail overlay (Logs tab)
	showLogDetail int64 // log ID; 0 = no overlay
	// Status tab time window
	statusWindow statusWindow
	// Status tab aggregated usage (per window)
	usageByModel   []account.UsageRow
	usageByAccount []account.UsageRow
	usageByKey     []account.KeyUsage
	// Provider addition multi-step input
	providerFormName string
	providerFormURL  string
	// OAuth device flow state
	oauthDeviceCode *account.DeviceCodeResult
	oauthClient     *http.Client
	oauthConfig     *config.AppConfig
}

type statusWindow int

const (
	windowWeek statusWindow = iota
	windowDay
	windowMonth
	windowAll
)

func (w statusWindow) String() string {
	switch w {
	case windowDay:
		return "day"
	case windowMonth:
		return "month"
	case windowAll:
		return "all"
	}
	return "week"
}

func (w statusWindow) Since() int64 {
	switch w {
	case windowDay:
		return time.Now().Unix() - 86400
	case windowMonth:
		return time.Now().Unix() - 86400*30
	case windowAll:
		return 0
	}
	return time.Now().Unix() - 86400*7
}

func newTUIModel(d *db.Db, cfg *config.AppConfig) tuiModel {
	m := tuiModel{db: d, oauthConfig: cfg}
	ti := textinput.New()
	ti.CharLimit = 50
	m.textInput = ti
	if cfg != nil {
		m.oauthClient = proxy.NewHTTPClient(60*time.Second, cfg.Proxy.UpstreamProxy)
	}
	m.refreshData()
	return m
}

func (m *tuiModel) refreshData() {
	var errs []string
	if v, err := account.ListAccounts(m.db); err != nil {
		errs = append(errs, "accounts: "+err.Error())
	} else {
		m.accounts = v
	}
	if v, err := provider.ListProviders(m.db); err != nil {
		errs = append(errs, "providers: "+err.Error())
	} else {
		m.providers = v
	}
	if v, err := account.RecentLogs(m.db, 500); err != nil {
		errs = append(errs, "logs: "+err.Error())
	} else {
		m.logs = v
	}
	if v, err := account.ListAPIKeys(m.db); err != nil {
		errs = append(errs, "keys: "+err.Error())
	} else {
		m.keys = v
	}
	m.models = proxy.DynamicModelList(m.accounts)
	if v, err := provider.ListModelMappings(m.db); err != nil {
		errs = append(errs, "provider_models: "+err.Error())
	} else {
		m.providerModels = v
	}
	m.stats = computeStats(m.accounts, m.logs)
	// Aggregated usage for Status tab (per time window).
	since := m.statusWindow.Since()
	if v, err := account.UsageByModel(m.db, since); err != nil {
		errs = append(errs, "usage_model: "+err.Error())
	} else {
		m.usageByModel = v
	}
	if v, err := account.UsageByAccount(m.db, since); err != nil {
		errs = append(errs, "usage_account: "+err.Error())
	} else {
		m.usageByAccount = v
	}
	if v, err := account.UsageByKey(m.db, since); err != nil {
		errs = append(errs, "usage_key: "+err.Error())
	} else {
		m.usageByKey = v
	}
	if len(errs) > 0 && m.statusMsg == "" {
		m.statusMsg = "DB error: " + strings.Join(errs, "; ")
		m.statusTime = time.Now()
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Handle input mode first — all keys go to the text input.
	if m.inputMode != inputNone {
		return m.handleInputMode(msg)
	}
	// Handle full-key overlay — any key dismisses it.
	if m.showFullKey != "" {
		if _, ok := msg.(tea.KeyMsg); ok {
			m.showFullKey = ""
			m.showFullKeyID = 0
		}
		return m, nil
	}
	// Handle log detail overlay — any key dismisses it.
	if m.showLogDetail != 0 {
		if _, ok := msg.(tea.KeyMsg); ok {
			m.showLogDetail = 0
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.quitting {
			return m, tea.Quit
		}
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tickMsg:
		m.refreshData()
		if m.statusMsg != "" && time.Since(m.statusTime) > 3*time.Second {
			m.statusMsg = ""
		}
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
	case refreshMsg:
		m.statusMsg = msg.text
		m.statusTime = time.Now()
		m.refreshData()
		return m, nil
	}
	return m, nil
}

func (m tuiModel) handleInputMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	// OAuth device flow mode is handled separately (no text input).
	if m.inputMode == inputOAuthDevice {
		return m.handleOAuthDevice(msg)
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			return m.handleInputEnter()
		case "esc", "ctrl+c":
			m.inputMode = inputNone
			m.textInput.Reset()
			m.providerFormName = ""
			m.providerFormURL = ""
			m.statusMsg = "Cancelled"
			m.statusTime = time.Now()
			return m, nil
		}
		// Forward to text input.
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleInputEnter processes the Enter key for all text-input modes.
func (m tuiModel) handleInputEnter() (tea.Model, tea.Cmd) {
	switch m.inputMode {
	case inputAddKey:
		label := m.textInput.Value()
		if label == "" {
			m.statusMsg = "Label cannot be empty"
			m.statusTime = time.Now()
			m.inputMode = inputNone
			m.textInput.Reset()
			return m, nil
		}
		newKey := "hydra-" + strings.ReplaceAll(uuid.NewString(), "-", "")
		id, err := account.AddAPIKey(m.db, newKey, label)
		if err != nil {
			m.statusMsg = "Failed: " + err.Error()
		} else {
			m.statusMsg = fmt.Sprintf("Key #%d created: %s (label: %s)", id, newKey, label)
		}
		m.statusTime = time.Now()
		m.inputMode = inputNone
		m.textInput.Reset()
		m.refreshData()
		return m, nil

	case inputAddProviderName:
		name := strings.TrimSpace(m.textInput.Value())
		if name == "" {
			m.statusMsg = "Name cannot be empty"
			m.statusTime = time.Now()
			return m, nil
		}
		m.providerFormName = name
		m.inputMode = inputAddProviderURL
		m.textInput.Reset()
		m.textInput.Focus()
		m.textInput.Prompt = "Base URL: "
		m.statusMsg = fmt.Sprintf("Adding provider: %s", name)
		m.statusTime = time.Now()
		return m, textinput.Blink

	case inputAddProviderURL:
		baseURL := strings.TrimSpace(m.textInput.Value())
		if baseURL == "" {
			m.statusMsg = "Base URL cannot be empty"
			m.statusTime = time.Now()
			return m, nil
		}
		m.providerFormURL = baseURL
		m.inputMode = inputAddProviderKey
		m.textInput.Reset()
		m.textInput.Focus()
		m.textInput.Prompt = "API Key: "
		m.statusMsg = fmt.Sprintf("Adding provider: %s → %s", m.providerFormName, baseURL)
		m.statusTime = time.Now()
		return m, textinput.Blink

	case inputAddProviderKey:
		apiKey := strings.TrimSpace(m.textInput.Value())
		if apiKey == "" {
			m.statusMsg = "API key cannot be empty"
			m.statusTime = time.Now()
			return m, nil
		}
		id, err := provider.AddProvider(m.db, m.providerFormName, m.providerFormURL, apiKey)
		if err != nil {
			m.statusMsg = "Failed: " + err.Error()
		} else {
			m.statusMsg = fmt.Sprintf("Provider #%d created: %s", id, m.providerFormName)
		}
		m.statusTime = time.Now()
		m.inputMode = inputNone
		m.textInput.Reset()
		m.providerFormName = ""
		m.providerFormURL = ""
		m.refreshData()
		return m, nil
	}

	// Fallback (shouldn't happen).
	m.inputMode = inputNone
	m.textInput.Reset()
	return m, nil
}

// oauthPollMsg is sent periodically to check if the OAuth device flow
// authorization has completed.
type oauthPollMsg struct {
	result *account.AuthResult
	err    error
}

// handleOAuthDevice handles the OAuth device flow waiting state.
// The user sees the verification URL and code, and the TUI polls
// in the background until authorization completes.
func (m tuiModel) handleOAuthDevice(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c", "q":
			m.inputMode = inputNone
			m.oauthDeviceCode = nil
			m.statusMsg = "OAuth cancelled"
			m.statusTime = time.Now()
			return m, nil
		}
	case oauthPollMsg:
		if msg.err != nil {
			if msg.err == account.ErrDevicePending {
				// Keep polling.
				interval := 5 * time.Second
				if m.oauthDeviceCode != nil && m.oauthDeviceCode.Interval > 0 {
					interval = time.Duration(m.oauthDeviceCode.Interval) * time.Second
				}
				return m, tea.Tick(interval, func(time.Time) tea.Msg {
					return m.pollOAuth()
				})
			}
			if msg.err == account.ErrDeviceSlowDown {
				return m, tea.Tick(10*time.Second, func(time.Time) tea.Msg {
					return m.pollOAuth()
				})
			}
			// Real error.
			m.inputMode = inputNone
			m.oauthDeviceCode = nil
			m.statusMsg = "OAuth failed: " + msg.err.Error()
			m.statusTime = time.Now()
			return m, nil
		}
		// Success — save the account.
		if msg.result != nil {
			id, err := account.AddAccount(m.db, msg.result.Email,
				msg.result.AccessToken, msg.result.RefreshToken,
				msg.result.ProjectID, msg.result.ExpiresAt)
			if err != nil {
				m.statusMsg = "Save failed: " + err.Error()
			} else {
				m.statusMsg = fmt.Sprintf("Account #%d added: %s", id, msg.result.Email)
			}
		}
		m.inputMode = inputNone
		m.oauthDeviceCode = nil
		m.statusTime = time.Now()
		m.refreshData()
		return m, nil
	}
	return m, nil
}

// pollOAuth polls the OAuth device token endpoint. This is called
// via tea.Tick so it runs as a command, not blocking the UI.
func (m tuiModel) pollOAuth() tea.Msg {
	if m.oauthClient == nil || m.oauthDeviceCode == nil {
		return oauthPollMsg{err: fmt.Errorf("no OAuth flow in progress")}
	}
	result, err := account.PollDeviceToken(m.oauthClient, m.oauthDeviceCode.DeviceCode)
	return oauthPollMsg{result: result, err: err}
}

// startOAuthDeviceFlow initiates the OAuth device flow.
func (m *tuiModel) startOAuthDeviceFlow() (tea.Model, tea.Cmd) {
	if m.oauthClient == nil {
		m.statusMsg = "OAuth not available (no HTTP client)"
		m.statusTime = time.Now()
		return *m, nil
	}
	dc, err := account.RequestDeviceCode(m.oauthClient)
	if err != nil {
		m.statusMsg = "OAuth device code request failed: " + err.Error()
		m.statusTime = time.Now()
		return *m, nil
	}
	m.oauthDeviceCode = dc
	m.inputMode = inputOAuthDevice
	m.statusMsg = "Waiting for authorization..."
	m.statusTime = time.Now()
	interval := time.Duration(dc.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}
	return *m, tea.Tick(interval, func(time.Time) tea.Msg {
		return m.pollOAuth()
	})
}

func (m tuiModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	tabBarY := 1
	if msg.Y == tabBarY && msg.Type == tea.MouseLeft {
		x := 0
		for i, name := range tabNames {
			tabW := len(name) + 2
			if msg.X >= x && msg.X < x+tabW {
				m.setTab(tab(i))
				return m, nil
			}
			x += tabW + 1
		}
	}
	return m, nil
}

func (m tuiModel) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	_ = key.NewBinding
	switch k.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		// esc quits only if not in a sub-view
		m.quitting = true
		return m, tea.Quit
	case "tab", "l", "ctrl+f":
		m.nextTab()
	case "shift+tab", "h", "ctrl+b":
		m.prevTab()
	case "1":
		m.setTab(tabAccounts)
	case "2":
		m.setTab(tabProviders)
	case "3":
		m.setTab(tabLogs)
	case "4":
		m.setTab(tabModels)
	case "5":
		m.setTab(tabKeys)
	case "6":
		m.setTab(tabStatus)
	case "j", "ctrl+n", "down":
		m.moveCursor(1)
	case "k", "ctrl+p", "up":
		m.moveCursor(-1)
	case "g", "ctrl+a", "home":
		m.cursorTop()
	case "G", "ctrl+e", "end":
		m.cursorBottom()
	case "ctrl+d", "pgdown":
		if m.tab == tabLogs {
			m.logScroll += m.bodyHeight() / 2
			m.clampLogScroll()
		}
	case "ctrl+u", "pgup":
		if m.tab == tabLogs {
			m.logScroll -= m.bodyHeight() / 2
			m.clampLogScroll()
		}
	case "r":
		if m.tab == tabAccounts || m.tab == tabStatus {
			return m, tea.Cmd(m.refreshQuota)
		}
	case "a":
		if m.tab == tabAccounts {
			// Start OAuth device flow for account addition.
			return m.startOAuthDeviceFlow()
		} else if m.tab == tabProviders {
			// Enter inline input mode for provider name.
			m.inputMode = inputAddProviderName
			m.textInput.Reset()
			m.textInput.Focus()
			m.textInput.Prompt = "Name: "
			m.statusMsg = ""
			return m, textinput.Blink
		} else if m.tab == tabKeys {
			// Enter inline input mode for key label.
			m.inputMode = inputAddKey
			m.textInput.Reset()
			m.textInput.Focus()
			m.textInput.Prompt = "Label: "
			m.statusMsg = ""
			return m, textinput.Blink
		}
	case "D":
		m.handleDelete()
	case "e":
		m.handleToggle()
	case "K":
		// Rotate selected key (Keys tab) — generates new key string.
		if m.tab == tabKeys && m.cursor < len(m.keys) {
			k := m.keys[m.cursor]
			newKey := "hydra-" + strings.ReplaceAll(uuid.NewString(), "-", "")
			if err := account.RotateAPIKey(m.db, k.ID, newKey); err != nil {
				m.statusMsg = "Rotate failed: " + err.Error()
			} else {
				m.statusMsg = fmt.Sprintf("Key #%d rotated: %s", k.ID, newKey)
			}
			m.statusTime = time.Now()
			m.refreshData()
		}
	case "s":
		// Show full key (Keys tab).
		if m.tab == tabKeys && m.cursor < len(m.keys) {
			k := m.keys[m.cursor]
			m.showFullKey = k.Key
			m.showFullKeyID = k.ID
		}
	case " ":
		// Space: show log detail overlay (Logs tab).
		if m.tab == tabLogs && m.logScroll+m.cursor < len(m.logs) {
			l := m.logs[m.logScroll+m.cursor]
			m.showLogDetail = l.ID
		}
	case "w":
		// Cycle time window (Status tab).
		if m.tab == tabStatus {
			switch m.statusWindow {
			case windowWeek:
				m.statusWindow = windowDay
			case windowDay:
				m.statusWindow = windowMonth
			case windowMonth:
				m.statusWindow = windowAll
			case windowAll:
				m.statusWindow = windowWeek
			}
			m.statusMsg = fmt.Sprintf("Window: %s", m.statusWindow)
			m.statusTime = time.Now()
		}
	case "m":
		if m.tab == tabStatus {
			m.cycleSchedulingMode()
		}
	}
	return m, nil
}

func (m *tuiModel) handleDelete() {
	if m.tab == tabAccounts && m.cursor < len(m.accounts) {
		a := m.accounts[m.cursor]
		if err := account.RemoveAccount(m.db, a.ID); err != nil {
			m.statusMsg = "Delete failed: " + err.Error()
		} else {
			m.statusMsg = fmt.Sprintf("Removed account #%d (%s)", a.ID, a.Email)
		}
		if m.cursor > 0 {
			m.cursor--
		}
		m.statusTime = time.Now()
		m.refreshData()
	} else if m.tab == tabProviders && m.cursor < len(m.providers) {
		p := m.providers[m.cursor]
		if err := provider.RemoveProvider(m.db, p.ID); err != nil {
			m.statusMsg = "Delete failed: " + err.Error()
		} else {
			m.statusMsg = fmt.Sprintf("Removed provider #%d (%s)", p.ID, p.Name)
		}
		if m.cursor > 0 {
			m.cursor--
		}
		m.statusTime = time.Now()
		m.refreshData()
	} else if m.tab == tabKeys && m.cursor < len(m.keys) {
		k := m.keys[m.cursor]
		if err := account.RemoveAPIKey(m.db, k.ID); err != nil {
			m.statusMsg = "Delete failed: " + err.Error()
		} else {
			m.statusMsg = fmt.Sprintf("Removed API key #%d (%s)", k.ID, k.Label)
		}
		if m.cursor > 0 {
			m.cursor--
		}
		m.statusTime = time.Now()
		m.refreshData()
	}
}

func (m *tuiModel) handleToggle() {
	if m.tab == tabAccounts && m.cursor < len(m.accounts) {
		a := m.accounts[m.cursor]
		newState := !a.Disabled()
		if err := account.SetAccountDisabled(m.db, a.ID, newState); err != nil {
			m.statusMsg = "Toggle failed: " + err.Error()
		} else {
			verb := "enabled"
			if newState {
				verb = "disabled"
			}
			m.statusMsg = fmt.Sprintf("Account #%d %s", a.ID, verb)
		}
		m.statusTime = time.Now()
		m.refreshData()
	} else if m.tab == tabProviders && m.cursor < len(m.providers) {
		p := m.providers[m.cursor]
		newState := !p.Disabled()
		if err := provider.SetProviderDisabled(m.db, p.ID, newState); err != nil {
			m.statusMsg = "Toggle failed: " + err.Error()
		} else {
			verb := "enabled"
			if newState {
				verb = "disabled"
			}
			m.statusMsg = fmt.Sprintf("Provider #%d %s", p.ID, verb)
		}
		m.statusTime = time.Now()
		m.refreshData()
	} else if m.tab == tabKeys && m.cursor < len(m.keys) {
		k := m.keys[m.cursor]
		newState := !k.Disabled
		if err := account.SetAPIKeyDisabled(m.db, k.ID, newState); err != nil {
			m.statusMsg = "Toggle failed: " + err.Error()
		} else {
			verb := "enabled"
			if newState {
				verb = "disabled"
			}
			m.statusMsg = fmt.Sprintf("API key #%d %s", k.ID, verb)
		}
		m.statusTime = time.Now()
		m.refreshData()
	} else if m.tab == tabModels {
		agyCount := len(m.models)
		if m.modelsCur >= agyCount && m.modelsCur < agyCount+len(m.providerModels) {
			pm := m.providerModels[m.modelsCur-agyCount]
			newState := !pm.Disabled
			if err := provider.SetModelMappingDisabled(m.db, pm.ModelID, newState); err != nil {
				m.statusMsg = "Toggle failed: " + err.Error()
			} else {
				verb := "enabled"
				if newState {
					verb = "disabled"
				}
				m.statusMsg = fmt.Sprintf("Model %s %s", pm.ModelID, verb)
			}
			m.statusTime = time.Now()
			m.refreshData()
		}
	}
}

func (m *tuiModel) cycleSchedulingMode() {
	cfg, err := config.Load()
	if err != nil {
		m.statusMsg = err.Error()
		m.statusTime = time.Now()
		return
	}
	switch cfg.Scheduling.Mode {
	case config.SchedulingCache:
		cfg.Scheduling.Mode = config.SchedulingBalance
	case config.SchedulingBalance:
		cfg.Scheduling.Mode = config.SchedulingPerformance
	case config.SchedulingPerformance:
		cfg.Scheduling.Mode = config.SchedulingCache
	}
	modeName := string(cfg.Scheduling.Mode)
	if err := cfg.Save(); err != nil {
		m.statusMsg = "Save failed: " + err.Error()
	} else {
		m.statusMsg = fmt.Sprintf("Scheduling -> %s (restart proxy to apply)", modeName)
	}
	m.statusTime = time.Now()
}

func (m tuiModel) refreshQuota() tea.Msg {
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = config.DefaultPtr()
	}
	threshold := int32(cfg.QuotaProtection.ThresholdPercentage)
	monitored := cfg.QuotaProtection.MonitoredModels
	protectionEnabled := cfg.QuotaProtection.Enabled
	client := proxy.NewHTTPClient(60*time.Second, cfg.Proxy.UpstreamProxy)
	accs, _ := account.ListAccounts(m.db)
	ok, errs := 0, 0
	for _, a := range accs {
		if a.Disabled() {
			continue
		}
		token := a.AccessToken
		if account.NeedsRefresh(a.ExpiresAt) {
			t, _, err := account.RefreshToken(client, a.RefreshToken)
			if err != nil {
				errs++
				continue
			}
			token = t
		}
		fetched, err := account.FetchQuota(client, token, a.ProjectID)
		if err != nil {
			errs++
			continue
		}
		newProtected := a.ProtectedModels
		if protectionEnabled {
			newProtected = account.ComputeProtectedModels(a.ProtectedModels, fetched.ModelPercentages, monitored, threshold)
		}
		if err := account.UpdateQuota(
			m.db, a.ID,
			fetched.JSONBlob, fetched.SummaryBlob,
			fetched.MaxPercentage, fetched.HasMaxPercentage,
			newProtected,
		); err != nil {
			errs++
			continue
		}
		ok++
	}
	return refreshMsg{text: fmt.Sprintf("Quota refreshed: %d ok, %d errors", ok, errs)}
}

func (m *tuiModel) nextTab() {
	m.tab = (m.tab + 1) % 6
	m.cursor = 0
	m.modelsCur = 0
	m.logScroll = 0
}
func (m *tuiModel) prevTab() {
	m.tab = (m.tab + 5) % 6
	m.cursor = 0
	m.modelsCur = 0
	m.logScroll = 0
}
func (m *tuiModel) setTab(t tab) {
	m.tab = t
	m.cursor = 0
	m.modelsCur = 0
	m.logScroll = 0
}

func (m *tuiModel) moveCursor(delta int) {
	switch m.tab {
	case tabAccounts:
		if len(m.accounts) == 0 {
			return
		}
		n := len(m.accounts)
		m.cursor = ((m.cursor+delta)%n + n) % n
	case tabProviders:
		if len(m.providers) == 0 {
			return
		}
		n := len(m.providers)
		m.cursor = ((m.cursor+delta)%n + n) % n
	case tabKeys:
		if len(m.keys) == 0 {
			return
		}
		n := len(m.keys)
		m.cursor = ((m.cursor+delta)%n + n) % n
	case tabModels:
		total := len(m.models) + len(m.providerModels)
		if total == 0 {
			return
		}
		m.modelsCur = ((m.modelsCur+delta)%total + total) % total
	case tabLogs:
		if delta > 0 {
			m.logScroll += delta
		} else {
			m.logScroll -= -delta
		}
		m.clampLogScroll()
	}
}

func (m *tuiModel) cursorTop() {
	switch m.tab {
	case tabAccounts, tabProviders, tabKeys:
		m.cursor = 0
	case tabModels:
		m.modelsCur = 0
	case tabLogs:
		m.logScroll = 0
	}
}

func (m *tuiModel) cursorBottom() {
	switch m.tab {
	case tabAccounts:
		if len(m.accounts) > 0 {
			m.cursor = len(m.accounts) - 1
		}
	case tabProviders:
		if len(m.providers) > 0 {
			m.cursor = len(m.providers) - 1
		}
	case tabKeys:
		if len(m.keys) > 0 {
			m.cursor = len(m.keys) - 1
		}
	case tabModels:
		total := len(m.models) + len(m.providerModels)
		if total > 0 {
			m.modelsCur = total - 1
		}
	case tabLogs:
		m.logScroll = len(m.logs) - m.bodyHeight()
		m.clampLogScroll()
	}
}

func (m *tuiModel) clampLogScroll() {
	if m.logScroll < 0 {
		m.logScroll = 0
	}
	max := len(m.logs) - m.bodyHeight()
	if max < 0 {
		max = 0
	}
	if m.logScroll > max {
		m.logScroll = max
	}
}

func (m tuiModel) bodyHeight() int {
	_, h := m.panelSize()
	return h
}

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	// Full-key overlay takes priority.
	if m.showFullKey != "" {
		return m.renderFullKeyOverlay()
	}
	// Log detail overlay.
	if m.showLogDetail != 0 {
		return m.renderLogDetailOverlay()
	}
	// Input mode overlays.
	switch m.inputMode {
	case inputAddKey:
		return m.renderInputOverlay("Add API Key", "Enter to confirm · Esc to cancel")
	case inputAddProviderName:
		return m.renderInputOverlay("Add Provider (1/3: Name)", "Enter to continue · Esc to cancel")
	case inputAddProviderURL:
		return m.renderInputOverlay("Add Provider (2/3: Base URL)", "Enter to continue · Esc to cancel")
	case inputAddProviderKey:
		return m.renderInputOverlay("Add Provider (3/3: API Key)", "Enter to confirm · Esc to cancel")
	case inputOAuthDevice:
		return m.renderOAuthDeviceOverlay()
	}

	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	b.WriteString(m.renderBody())
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

func (m tuiModel) renderFullKeyOverlay() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	content := panelTitleActive.Render(fmt.Sprintf("API Key #%d (full)", m.showFullKeyID)) + "\n\n" +
		boldStyle.Render(m.showFullKey) + "\n\n" +
		grayStyle.Render("Press any key to dismiss · Copy with your terminal's select")
	b.WriteString(m.renderPanel(content))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(" any key = dismiss"))
	return b.String()
}

// renderInputOverlay renders a generic text input overlay for inline
// TUI operations (add key, add provider steps).
func (m tuiModel) renderInputOverlay(title, hint string) string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	content := panelTitleActive.Render(title) + "\n\n" +
		m.textInput.View() + "\n\n" +
		grayStyle.Render(hint)
	b.WriteString(m.renderPanel(content))
	b.WriteString("\n")
	return b.String()
}

// renderOAuthDeviceOverlay renders the OAuth device flow waiting screen.
func (m tuiModel) renderOAuthDeviceOverlay() string {
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	var content string
	if m.oauthDeviceCode != nil {
		content = panelTitleActive.Render("Add Account (OAuth Device Flow)") + "\n\n" +
			"1. Go to: " + boldStyle.Render(m.oauthDeviceCode.VerificationURL) + "\n" +
			"2. Enter code: " + boldStyle.Render(m.oauthDeviceCode.UserCode) + "\n" +
			"3. Sign in with your Google account and approve\n\n" +
			yellowStyle.Render("Waiting for authorization...") + "\n\n" +
			grayStyle.Render("Esc to cancel")
	} else {
		content = panelTitleActive.Render("Add Account (OAuth Device Flow)") + "\n\n" +
			"Requesting device code..." + "\n\n" +
			grayStyle.Render("Esc to cancel")
	}
	b.WriteString(m.renderPanel(content))
	b.WriteString("\n")
	return b.String()
}

func (m tuiModel) renderLogDetailOverlay() string {
	// Find the log entry by ID.
	var l *account.RequestLog
	for _, log := range m.logs {
		if log.ID == m.showLogDetail {
			l = log
			break
		}
	}
	if l == nil {
		m.showLogDetail = 0
		return m.View()
	}

	// Build account/key context maps.
	accMap := make(map[int64]string)
	for _, a := range m.accounts {
		accMap[a.ID] = a.Email
	}
	keyMap := make(map[int64]string)
	for _, k := range m.keys {
		keyMap[k.ID] = k.Label
	}

	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	var content strings.Builder
	content.WriteString(panelTitleActive.Render(fmt.Sprintf("Log #%d Detail", l.ID)))
	content.WriteString("\n\n")

	// Time and status.
	t := time.Unix(l.Ts, 0).Format("2006-01-02 15:04:05")
	statusColor := greenStyle
	switch {
	case l.Status == 429:
		statusColor = yellowStyle
	case l.Status >= 400:
		statusColor = redStyle
	}
	content.WriteString(fmt.Sprintf("  Time:        %s\n", t))
	content.WriteString(fmt.Sprintf("  Status:      %s\n", statusColor.Render(fmt.Sprintf("%d", l.Status))))

	// Account context.
	accCtx := "-"
	if l.AccountID != nil {
		if email, ok := accMap[*l.AccountID]; ok {
			accCtx = email
		} else {
			accCtx = fmt.Sprintf("#%d", *l.AccountID)
		}
	}
	content.WriteString(fmt.Sprintf("  Account:     %s\n", accCtx))

	// Key context.
	keyCtx := "-"
	if l.APIKeyID != nil {
		if label, ok := keyMap[*l.APIKeyID]; ok {
			keyCtx = label
		} else {
			keyCtx = fmt.Sprintf("#%d", *l.APIKeyID)
		}
	}
	content.WriteString(fmt.Sprintf("  API Key:     %s\n", keyCtx))

	// Model.
	model := "-"
	if l.Model != nil {
		model = *l.Model
	}
	content.WriteString(fmt.Sprintf("  Model:       %s\n", model))

	// Client IP.
	ip := "-"
	if l.ClientIP != nil {
		ip = *l.ClientIP
	}
	content.WriteString(fmt.Sprintf("  Client IP:   %s\n", ip))

	// Token breakdown.
	content.WriteString("\n  Tokens\n")
	prompt := blueStyle.Render(
		fmt.Sprintf("%d", derefOr(l.PromptTokens, 0)))
	compl := magentaStyle.Render(
		fmt.Sprintf("%d", derefOr(l.CompletionTokens, 0)))
	cached := greenStyle.Render(
		fmt.Sprintf("%d", derefOr(l.CachedTokens, 0)))
	think := yellowStyle.Render(
		fmt.Sprintf("%d", derefOr(l.ThoughtTokens, 0)))
	content.WriteString(fmt.Sprintf("    Prompt:     %s\n", prompt))
	content.WriteString(fmt.Sprintf("    Completion: %s\n", compl))
	content.WriteString(fmt.Sprintf("    Cached:     %s\n", cached))
	content.WriteString(fmt.Sprintf("    Thinking:   %s\n", think))

	// Error (full, no truncation).
	if l.Error != nil && *l.Error != "" {
		content.WriteString("\n  Error\n")
		content.WriteString(redStyle.Render(*l.Error))
		content.WriteString("\n")
	}

	content.WriteString("\n" + grayStyle.Render("Press any key to dismiss"))
	b.WriteString(m.renderPanel(content.String()))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(" any key = dismiss"))
	return b.String()
}

// renderPanel wraps content in a fixed-size bordered panel that fills the
// available terminal space. The panel dimensions are calculated from the
// terminal size, not the content size — this keeps the layout stable.
func (m tuiModel) renderPanel(content string) string {
	w, h := m.panelSize()
	if w < 10 || h < 3 {
		return content
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Width(w).
		Height(h).
		Render(content)
}

// panelSize returns the inner content area dimensions (excluding borders).
func (m tuiModel) panelSize() (int, int) {
	if m.width == 0 || m.height == 0 {
		return 80, 20
	}
	// Layout: title(1) + tabs(1) + summary(1) + panel + footer(1 or 2)
	footerLines := 1
	if m.statusMsg != "" {
		footerLines = 2
	}
	// Panel outer height = terminal - header(3) - footer - 1 (newline before footer)
	panelOuterH := m.height - 3 - footerLines - 1
	if panelOuterH < 3 {
		panelOuterH = 3
	}
	// Panel outer width = terminal width
	panelOuterW := m.width
	// Inner content area = outer - 2 (borders)
	return panelOuterW - 2, panelOuterH - 2
}

func (m tuiModel) renderHeader() string {
	var tabs strings.Builder
	for i, name := range tabNames {
		style := inactiveTabStyle
		if tab(i) == m.tab {
			style = activeTabStyle
		}
		if i > 0 {
			tabs.WriteString(" ")
		}
		tabs.WriteString(style.Render(" " + name + " "))
	}

	line2 := fmt.Sprintf("  %s accounts · %s requests · %s tokens · %s cached",
		greenStyle.Render(fmt.Sprintf("%d", m.stats.accountCount)),
		yellowStyle.Render(fmt.Sprintf("%d", m.stats.requestCount)),
		magentaStyle.Render(fmt.Sprintf("%d", m.stats.promptTokens+m.stats.completionTokens)),
		greenStyle.Render(fmt.Sprintf("%d", m.stats.cachedTokens)),
	)

	return titleStyle.Render("hydra") + "\n" +
		tabs.String() + "\n" + line2
}

func (m tuiModel) renderBody() string {
	var content string
	switch m.tab {
	case tabAccounts:
		content = m.renderAccounts()
	case tabProviders:
		content = m.renderProviders()
	case tabLogs:
		content = m.renderLogs()
	case tabModels:
		content = m.renderModels()
	case tabKeys:
		content = m.renderKeys()
	case tabStatus:
		content = m.renderStatus()
	}
	// Truncate content to panel height to prevent overflow.
	_, panelH := m.panelSize()
	content = truncateLines(content, panelH)
	return m.renderPanel(content)
}

// truncateLines limits content to at most maxLines lines, preserving ANSI codes.
func truncateLines(content string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	return strings.Join(lines[:maxLines], "\n")
}

func (m tuiModel) renderAccounts() string {
	if len(m.accounts) == 0 {
		return panelTitleActive.Render("Accounts") + "\n  " +
			grayStyle.Render("No accounts bound. Run `hydra accounts add` to bind one via OAuth.")
	}
	var b strings.Builder
	b.WriteString(panelTitleActive.Render(fmt.Sprintf("Accounts (%d)", len(m.accounts))))
	b.WriteString("\n")
	// Use lipgloss styled Width for columns — %-14s breaks on ANSI-colored strings.
	idW := 4
	emailW := 26
	quotaW := 16
	idStyle := lipgloss.NewStyle().Width(idW)
	emailStyle := lipgloss.NewStyle().Width(emailW)
	quotaStyle := lipgloss.NewStyle().Width(quotaW)
	header := idStyle.Foreground(lipgloss.Color("245")).Render("ID") + " " +
		emailStyle.Foreground(lipgloss.Color("245")).Render("EMAIL") + " " +
		quotaStyle.Foreground(lipgloss.Color("245")).Render("GEM_5H") + " " +
		quotaStyle.Foreground(lipgloss.Color("245")).Render("GEM_WK") + " " +
		quotaStyle.Foreground(lipgloss.Color("245")).Render("EXT_5H") + " " +
		quotaStyle.Foreground(lipgloss.Color("245")).Render("EXT_WK") + " " +
		grayStyle.Render("STATUS")
	b.WriteString(header)
	b.WriteString("\n")
	for i, a := range m.accounts {
		gw := a.QuotaWindowsParsed()
		status := greenStyle.Render("active")
		if a.Disabled() {
			status = redStyle.Render("disabled")
		}
		row := fmt.Sprintf("%-*d", idW, a.ID) + " " +
			emailStyle.Render(a.Email) + " " +
			quotaStyle.Render(formatQuotaWindowStyled(gw.Gemini5h)) + " " +
			quotaStyle.Render(formatQuotaWindowStyled(gw.GeminiWeekly)) + " " +
			quotaStyle.Render(formatQuotaWindowStyled(gw.Other5h)) + " " +
			quotaStyle.Render(formatQuotaWindowStyled(gw.OtherWeekly)) + " " +
			status
		if i == m.cursor {
			b.WriteString(selectedStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func formatQuotaWindowStyled(w *account.QuotaWindow) string {
	if w == nil {
		return grayStyle.Render("-")
	}
	if w.Disabled {
		return grayStyle.Render(fmt.Sprintf("(%d%% off)", w.MaxPercentage))
	}
	s := fmt.Sprintf("%d%% (%s)", w.MaxPercentage, w.ResetIn())
	switch {
	case w.MaxPercentage >= 50:
		return greenStyle.Render(s)
	case w.MaxPercentage >= 20:
		return yellowStyle.Render(s)
	case w.MaxPercentage > 0:
		return redStyle.Render(s)
	}
	return redStyle.Render("0%")
}

func (m tuiModel) renderKeys() string {
	if len(m.keys) == 0 {
		return panelTitleActive.Render("API Keys") + "\n  " +
			grayStyle.Render("No API keys. Press 'a' to create one, or run `hydra key add <label>`.")
	}
	usage, _ := account.UsageByKey(m.db, 0)
	var b strings.Builder
	b.WriteString(panelTitleActive.Render(fmt.Sprintf("API Keys (%d)", len(m.keys))))
	b.WriteString("\n")
	// Column styles for ANSI-safe alignment.
	cID := lipgloss.NewStyle().Width(4)
	cLabel := lipgloss.NewStyle().Width(14)
	cKey := lipgloss.NewStyle().Width(20)
	cStatus := lipgloss.NewStyle().Width(8)
	cReqs := lipgloss.NewStyle().Width(8)
	cTokens := lipgloss.NewStyle().Width(10)
	cCreated := lipgloss.NewStyle().Width(12)
	gh := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	header := cID.Render(gh.Render("ID")) + " " +
		cLabel.Render(gh.Render("LABEL")) + " " +
		cKey.Render(gh.Render("KEY")) + " " +
		cStatus.Render(gh.Render("STATUS")) + " " +
		cReqs.Render(gh.Render("REQS")) + " " +
		cTokens.Render(gh.Render("TOKENS")) + " " +
		cCreated.Render(gh.Render("CREATED"))
	b.WriteString(header)
	b.WriteString("\n")
	for i, k := range m.keys {
		var reqs, tokens int64
		for _, u := range usage {
			if u.KeyID != nil && *u.KeyID == k.ID {
				reqs = u.Requests
				tokens = u.PromptTokens + u.CompletionTokens
				break
			}
		}
		prefix := k.Key
		if len(prefix) >= 8 {
			prefix = prefix[:8] + "…" + prefix[len(prefix)-4:]
		}
		status := greenStyle.Render("active")
		if k.Disabled {
			status = redStyle.Render("disabled")
		}
		created := time.Unix(k.CreatedAt, 0).Format("2006-01-02")
		row := cID.Render(fmt.Sprintf("%d", k.ID)) + " " +
			cLabel.Render(k.Label) + " " +
			cKey.Render(prefix) + " " +
			cStatus.Render(status) + " " +
			cReqs.Render(fmt.Sprintf("%d", reqs)) + " " +
			cTokens.Render(fmt.Sprintf("%d", tokens)) + " " +
			cCreated.Render(created)
		if i == m.cursor {
			b.WriteString(selectedStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderProviders() string {
	if len(m.providers) == 0 {
		return panelTitleActive.Render("Providers") + "\n  " +
			grayStyle.Render("No API key providers. Press 'a' to add one.")
	}
	var b strings.Builder
	b.WriteString(panelTitleActive.Render(fmt.Sprintf("Providers (%d)", len(m.providers))))
	b.WriteString("\n")
	cID := lipgloss.NewStyle().Width(4)
	cName := lipgloss.NewStyle().Width(16)
	cURL := lipgloss.NewStyle().Width(36)
	cKey := lipgloss.NewStyle().Width(16)
	cStatus := lipgloss.NewStyle().Width(10)
	gh := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	header := cID.Render(gh.Render("ID")) + " " +
		cName.Render(gh.Render("NAME")) + " " +
		cURL.Render(gh.Render("BASE_URL")) + " " +
		cKey.Render(gh.Render("API_KEY")) + " " +
		cStatus.Render(gh.Render("STATUS"))
	b.WriteString(header)
	b.WriteString("\n")
	for i, p := range m.providers {
		status := greenStyle.Render("active")
		if p.HealthDisabled {
			status = redStyle.Render("unhealthy")
		} else if p.OperatorDisabled {
			status = yellowStyle.Render("disabled")
		}
		row := cID.Render(fmt.Sprintf("%d", p.ID)) + " " +
			cName.Render(p.Name) + " " +
			cURL.Render(p.BaseURL) + " " +
			cKey.Render(provider.MaskAPIKey(p.APIKey)) + " " +
			cStatus.Render(status)
		if i == m.cursor {
			b.WriteString(selectedStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderLogs() string {
	if len(m.logs) == 0 {
		return panelTitleActive.Render("Recent Logs") + "\n  " +
			grayStyle.Render("No request logs yet.")
	}
	var b strings.Builder
	b.WriteString(panelTitleActive.Render(fmt.Sprintf("Recent Logs (%d)", len(m.logs))))
	b.WriteString("\n")
	// Column styles for ANSI-safe alignment.
	lTime := lipgloss.NewStyle().Width(11)
	lStatus := lipgloss.NewStyle().Width(3)
	lAcc := lipgloss.NewStyle().Width(6)
	lKey := lipgloss.NewStyle().Width(8)
	lModel := lipgloss.NewStyle().Width(22)
	lPrompt := lipgloss.NewStyle().Width(7)
	lCompl := lipgloss.NewStyle().Width(7)
	gh := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	header := lTime.Render(gh.Render("TIME")) + " " +
		lStatus.Render(gh.Render("ST")) + " " +
		lAcc.Render(gh.Render("ACC")) + " " +
		lKey.Render(gh.Render("KEY")) + " " +
		lModel.Render(gh.Render("MODEL")) + " " +
		lPrompt.Render(gh.Render("PROMPT")) + " " +
		lCompl.Render(gh.Render("COMPL"))
	b.WriteString(header)
	b.WriteString("\n")
	cap := m.bodyHeight() - 3 // title + header + scroll indicator
	if cap < 1 {
		cap = 1
	}
	end := m.logScroll + cap
	if end > len(m.logs) {
		end = len(m.logs)
	}
	// Build account ID → email map for context.
	accMap := make(map[int64]string)
	for _, a := range m.accounts {
		accMap[a.ID] = a.Email
	}
	keyMap := make(map[int64]string)
	for _, k := range m.keys {
		keyMap[k.ID] = k.Label
	}
	for i := m.logScroll; i < end; i++ {
		l := m.logs[i]
		t := time.Unix(l.Ts, 0).Format("01-02 15:04:05")
		statusColor := greenStyle
		switch {
		case l.Status == 429:
			statusColor = yellowStyle
		case l.Status >= 400:
			statusColor = redStyle
		}
		model := "-"
		if l.Model != nil {
			model = *l.Model
		}
		modelColor := grayStyle
		switch {
		case strings.HasPrefix(model, "claude-"):
			modelColor = magentaStyle
		case strings.HasPrefix(model, "gemini-"):
			modelColor = blueStyle
		}
		// Account context (abbreviated email).
		accCtx := "-"
		if l.AccountID != nil {
			if email, ok := accMap[*l.AccountID]; ok {
				if at := strings.Index(email, "@"); at > 0 {
					accCtx = email[:at]
				} else {
					accCtx = email
				}
			} else {
				accCtx = fmt.Sprintf("#%d", *l.AccountID)
			}
		}
		if len(accCtx) > 6 {
			accCtx = accCtx[:6]
		}
		// Key context (label).
		keyCtx := "-"
		if l.APIKeyID != nil {
			if label, ok := keyMap[*l.APIKeyID]; ok {
				keyCtx = label
			} else {
				keyCtx = fmt.Sprintf("#%d", *l.APIKeyID)
			}
		}
		if len(keyCtx) > 8 {
			keyCtx = keyCtx[:8]
		}
		line := lTime.Render(t) + " " +
			lStatus.Render(statusColor.Render(fmt.Sprintf("%d", l.Status))) + " " +
			lAcc.Render(accCtx) + " " +
			lKey.Render(keyCtx) + " " +
			lModel.Render(modelColor.Render(model)) + " " +
			lPrompt.Render(fmt.Sprintf("%d", derefOr(l.PromptTokens, 0))) + " " +
			lCompl.Render(fmt.Sprintf("%d", derefOr(l.CompletionTokens, 0)))
		if l.Error != nil && *l.Error != "" {
			line += " " + redStyle.Render("!")
		}
		b.WriteString(line + "\n")
	}
	if len(m.logs) > cap {
		scroll := fmt.Sprintf("  [%d-%d/%d] Ctrl+d/u scroll · Space detail",
			m.logScroll+1, end, len(m.logs))
		b.WriteString(grayStyle.Render(scroll))
	}
	return b.String()
}

func (m tuiModel) renderModels() string {
	agyCount := len(m.models)
	provCount := len(m.providerModels)
	total := agyCount + provCount
	if total == 0 {
		return panelTitleActive.Render("Models") + "\n  " +
			grayStyle.Render("No models available. Run `hydra quota` to fetch from accounts.")
	}
	var b strings.Builder
	b.WriteString(panelTitleActive.Render(fmt.Sprintf("Models (%d)", total)))
	b.WriteString("\n")
	// Column styles.
	mModel := lipgloss.NewStyle().Width(34)
	mSrc := lipgloss.NewStyle().Width(12)
	mReqs := lipgloss.NewStyle().Width(8)
	gh := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	header := mModel.Render(gh.Render("MODEL ID")) + " " +
		mSrc.Render(gh.Render("SOURCE")) + " " +
		mReqs.Render(gh.Render("REQS"))
	b.WriteString(header)
	b.WriteString("\n")

	// AGY models.
	for i, name := range m.models {
		modelColor := grayStyle
		switch {
		case strings.HasPrefix(name, "claude-"):
			modelColor = magentaStyle
		case strings.HasPrefix(name, "gemini-"):
			modelColor = blueStyle
		}
		usage := 0
		if m.stats.modelUsage != nil {
			usage = m.stats.modelUsage[name]
		}
		usageStr := grayStyle.Render("0")
		if usage > 0 {
			usageStr = yellowStyle.Render(fmt.Sprintf("%d", usage))
		}
		row := mModel.Render(modelColor.Render(name)) + " " +
			mSrc.Render(grayStyle.Render("antigravity")) + " " +
			mReqs.Render(usageStr)
		if i == m.modelsCur {
			b.WriteString(selectedStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}

	// Provider models.
	for i, pm := range m.providerModels {
		idx := agyCount + i
		modelColor := greenStyle
		if pm.Disabled {
			modelColor = redStyle
		}
		usage := 0
		if m.stats.modelUsage != nil {
			usage = m.stats.modelUsage[pm.ModelID]
		}
		usageStr := grayStyle.Render("0")
		if usage > 0 {
			usageStr = yellowStyle.Render(fmt.Sprintf("%d", usage))
		}
		srcStr := "provider"
		if pm.Disabled {
			srcStr = "provider✗"
		}
		row := mModel.Render(modelColor.Render(pm.ModelID)) + " " +
			mSrc.Render(grayStyle.Render(srcStr)) + " " +
			mReqs.Render(usageStr)
		if idx == m.modelsCur {
			b.WriteString(selectedStyle.Render(row))
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderStatus() string {
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = config.DefaultPtr()
	}
	modeName := "Balance (sticky+switch)"
	modeColor := greenStyle
	switch cfg.Scheduling.Mode {
	case config.SchedulingCache:
		modeName = "Cache (sticky)"
		modeColor = blueStyle
	case config.SchedulingPerformance:
		modeName = "Performance (P2C random)"
		modeColor = yellowStyle
	}

	// Compute window-scoped totals from aggregated data.
	var wReqs, wPrompt, wCompl, wCached, wThought int64
	for _, r := range m.usageByModel {
		wReqs += r.Requests
		wPrompt += r.PromptTokens
		wCompl += r.CompletionTokens
		wCached += r.CachedTokens
		wThought += r.ThoughtTokens
	}
	wHit := 0.0
	if wPrompt > 0 {
		wHit = float64(wCached) / float64(wPrompt) * 100.0
	}

	var b strings.Builder
	b.WriteString(panelTitleActive.Render("Status"))
	b.WriteString(fmt.Sprintf("  %s  %s",
		grayStyle.Render(fmt.Sprintf("window=%s", m.statusWindow)),
		grayStyle.Render("[w cycle]")))
	b.WriteString("\n\n")
	b.WriteString("  Proxy\n")
	b.WriteString(fmt.Sprintf("    Endpoint:     http://%s:%d\n", cfg.Proxy.Bind, cfg.Proxy.Port))
	b.WriteString(fmt.Sprintf("    API keys:     %s  %s\n",
		greenStyle.Render(fmt.Sprintf("%d", len(m.keys))),
		grayStyle.Render("[manage in Keys tab]")))
	b.WriteString(fmt.Sprintf("    Scheduling:   %s  %s\n", modeColor.Render(modeName), grayStyle.Render("[m cycle]")))
	prot := "off"
	protColor := grayStyle
	if cfg.QuotaProtection.Enabled {
		prot = "on"
		protColor = greenStyle
	}
	thr := grayStyle.Render(
		fmt.Sprintf("(threshold=%d%%)", cfg.QuotaProtection.ThresholdPercentage))
	b.WriteString(fmt.Sprintf("    Protection:   %s %s\n", protColor.Render(prot), thr))
	b.WriteString("\n  Accounts\n")
	active := greenStyle.Render(fmt.Sprintf("%d", m.stats.activeAccounts))
	b.WriteString(fmt.Sprintf("    Active:       %s / %d\n", active, m.stats.accountCount))
	b.WriteString(fmt.Sprintf("    Disabled:     %s\n", redStyle.Render(fmt.Sprintf("%d", m.stats.disabledAccounts))))
	b.WriteString(fmt.Sprintf("\n  Usage (%s)\n", m.statusWindow))
	b.WriteString(fmt.Sprintf("    Requests:     %d\n", wReqs))
	b.WriteString(fmt.Sprintf("    Prompt:       %s\n", blueStyle.Render(fmt.Sprintf("%d", wPrompt))))
	b.WriteString(fmt.Sprintf("    Completion:   %s\n", magentaStyle.Render(fmt.Sprintf("%d", wCompl))))
	cachedS := greenStyle.Render(fmt.Sprintf("%d", wCached))
	hitS := grayStyle.Render(fmt.Sprintf("(%.1f%% hit)", wHit))
	b.WriteString(fmt.Sprintf("    Cached:       %s %s\n", cachedS, hitS))
	b.WriteString(fmt.Sprintf("    Thinking:     %s\n", yellowStyle.Render(fmt.Sprintf("%d", wThought))))

	// Per-model table.
	if len(m.usageByModel) > 0 {
		b.WriteString(fmt.Sprintf("\n  By Model (%s)\n", m.statusWindow))
		b.WriteString(grayStyle.Render(fmt.Sprintf("    %-28s %-6s %-8s %-8s %-8s",
			"MODEL", "REQS", "PROMPT", "COMPL", "CACHED")))
		b.WriteString("\n")
		for _, r := range m.usageByModel {
			modelColor := grayStyle
			switch {
			case strings.HasPrefix(r.Label, "claude-"):
				modelColor = magentaStyle
			case strings.HasPrefix(r.Label, "gemini-"):
				modelColor = blueStyle
			}
			b.WriteString(fmt.Sprintf("    %-28s %-6d %-8d %-8d %-8d\n",
				modelColor.Render(fmt.Sprintf("%-28s", r.Label)),
				r.Requests, r.PromptTokens, r.CompletionTokens, r.CachedTokens))
		}
	}

	// Per-account table.
	if len(m.usageByAccount) > 0 {
		b.WriteString(fmt.Sprintf("\n  By Account (%s)\n", m.statusWindow))
		b.WriteString(grayStyle.Render(fmt.Sprintf("    %-28s %-6s %-8s %-8s",
			"EMAIL", "REQS", "PROMPT", "COMPL")))
		b.WriteString("\n")
		for _, r := range m.usageByAccount {
			b.WriteString(fmt.Sprintf("    %-28s %-6d %-8d %-8d\n",
				r.Label, r.Requests, r.PromptTokens, r.CompletionTokens))
		}
	}

	// Per-key table.
	if len(m.usageByKey) > 0 {
		b.WriteString(fmt.Sprintf("\n  By Key (%s)\n", m.statusWindow))
		b.WriteString(grayStyle.Render(fmt.Sprintf("    %-14s %-6s %-8s %-8s",
			"LABEL", "REQS", "PROMPT", "COMPL")))
		b.WriteString("\n")
		for _, r := range m.usageByKey {
			b.WriteString(fmt.Sprintf("    %-14s %-6d %-8d %-8d\n",
				r.Label, r.Requests, r.PromptTokens, r.CompletionTokens))
		}
	}

	return b.String()
}

// keyHints returns context-specific keybinding hints for the current tab.
func (m tuiModel) keyHints() string {
	common := []struct{ key, desc string }{
		{"Tab", "switch"},
		{"j/k", "navigate"},
		{"q", "quit"},
	}
	var specific []struct{ key, desc string }
	switch m.tab {
	case tabAccounts:
		specific = []struct{ key, desc string }{
			{"a", "add (OAuth)"},
			{"r", "refresh quota"},
			{"D", "delete"},
			{"e", "enable/disable"},
		}
	case tabLogs:
		specific = []struct{ key, desc string }{
			{"Space", "detail"},
			{"Ctrl+d/u", "scroll"},
			{"g/G", "top/bot"},
		}
	case tabProviders:
		specific = []struct{ key, desc string }{
			{"a", "add"},
			{"D", "delete"},
			{"e", "enable/disable"},
		}
	case tabModels:
		specific = []struct{ key, desc string }{
			{"e", "enable/disable (provider)"},
			{"g/G", "top/bot"},
		}
	case tabKeys:
		specific = []struct{ key, desc string }{
			{"a", "add"},
			{"K", "rotate"},
			{"s", "show full"},
			{"D", "delete"},
			{"e", "enable/disable"},
		}
	case tabStatus:
		specific = []struct{ key, desc string }{
			{"r", "refresh quota"},
			{"m", "cycle mode"},
			{"w", "cycle window"},
		}
	}
	all := append(specific, common...)
	var parts []string
	for _, h := range all {
		parts = append(parts, helpKeyStyle.Render(h.key)+" "+helpStyle.Render(h.desc))
	}
	return strings.Join(parts, helpStyle.Render(" · "))
}

func (m tuiModel) renderFooter() string {
	hints := m.keyHints()
	if m.statusMsg != "" {
		return statusBarStyle.Render(" "+m.statusMsg) + "\n" + helpStyle.Render(" "+hints)
	}
	return helpStyle.Render(" " + hints)
}

// RunTUI launches the TUI dashboard.
func RunTUI(d *db.Db, cfg *config.AppConfig) error {
	p := tea.NewProgram(newTUIModel(d, cfg), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

package cli

import (
	"fmt"
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
	"github.com/deungjaho/hydra/internal/proxy"
)

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

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
	panelBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240"))
	panelTitleActive = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("36"))
	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("236"))
	grayStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	greenStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellowStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	redStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	magentaStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	blueStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	boldStyle    = lipgloss.NewStyle().Bold(true)
	helpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	helpKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("36")).Bold(true)
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("238")).
			Foreground(lipgloss.Color("252"))
	inputPromptStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("36"))
)

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type tab int

const (
	tabAccounts tab = iota
	tabLogs
	tabModels
	tabKeys
	tabStatus
)

var tabNames = []string{"Accounts", "Logs", "Models", "Keys", "Status"}

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
	totalCost        float64
	modelUsage       map[string]int
	modelCost        map[string]float64
}

func computeStats(accounts []*account.Account, logs []*account.RequestLog) stats {
	var s stats
	s.accountCount = len(accounts)
	for _, a := range accounts {
		if !a.Disabled {
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
		if l.HasPromptTokens {
			s.promptTokens += l.PromptTokens
		}
		if l.HasCompletion {
			s.completionTokens += l.CompletionTokens
		}
		if l.HasCached {
			s.cachedTokens += l.CachedTokens
		}
		if l.HasThought {
			s.thoughtTokens += l.ThoughtTokens
		}
		if l.HasCost {
			s.totalCost += l.CostUSD
		}
		if l.HasModel {
			if s.modelUsage == nil {
				s.modelUsage = make(map[string]int)
			}
			s.modelUsage[l.Model]++
			if l.HasCost {
				if s.modelCost == nil {
					s.modelCost = make(map[string]float64)
				}
				s.modelCost[l.Model] += l.CostUSD
			}
		}
	}
	return s
}

type refreshMsg struct{ text string }
type tickMsg struct{}

// inputMode tracks inline input state for TUI operations.
type inputMode int

const (
	inputNone  inputMode = iota
	inputAddKey           // typing label for new key
)

type tuiModel struct {
	db         *db.Db
	tab        tab
	cursor     int
	logScroll  int
	modelsCur  int
	keys       []*account.ApiKey
	accounts   []*account.Account
	logs       []*account.RequestLog
	models     []string
	stats      stats
	statusMsg  string
	statusTime time.Time
	width      int
	height     int
	quitting   bool
	// Inline input
	inputMode inputMode
	textInput textinput.Model
	// Key detail view
	showFullKey   string // non-empty = showing full key overlay
	showFullKeyID int64
}

func newTUIModel(d *db.Db) tuiModel {
	m := tuiModel{db: d}
	ti := textinput.New()
	ti.CharLimit = 50
	m.textInput = ti
	m.refreshData()
	return m
}

func (m *tuiModel) refreshData() {
	m.accounts, _ = account.ListAccounts(m.db)
	m.logs, _ = account.RecentLogs(m.db, 500)
	m.keys, _ = account.ListAPIKeys(m.db)
	m.models = proxy.DynamicModelList(m.accounts)
	m.stats = computeStats(m.accounts, m.logs)
}

// ---------------------------------------------------------------------------
// Bubbletea implementation
// ---------------------------------------------------------------------------

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
		if km, ok := msg.(tea.KeyMsg); ok {
			_ = km
			m.showFullKey = ""
			m.showFullKeyID = 0
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
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
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
		case "esc", "ctrl+c":
			m.inputMode = inputNone
			m.textInput.Reset()
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
		m.setTab(tabLogs)
	case "3":
		m.setTab(tabModels)
	case "4":
		m.setTab(tabKeys)
	case "5":
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
			m.statusMsg = "Run `hydra accounts add` in a separate terminal to bind an account."
			m.statusTime = time.Now()
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
		_ = account.RemoveAccount(m.db, a.ID)
		if m.cursor > 0 {
			m.cursor--
		}
		m.statusMsg = fmt.Sprintf("Removed account #%d (%s)", a.ID, a.Email)
		m.statusTime = time.Now()
		m.refreshData()
	} else if m.tab == tabKeys && m.cursor < len(m.keys) {
		k := m.keys[m.cursor]
		_ = account.RemoveAPIKey(m.db, k.ID)
		if m.cursor > 0 {
			m.cursor--
		}
		m.statusMsg = fmt.Sprintf("Removed API key #%d (%s)", k.ID, k.Label)
		m.statusTime = time.Now()
		m.refreshData()
	}
}

func (m *tuiModel) handleToggle() {
	if m.tab == tabAccounts && m.cursor < len(m.accounts) {
		a := m.accounts[m.cursor]
		newState := !a.Disabled
		_ = account.SetAccountDisabled(m.db, a.ID, newState)
		verb := "enabled"
		if newState {
			verb = "disabled"
		}
		m.statusMsg = fmt.Sprintf("Account #%d %s", a.ID, verb)
		m.statusTime = time.Now()
		m.refreshData()
	} else if m.tab == tabKeys && m.cursor < len(m.keys) {
		k := m.keys[m.cursor]
		newState := !k.Disabled
		_ = account.SetAPIKeyDisabled(m.db, k.ID, newState)
		verb := "enabled"
		if newState {
			verb = "disabled"
		}
		m.statusMsg = fmt.Sprintf("API key #%d %s", k.ID, verb)
		m.statusTime = time.Now()
		m.refreshData()
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
		if a.Disabled {
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
		_ = account.UpdateQuota(m.db, a.ID, fetched.JSONBlob, fetched.SummaryBlob, fetched.MaxPercentage, fetched.HasMaxPercentage, newProtected)
		ok++
	}
	return refreshMsg{text: fmt.Sprintf("Quota refreshed: %d ok, %d errors", ok, errs)}
}

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

func (m *tuiModel) nextTab() {
	m.tab = (m.tab + 1) % 5
	m.cursor = 0
	m.modelsCur = 0
	m.logScroll = 0
}
func (m *tuiModel) prevTab() {
	m.tab = (m.tab + 4) % 5
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
		m.cursor = ((m.cursor + delta) % n + n) % n
	case tabKeys:
		if len(m.keys) == 0 {
			return
		}
		n := len(m.keys)
		m.cursor = ((m.cursor + delta) % n + n) % n
	case tabModels:
		if len(m.models) == 0 {
			return
		}
		n := len(m.models)
		m.modelsCur = ((m.modelsCur + delta) % n + n) % n
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
	case tabAccounts, tabKeys:
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
	case tabKeys:
		if len(m.keys) > 0 {
			m.cursor = len(m.keys) - 1
		}
	case tabModels:
		if len(m.models) > 0 {
			m.modelsCur = len(m.models) - 1
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
	if m.height == 0 {
		return 20
	}
	h := m.height - 7
	if h < 5 {
		h = 5
	}
	return h
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	// Full-key overlay takes priority.
	if m.showFullKey != "" {
		return m.renderFullKeyOverlay()
	}
	// Input mode overlay.
	if m.inputMode == inputAddKey {
		var b strings.Builder
		b.WriteString(m.renderHeader())
		b.WriteString("\n")
		b.WriteString(panelBorder.Render(
			panelTitleActive.Render("Add API Key") + "\n\n" +
				m.textInput.View() + "\n\n" +
				grayStyle.Render("Enter to confirm · Esc to cancel")))
		b.WriteString("\n")
		return b.String()
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
	b.WriteString(panelBorder.Render(content))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render(" any key = dismiss"))
	return b.String()
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

	line2 := fmt.Sprintf("  %s accounts · %s requests · %s tokens · %s cached · $%.4f",
		greenStyle.Render(fmt.Sprintf("%d", m.stats.accountCount)),
		yellowStyle.Render(fmt.Sprintf("%d", m.stats.requestCount)),
		magentaStyle.Render(fmt.Sprintf("%d", m.stats.promptTokens+m.stats.completionTokens)),
		greenStyle.Render(fmt.Sprintf("%d", m.stats.cachedTokens)),
		m.stats.totalCost,
	)

	return titleStyle.Render("hydra") + "\n" +
		tabs.String() + "\n" + line2
}

func (m tuiModel) renderBody() string {
	var content string
	switch m.tab {
	case tabAccounts:
		content = m.renderAccounts()
	case tabLogs:
		content = m.renderLogs()
	case tabModels:
		content = m.renderModels()
	case tabKeys:
		content = m.renderKeys()
	case tabStatus:
		content = m.renderStatus()
	}
	return panelBorder.Render(content)
}

func (m tuiModel) renderAccounts() string {
	if len(m.accounts) == 0 {
		return panelTitleActive.Render("Accounts") + "\n  " +
			grayStyle.Render("No accounts bound. Run `hydra accounts add` to bind one via OAuth.")
	}
	var b strings.Builder
	b.WriteString(panelTitleActive.Render(fmt.Sprintf("Accounts (%d)", len(m.accounts))))
	b.WriteString("\n")
	header := fmt.Sprintf("  %-4s %-26s %-14s %-14s %-14s %-14s %s",
		"ID", "EMAIL", "GEM_5H", "GEM_WK", "EXT_5H", "EXT_WK", "STATUS")
	b.WriteString(grayStyle.Render(header))
	b.WriteString("\n")
	for i, a := range m.accounts {
		gw := a.QuotaWindowsParsed()
		status := greenStyle.Render("active")
		if a.Disabled {
			status = redStyle.Render("disabled")
		}
		row := fmt.Sprintf("%-4d %-26s %-14s %-14s %-14s %-14s %s",
			a.ID, a.Email,
			formatQuotaWindowStyled(gw.Gemini5h),
			formatQuotaWindowStyled(gw.GeminiWeekly),
			formatQuotaWindowStyled(gw.Other5h),
			formatQuotaWindowStyled(gw.OtherWeekly),
			status,
		)
		if i == m.cursor {
			b.WriteString(selectedStyle.Render(" " + row))
		} else {
			b.WriteString(" " + row)
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
	header := fmt.Sprintf("  %-4s %-14s %-20s %-8s %-8s %-10s %-10s %-12s",
		"ID", "LABEL", "KEY", "STATUS", "REQS", "TOKENS", "COST", "CREATED")
	b.WriteString(grayStyle.Render(header))
	b.WriteString("\n")
	for i, k := range m.keys {
		var reqs, tokens int64
		var cost float64
		for _, u := range usage {
			if u.HasKeyID && u.KeyID == k.ID {
				reqs = u.Requests
				tokens = u.PromptTokens + u.CompletionTokens
				cost = u.CostUSD
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
		row := fmt.Sprintf("%-4d %-14s %-20s %-8s %-8d %-10d $%.4f  %s",
			k.ID, k.Label, prefix, status, reqs, tokens, cost, created)
		if i == m.cursor {
			b.WriteString(selectedStyle.Render(" " + row))
		} else {
			b.WriteString(" " + row)
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
	// Header row for consistency with other tabs.
	header := fmt.Sprintf("  %-11s %-3s %-4s %-4s %-22s %-7s %-7s %s",
		"TIME", "ST", "ACC", "KEY", "MODEL", "PROMPT", "COMPL", "COST/ERROR")
	b.WriteString(grayStyle.Render(header))
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
		if l.HasModel {
			model = l.Model
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
		if l.HasAccountID {
			if email, ok := accMap[l.AccountID]; ok {
				if at := strings.Index(email, "@"); at > 0 {
					accCtx = email[:at]
				} else {
					accCtx = email
				}
			} else {
				accCtx = fmt.Sprintf("#%d", l.AccountID)
			}
		}
		if len(accCtx) > 4 {
			accCtx = accCtx[:4]
		}
		// Key context (label).
		keyCtx := "-"
		if l.HasAPIKeyID {
			if label, ok := keyMap[l.APIKeyID]; ok {
				keyCtx = label
			} else {
				keyCtx = fmt.Sprintf("#%d", l.APIKeyID)
			}
		}
		if len(keyCtx) > 4 {
			keyCtx = keyCtx[:4]
		}
		cost := "-"
		if l.HasCost {
			cost = fmt.Sprintf("$%.4f", l.CostUSD)
		}
		line := fmt.Sprintf("%-11s %-3s %-4s %-4s %-22s %-7d %-7d %s",
			t, statusColor.Render(fmt.Sprintf("%d", l.Status)),
			accCtx, keyCtx,
			modelColor.Render(fmt.Sprintf("%-22s", model)),
			orInt64(l.HasPromptTokens, l.PromptTokens, 0),
			orInt64(l.HasCompletion, l.CompletionTokens, 0),
			cost,
		)
		if l.HasError && l.Error != "" {
			errMsg := l.Error
			if len(errMsg) > 30 {
				errMsg = errMsg[:30] + "…"
			}
			line += " " + redStyle.Render(errMsg)
		}
		b.WriteString(line + "\n")
	}
	if len(m.logs) > cap {
		b.WriteString(grayStyle.Render(fmt.Sprintf("  [%d-%d/%d] Ctrl+d/u scroll", m.logScroll+1, end, len(m.logs))))
	}
	return b.String()
}

func (m tuiModel) renderModels() string {
	if len(m.models) == 0 {
		return panelTitleActive.Render("Models") + "\n  " +
			grayStyle.Render("No models available. Run `hydra quota` to fetch from accounts.")
	}
	var b strings.Builder
	b.WriteString(panelTitleActive.Render(fmt.Sprintf("Models (%d)", len(m.models))))
	b.WriteString("\n")
	// No # column, no FAMILY column — color coding is sufficient.
	header := fmt.Sprintf("  %-34s %-10s %-12s",
		"MODEL ID", "REQS", "COST")
	b.WriteString(grayStyle.Render(header))
	b.WriteString("\n")
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
			usageStr = yellowStyle.Render(fmt.Sprintf("%-10d", usage))
		}
		costStr := grayStyle.Render("-")
		if m.stats.modelCost != nil {
			if c, ok := m.stats.modelCost[name]; ok && c > 0 {
				costStr = greenStyle.Render(fmt.Sprintf("$%.4f", c))
			}
		}
		row := fmt.Sprintf("%-34s %-10s %-12s",
			modelColor.Render(fmt.Sprintf("%-34s", name)), usageStr, costStr)
		if i == m.modelsCur {
			b.WriteString(selectedStyle.Render(" " + row))
		} else {
			b.WriteString(" " + row)
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

	var b strings.Builder
	b.WriteString(panelTitleActive.Render("Status"))
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
	b.WriteString(fmt.Sprintf("    Protection:   %s %s\n", protColor.Render(prot), grayStyle.Render(fmt.Sprintf("(threshold=%d%%)", cfg.QuotaProtection.ThresholdPercentage))))
	b.WriteString("\n  Accounts\n")
	b.WriteString(fmt.Sprintf("    Active:       %s / %d\n", greenStyle.Render(fmt.Sprintf("%d", m.stats.activeAccounts)), m.stats.accountCount))
	b.WriteString(fmt.Sprintf("    Disabled:     %s\n", redStyle.Render(fmt.Sprintf("%d", m.stats.disabledAccounts))))
	b.WriteString("\n  Requests\n")
	b.WriteString(fmt.Sprintf("    Total:        %d\n", m.stats.requestCount))
	b.WriteString(fmt.Sprintf("    Successful:   %s\n", greenStyle.Render(fmt.Sprintf("%d", m.stats.successCount))))
	b.WriteString(fmt.Sprintf("    Errors:       %s\n", redStyle.Render(fmt.Sprintf("%d", m.stats.errorCount))))
	b.WriteString(fmt.Sprintf("    Rate-limited: %s\n", yellowStyle.Render(fmt.Sprintf("%d", m.stats.rateLimitedCount))))
	b.WriteString("\n  Tokens\n")
	b.WriteString(fmt.Sprintf("    Prompt:       %s\n", blueStyle.Render(fmt.Sprintf("%d", m.stats.promptTokens))))
	b.WriteString(fmt.Sprintf("    Completion:   %s\n", magentaStyle.Render(fmt.Sprintf("%d", m.stats.completionTokens))))
	hit := 0.0
	if m.stats.promptTokens > 0 {
		hit = float64(m.stats.cachedTokens) / float64(m.stats.promptTokens) * 100.0
	}
	b.WriteString(fmt.Sprintf("    Cached:       %s %s\n", greenStyle.Render(fmt.Sprintf("%d", m.stats.cachedTokens)), grayStyle.Render(fmt.Sprintf("(%.1f%% hit)", hit))))
	b.WriteString(fmt.Sprintf("    Thinking:     %s\n", yellowStyle.Render(fmt.Sprintf("%d", m.stats.thoughtTokens))))
	b.WriteString(fmt.Sprintf("    Total:        %s\n", boldStyle.Render(fmt.Sprintf("%d", m.stats.promptTokens+m.stats.completionTokens))))
	b.WriteString("\n  Cost\n")
	b.WriteString(fmt.Sprintf("    Total:        %s\n", greenStyle.Render(fmt.Sprintf("$%.4f", m.stats.totalCost))))
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
			{"r", "refresh quota"},
			{"D", "delete"},
			{"e", "enable/disable"},
		}
	case tabLogs:
		specific = []struct{ key, desc string }{
			{"Ctrl+d/u", "scroll"},
			{"g/G", "top/bot"},
		}
	case tabModels:
		specific = []struct{ key, desc string }{
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
		return statusBarStyle.Render(" " + m.statusMsg) + "\n" + helpStyle.Render(" " + hints)
	}
	return helpStyle.Render(" " + hints)
}

// RunTUI launches the TUI dashboard.
func RunTUI(d *db.Db) error {
	p := tea.NewProgram(newTUIModel(d), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

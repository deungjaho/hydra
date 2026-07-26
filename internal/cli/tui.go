package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
	"github.com/deungjaho/hydra/internal/proxy"
)

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
		}
	}
	return s
}

type refreshMsg struct{ text string }
type tickMsg struct{}

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
}

func newTUIModel(d *db.Db) tuiModel {
	m := tuiModel{db: d}
	m.refreshData()
	return m
}

func (m *tuiModel) refreshData() {
	m.accounts, _ = account.ListAccounts(m.db)
	m.logs, _ = account.RecentLogs(m.db, 200)
	m.keys, _ = account.ListAPIKeys(m.db)
	m.models = proxy.DynamicModelList(m.accounts)
	m.stats = computeStats(m.accounts, m.logs)
}

// Init implements tea.Model.
func (m tuiModel) Init() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update implements tea.Model.
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	case tickMsg:
		m.refreshData()
		// Clear stale status message.
		if m.statusMsg != "" && time.Since(m.statusTime) > 3*time.Second {
			m.statusMsg = ""
		}
		return m, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
	case refreshMsg:
		m.statusMsg = msg.text
		m.statusTime = time.Now()
		return m, nil
	}
	return m, nil
}

func (m tuiModel) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	km := key.NewBinding
	_ = km
	switch k.String() {
	case "q", "esc", "ctrl+c":
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
			m.logScroll += visibleRows() / 2
			m.clampLogScroll()
		}
	case "ctrl+u", "pgup":
		if m.tab == tabLogs {
			m.logScroll -= visibleRows() / 2
			m.clampLogScroll()
		}
	case "r":
		if m.tab == tabAccounts || m.tab == tabStatus {
			return m, tea.Cmd(m.refreshQuota)
		}
	case "a":
		if m.tab == tabAccounts {
			// TUI can't easily run interactive OAuth inline; print a hint.
			m.statusMsg = "Run `hydra accounts add` in a separate terminal to bind an account."
			m.statusTime = time.Now()
		} else if m.tab == tabKeys {
			m.statusMsg = "Run `hydra key add <label>` in a separate terminal to add a key."
			m.statusTime = time.Now()
		}
	case "D":
		if m.tab == tabAccounts && m.cursor < len(m.accounts) {
			a := m.accounts[m.cursor]
			_ = account.RemoveAccount(m.db, a.ID)
			if m.cursor > 0 {
				m.cursor--
			}
			m.statusMsg = fmt.Sprintf("✓ Removed account #%d (%s)", a.ID, a.Email)
			m.statusTime = time.Now()
			m.refreshData()
		} else if m.tab == tabKeys && m.cursor < len(m.keys) {
			k := m.keys[m.cursor]
			_ = account.RemoveAPIKey(m.db, k.ID)
			if m.cursor > 0 {
				m.cursor--
			}
			m.statusMsg = fmt.Sprintf("✓ Removed API key #%d", k.ID)
			m.statusTime = time.Now()
			m.refreshData()
		}
	case "e":
		if m.tab == tabAccounts && m.cursor < len(m.accounts) {
			a := m.accounts[m.cursor]
			newState := !a.Disabled
			_ = account.SetAccountDisabled(m.db, a.ID, newState)
			verb := "enabled"
			if newState {
				verb = "disabled"
			}
			m.statusMsg = fmt.Sprintf("✓ Account #%d %s", a.ID, verb)
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
			m.statusMsg = fmt.Sprintf("✓ API key #%d %s", k.ID, verb)
			m.statusTime = time.Now()
			m.refreshData()
		}
	case "m":
		if m.tab == tabStatus {
			cfg, err := config.Load()
			if err != nil {
				m.statusMsg = "✗ " + err.Error()
				m.statusTime = time.Now()
				return m, nil
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
				m.statusMsg = "✗ Save failed: " + err.Error()
			} else {
				m.statusMsg = fmt.Sprintf("✓ Scheduling mode → %s (restart proxy to apply)", modeName)
			}
			m.statusTime = time.Now()
		}
	case "K":
		if m.tab == tabStatus {
			cfg, err := config.Load()
			if err != nil {
				m.statusMsg = "✗ " + err.Error()
				m.statusTime = time.Now()
				return m, nil
			}
			cfg.Proxy.APIKey = "hydra-" + strings.ReplaceAll(uuid.NewString(), "-", "")
			newKey := cfg.Proxy.APIKey
			if err := cfg.Save(); err != nil {
				m.statusMsg = "✗ Save failed: " + err.Error()
			} else {
				m.statusMsg = fmt.Sprintf("✓ API key rotated: %s (restart proxy to apply)", newKey)
			}
			m.statusTime = time.Now()
		}
	}
	return m, nil
}

// refreshQuota runs a background quota refresh and emits a refreshMsg.
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
		m.logScroll = len(m.logs) - visibleRows()
		m.clampLogScroll()
	}
}

func (m *tuiModel) clampLogScroll() {
	if m.logScroll < 0 {
		m.logScroll = 0
	}
	max := len(m.logs) - visibleRows()
	if max < 0 {
		max = 0
	}
	if m.logScroll > max {
		m.logScroll = max
	}
}

func visibleRows() int { return 40 }

// View implements tea.Model.
func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")
	b.WriteString(m.renderBody())
	b.WriteString("\n")
	b.WriteString(m.renderFooter())
	return b.String()
}

var (
	cyanStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("36")).Bold(true)
	grayStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	greenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	redStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	magentaStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	blueStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	boldStyle   = lipgloss.NewStyle().Bold(true)
)

func (m tuiModel) renderHeader() string {
	var tabs strings.Builder
	for i, name := range tabNames {
		style := grayStyle
		if tab(i) == m.tab {
			style = cyanStyle.Background(lipgloss.Color("51")).Foreground(lipgloss.Color("0"))
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

	return boldStyle.Foreground(lipgloss.Color("36")).Render("hydra") + "\n" +
		tabs.String() + "\n" + line2
}

func (m tuiModel) renderBody() string {
	switch m.tab {
	case tabAccounts:
		return m.renderAccounts()
	case tabLogs:
		return m.renderLogs()
	case tabModels:
		return m.renderModels()
	case tabKeys:
		return m.renderKeys()
	case tabStatus:
		return m.renderStatus()
	}
	return ""
}

func (m tuiModel) renderAccounts() string {
	if len(m.accounts) == 0 {
		return cyanStyle.Render("Accounts [a add · D delete · e enable/disable]") + "\n  " +
			grayStyle.Render("No accounts bound. Run `hydra accounts add` to bind one via OAuth.")
	}
	var b strings.Builder
	b.WriteString(cyanStyle.Render("Accounts [a add · D delete · e enable/disable]"))
	b.WriteString("\n")
	header := fmt.Sprintf("%-4s %-26s %-14s %-14s %-14s %-14s %s",
		"ID", "EMAIL", "GEM_5H", "GEM_WK", "EXT_5H", "EXT_WK", "STATUS")
	b.WriteString(cyanStyle.Render(header))
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
			b.WriteString("> " + row + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
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
		return cyanStyle.Render("API Keys [a add · D delete · e enable/disable]") + "\n  " +
			grayStyle.Render("No API keys. Run `hydra key add <label>` to create one.")
	}
	usage, _ := account.UsageByKey(m.db, 0)
	var b strings.Builder
	b.WriteString(cyanStyle.Render("API Keys [a add · D delete · e enable/disable]"))
	b.WriteString("\n")
	header := fmt.Sprintf("%-4s %-14s %-20s %-8s %-8s %-10s %-10s",
		"ID", "LABEL", "KEY", "STATUS", "REQS", "TOKENS", "COST")
	b.WriteString(cyanStyle.Render(header))
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
		row := fmt.Sprintf("%-4d %-14s %-20s %-8s %-8d %-10d $%.4f",
			k.ID, k.Label, prefix, status, reqs, tokens, cost)
		if i == m.cursor {
			b.WriteString("> " + row + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
	}
	return b.String()
}

func (m tuiModel) renderLogs() string {
	if len(m.logs) == 0 {
		return cyanStyle.Render("Recent Logs") + "\n  " + grayStyle.Render("No request logs yet.")
	}
	var b strings.Builder
	b.WriteString(cyanStyle.Render(fmt.Sprintf("Recent Logs (%d) — j/k scroll, Ctrl+d/u half-page", len(m.logs))))
	b.WriteString("\n")
	cap := visibleRows()
	end := m.logScroll + cap
	if end > len(m.logs) {
		end = len(m.logs)
	}
	for i := m.logScroll; i < end; i++ {
		l := m.logs[i]
		t := time.Unix(l.Ts, 0).UTC().Format("01-02 15:04:05")
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
		cost := "-"
		if l.HasCost {
			cost = fmt.Sprintf("$%.4f", l.CostUSD)
		}
		line := fmt.Sprintf("%-11s %-3s %-22s p:%d c:%d %s %s",
			t, statusColor.Render(fmt.Sprintf("%d", l.Status)), modelColor.Render(fmt.Sprintf("%-22s", model)),
			orInt64(l.HasPromptTokens, l.PromptTokens, 0),
			orInt64(l.HasCompletion, l.CompletionTokens, 0),
			cost,
			"",
		)
		if l.HasError && l.Error != "" {
			line += " " + redStyle.Render(l.Error)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (m tuiModel) renderModels() string {
	if len(m.models) == 0 {
		return cyanStyle.Render("Models") + "\n  " + grayStyle.Render("No models available. Run `hydra quota` to fetch from accounts.")
	}
	var b strings.Builder
	b.WriteString(cyanStyle.Render(fmt.Sprintf("Models (%d)", len(m.models))))
	b.WriteString("\n")
	b.WriteString(cyanStyle.Render(fmt.Sprintf("%-4s %-34s %-10s %-10s", "#", "MODEL ID", "FAMILY", "REQUESTS")))
	b.WriteString("\n")
	for i, name := range m.models {
		family := "other"
		familyColor := grayStyle
		switch {
		case strings.HasPrefix(name, "claude-"):
			family = "claude"
			familyColor = magentaStyle
		case strings.HasPrefix(name, "gemini-"):
			family = "gemini"
			familyColor = blueStyle
		}
		usage := 0
		if m.stats.modelUsage != nil {
			usage = m.stats.modelUsage[name]
		}
		usageStr := grayStyle.Render("0")
		if usage > 0 {
			usageStr = yellowStyle.Render(fmt.Sprintf("%d", usage))
		}
		row := fmt.Sprintf("%-4d %-34s %-10s %-10s",
			i+1, familyColor.Render(name), familyColor.Render(family), usageStr)
		if i == m.modelsCur {
			b.WriteString("> " + row + "\n")
		} else {
			b.WriteString("  " + row + "\n")
		}
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
	keyDisplay := "(none)"
	if cfg.Proxy.APIKey != "" {
		k := cfg.Proxy.APIKey
		if len(k) >= 8 {
			keyDisplay = k[:8] + "…" + k[len(k)-4:]
		}
	}

	var b strings.Builder
	b.WriteString(cyanStyle.Render("Status — overview [m mode · K rotate key]"))
	b.WriteString("\n\n")
	b.WriteString("  Proxy\n")
	b.WriteString(fmt.Sprintf("    Endpoint:     http://%s:%d\n", cfg.Proxy.Bind, cfg.Proxy.Port))
	b.WriteString(fmt.Sprintf("    API key:      %s  %s\n", magentaStyle.Render(keyDisplay), grayStyle.Render("[K rotate]")))
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

	if len(m.accounts) > 0 {
		b.WriteString("\n  Per-account quota\n")
		for _, a := range m.accounts {
			gw := a.QuotaWindowsParsed()
			b.WriteString(fmt.Sprintf("    %-22s g5h:%s gwk:%s\n", a.Email,
				formatQuotaPctStyled(gw.Gemini5h), formatQuotaPctStyled(gw.GeminiWeekly)))
			b.WriteString(fmt.Sprintf("    %-22s e5h:%s ewk:%s",
				"", formatQuotaPctStyled(gw.Other5h), formatQuotaPctStyled(gw.OtherWeekly)))
			if len(a.ProtectedModels) > 0 {
				b.WriteString("  " + yellowStyle.Render(fmt.Sprintf("[protected: %s]", strings.Join(a.ProtectedModels, ","))))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func formatQuotaPctStyled(w *account.QuotaWindow) string {
	if w == nil {
		return grayStyle.Render("-")
	}
	if w.Disabled {
		return grayStyle.Render(fmt.Sprintf("(%d%% off)", w.MaxPercentage))
	}
	s := fmt.Sprintf("%d%%", w.MaxPercentage)
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

func (m tuiModel) renderFooter() string {
	hints := "  Tab/1-5 · j/k↑↓ · g/G top/bot · r refresh · D del · e enable · m mode · K rotate · q quit"
	if m.statusMsg != "" {
		return lipgloss.NewStyle().Background(lipgloss.Color("3")).Foreground(lipgloss.Color("0")).Render(
			" "+m.statusMsg+" | "+hints)
	}
	return grayStyle.Render(hints)
}

// RunTUI launches the TUI dashboard.
func RunTUI(d *db.Db) error {
	p := tea.NewProgram(newTUIModel(d), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

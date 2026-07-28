package proxy

import (
	"fmt"
	"log"
	"sync"
)

// Notifier is the abstraction for delivering health-check alerts.
// Implementations may send to Slack, Discord, webhook, email, etc.
// The default implementation (LogNotifier) just logs to stderr.
type Notifier interface {
	// NotifyAccountUnhealthy is called when an account fails its
	// health probe (token refresh failed or upstream unreachable).
	NotifyAccountUnhealthy(email string, reason string)
	// NotifyAccountRecovered is called when a previously unhealthy
	// account passes its health probe again.
	NotifyAccountRecovered(email string)
	// NotifyAllAccountsDown is called when every account is
	// unhealthy — the proxy cannot serve any request.
	NotifyAllAccountsDown(details []AccountHealth)
}

// AccountHealth is a snapshot of one account's health-check result.
type AccountHealth struct {
	Email    string
	Healthy  bool
	Reason   string
	Disabled bool
}

// LogNotifier is the default no-op notifier that logs to stderr.
type LogNotifier struct {
	mu        sync.Mutex
	unhealthy map[string]bool // tracks state to avoid duplicate notifications
}

// NewLogNotifier creates a LogNotifier.
func NewLogNotifier() *LogNotifier {
	return &LogNotifier{unhealthy: make(map[string]bool)}
}

func (n *LogNotifier) NotifyAccountUnhealthy(email string, reason string) {
	n.mu.Lock()
	wasUnhealthy := n.unhealthy[email]
	n.unhealthy[email] = true
	n.mu.Unlock()
	if wasUnhealthy {
		return // already notified, don't spam
	}
	log.Printf("[NOTIFY] account %s UNHEALTHY: %s", email, reason)
}

func (n *LogNotifier) NotifyAccountRecovered(email string) {
	n.mu.Lock()
	wasUnhealthy := n.unhealthy[email]
	delete(n.unhealthy, email)
	n.mu.Unlock()
	if !wasUnhealthy {
		return // wasn't unhealthy, don't notify
	}
	log.Printf("[NOTIFY] account %s RECOVERED", email)
}

func (n *LogNotifier) NotifyAllAccountsDown(details []AccountHealth) {
	summary := ""
	for _, d := range details {
		summary += fmt.Sprintf("  %s: %s\n", d.Email, d.Reason)
	}
	log.Printf("[NOTIFY] ALL ACCOUNTS DOWN:\n%s", summary)
}

// WebhookNotifier sends notifications to an HTTP endpoint (POST JSON).
// This is a placeholder for future implementation — not wired yet.
// To activate, set [health_check] notify_webhook in config.toml.
//
// type WebhookNotifier struct { ... }

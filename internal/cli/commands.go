package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/deungjaho/hydra/internal/account"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
	"github.com/deungjaho/hydra/internal/proxy"
)

// NewRootCmd builds the cobra root command.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hydra",
		Short: "Hydra — terminal AI proxy gateway for Antigravity accounts",
		// No subcommand → launch the TUI dashboard.
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			return RunTUI(d)
		},
	}

	root.AddCommand(newServeCmd())
	root.AddCommand(newAccountsCmd())
	root.AddCommand(newQuotaCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newUsageCmd())
	root.AddCommand(newKeyCmd())
	root.AddCommand(newConfigCmd())
	return root
}

func newServeCmd() *cobra.Command {
	var port int
	var bind string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the reverse proxy server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("port") {
				cfg.Proxy.Port = port
			}
			if cmd.Flags().Changed("bind") {
				cfg.Proxy.Bind = bind
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			// Auto-create a default API key on first run.
			keys, _ := account.ListAPIKeys(d)
			if len(keys) == 0 {
				defaultKey := "hydra-" + strings.ReplaceAll(uuid.NewString(), "-", "")
				id, err := account.AddAPIKey(d, defaultKey, "default")
				if err != nil {
					return err
				}
				fmt.Printf("Created default API key #%d (label: default)\n  %s\n", id, defaultKey)
				fmt.Println("Use `hydra key add <label>` to create more keys.")
			}
			state := proxy.NewProxyState(d)
			srv := proxy.NewProxyServer(cfg, state)
			ctx, stop := signal.NotifyContext(context.Background(),
				os.Interrupt, syscall.SIGTERM)
			defer stop()
			return srv.Serve(ctx)
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 0, "Override the listen port")
	cmd.Flags().StringVar(&bind, "bind", "", "Override the bind address")
	return cmd
}

func newAccountsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accounts",
		Short: "Manage bound Antigravity accounts",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "add",
		Short: "Bind a new Antigravity account via OAuth",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			client := proxy.NewHTTPClient(60*time.Second, cfg.Proxy.UpstreamProxy)
			result, err := account.RunOAuthFlow(client)
			if err != nil {
				return err
			}
			// Check if account already exists (for user-facing message).
			existing := false
			if accs, _ := account.ListAccounts(d); accs != nil {
				for _, a := range accs {
					if a.Email == result.Email {
						existing = true
						break
					}
				}
			}
			id, err := account.AddAccount(
				d, result.Email,
				result.AccessToken, result.RefreshToken,
				result.ProjectID, result.ExpiresAt,
			)
			if err != nil {
				return err
			}
			fmt.Println()
			if existing {
				fmt.Printf("✓ Account re-authorized: id=%d email=%s\n", id, result.Email)
			} else {
				fmt.Printf("✓ Account bound: id=%d email=%s\n", id, result.Email)
			}
			if result.ProjectID != "" {
				fmt.Printf("  project_id: %s\n", result.ProjectID)
			}
			fetched, err := account.FetchQuota(client, result.AccessToken, result.ProjectID)
			if err != nil {
				fmt.Printf("  ⚠ failed to fetch initial quota: %v\n", err)
				return nil
			}
			var protected []string
			if cfg.QuotaProtection.Enabled {
				protected = account.ComputeProtectedModels(
					nil, fetched.ModelPercentages,
					cfg.QuotaProtection.MonitoredModels,
					int32(cfg.QuotaProtection.ThresholdPercentage))
			}
			_ = account.UpdateQuota(
				d, id, fetched.JSONBlob, fetched.SummaryBlob,
				fetched.MaxPercentage, fetched.HasMaxPercentage,
				protected,
			)
			maxPct := int64(-1)
			if fetched.HasMaxPercentage {
				maxPct = fetched.MaxPercentage
			}
			fmt.Printf("  quota: max=%d%%, models=%d\n", maxPct, len(fetched.ModelPercentages))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all bound accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			accs, err := account.ListAccounts(d)
			if err != nil {
				return err
			}
			if len(accs) == 0 {
				fmt.Println("No accounts bound. Run `hydra accounts add` to bind one.")
				return nil
			}
			fmt.Printf("%-4s %-26s %-14s %-14s %-14s %-14s %s\n",
				"ID", "EMAIL", "GEM_5H", "GEM_WK", "EXT_5H", "EXT_WK", "STATUS")
			fmt.Println(strings.Repeat("-", 105))
			for _, a := range accs {
				gw := a.QuotaWindowsParsed()
				status := "active"
				if a.Disabled {
					status = "disabled"
				}
				fmt.Printf("%-4d %-26s %-14s %-14s %-14s %-14s %s\n",
					a.ID, a.Email,
					formatQuotaWindow(gw.Gemini5h),
					formatQuotaWindow(gw.GeminiWeekly),
					formatQuotaWindow(gw.Other5h),
					formatQuotaWindow(gw.OtherWeekly),
					status)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "remove [id]",
		Short: "Remove a bound account by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			if err := account.RemoveAccount(d, id); err != nil {
				return err
			}
			fmt.Printf("✓ Removed account %d\n", id)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "refresh [id]",
		Short: "Refresh an account's tokens",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			acc, err := account.GetAccount(d, id)
			if err != nil {
				return err
			}
			client := proxy.NewHTTPClient(60*time.Second, cfg.Proxy.UpstreamProxy)
			tok, expiresAt, err := account.RefreshToken(client, acc.RefreshToken)
			if err != nil {
				return err
			}
			if err := account.UpdateTokens(d, id, tok, expiresAt, ""); err != nil {
				return err
			}
			fmt.Printf("✓ Refreshed account %d (%s)\n", id, acc.Email)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "enable [id]",
		Short: "Enable a disabled account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			if err := account.SetAccountDisabled(d, id, false); err != nil {
				return err
			}
			fmt.Printf("✓ Enabled account %d\n", id)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "disable [id]",
		Short: "Disable an account (excludes it from the pool)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			if err := account.SetAccountDisabled(d, id, true); err != nil {
				return err
			}
			fmt.Printf("✓ Disabled account %d\n", id)
			return nil
		},
	})
	return cmd
}

func newQuotaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "quota",
		Short: "Refresh quota for all accounts from Google's API",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			state := proxy.NewProxyState(d)
			srv := proxy.NewProxyServer(cfg, state)
			if err := proxy.RefreshAllQuotas(srv); err != nil {
				return err
			}
			fmt.Println("✓ Quota refreshed for all accounts.")
			return nil
		},
	}
}

func newLogsCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show recent request logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			logs, err := account.RecentLogs(d, limit)
			if err != nil {
				return err
			}
			if len(logs) == 0 {
				fmt.Println("No request logs yet.")
				return nil
			}
			fmt.Printf("%-6s %-20s %-4s %-24s %-8s %-8s %-9s %-15s %s\n",
				"ID", "TIME", "STAT", "MODEL", "PROMPT", "COMPL", "COST", "IP", "ERROR")
			fmt.Println(strings.Repeat("-", 110))
			for _, l := range logs {
				t := time.Unix(l.Ts, 0).UTC().Format("2006-01-02 15:04:05")
				cost := "-"
				if l.HasCost {
					cost = fmt.Sprintf("$%.4f", l.CostUSD)
				}
				model := "-"
				if l.HasModel {
					model = l.Model
				}
				ip := "-"
				if l.HasClientIP {
					ip = l.ClientIP
				}
				errMsg := ""
				if l.HasError {
					errMsg = l.Error
				}
				fmt.Printf("%-6d %-20s %-4d %-24s %-8d %-8d %-9s %-15s %s\n",
					l.ID, t, l.Status, model,
					orInt64(l.HasPromptTokens, l.PromptTokens, 0),
					orInt64(l.HasCompletion, l.CompletionTokens, 0),
					cost, ip, errMsg)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Number of entries to show")
	return cmd
}

func newUsageCmd() *cobra.Command {
	var window string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show usage / cost stats aggregated from request logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			var since int64
			switch window {
			case "day":
				since = time.Now().Unix() - 86400
			case "week":
				since = time.Now().Unix() - 86400*7
			case "month":
				since = time.Now().Unix() - 86400*30
			case "all":
				since = 0
			default:
				return fmt.Errorf("invalid window `%s` (expected day/week/month/all)", window)
			}
			byModel, err := account.UsageByModel(d, since)
			if err != nil {
				return err
			}
			byAccount, err := account.UsageByAccount(d, since)
			if err != nil {
				return err
			}
			var totalCost, totalPrompt, totalCompl, totalCached, totalThought, totalReqs int64
			for _, r := range byModel {
				totalCost += int64(r.CostUSD * 1e6)
				totalPrompt += r.PromptTokens
				totalCompl += r.CompletionTokens
				totalCached += r.CachedTokens
				totalThought += r.ThoughtTokens
				totalReqs += r.Requests
			}
			hitRate := 0.0
			if totalPrompt > 0 {
				hitRate = float64(totalCached) / float64(totalPrompt) * 100.0
			}
			fmt.Printf("Usage over `%s`:\n", window)
			fmt.Printf("  total: %d requests, %d prompt tok, %d completion tok, "+
				"%d cached tok (%.1f%% hit), %d thinking tok, $%.4f\n",
				totalReqs, totalPrompt, totalCompl, totalCached, hitRate,
				totalThought, float64(totalCost)/1e6)
			fmt.Println()
			fmt.Println("By model:")
			fmt.Printf("%-28s %-6s %-10s %-10s %-10s %-10s %-10s\n",
				"MODEL", "REQS", "PROMPT", "COMPL", "CACHED", "THINK", "COST")
			fmt.Println(strings.Repeat("-", 90))
			for _, r := range byModel {
				hit := "-"
				if r.PromptTokens > 0 {
					hit = fmt.Sprintf("%.1f%%", float64(r.CachedTokens)/float64(r.PromptTokens)*100.0)
				}
				fmt.Printf("%-28s %-6d %-10d %-10d %-10s %-10d $%.4f\n",
					r.Label, r.Requests, r.PromptTokens, r.CompletionTokens,
					fmt.Sprintf("%d (%s)", r.CachedTokens, hit), r.ThoughtTokens, r.CostUSD)
			}
			fmt.Println()
			fmt.Println("By account:")
			fmt.Printf("%-28s %-6s %-10s %-10s %-10s %-10s %-10s\n",
				"EMAIL", "REQS", "PROMPT", "COMPL", "CACHED", "THINK", "COST")
			fmt.Println(strings.Repeat("-", 90))
			for _, r := range byAccount {
				hit := "-"
				if r.PromptTokens > 0 {
					hit = fmt.Sprintf("%.1f%%", float64(r.CachedTokens)/float64(r.PromptTokens)*100.0)
				}
				fmt.Printf("%-28s %-6d %-10d %-10d %-10s %-10d $%.4f\n",
					r.Label, r.Requests, r.PromptTokens, r.CompletionTokens,
					fmt.Sprintf("%d (%s)", r.CachedTokens, hit), r.ThoughtTokens, r.CostUSD)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&window, "window", "w", "week", "Time window: day, week, month, or all")
	return cmd
}

func newKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage API keys (add, list, remove, rotate, enable, disable)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default: list keys
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			keys, err := account.ListAPIKeys(d)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				fmt.Println("No API keys. Run `hydra key add <label>` to create one.")
				return nil
			}
			usage, _ := account.UsageByKey(d, 0)
			fmt.Printf("%-4s %-14s %-20s %-8s %-8s %-10s %-10s %-12s\n",
				"ID", "LABEL", "KEY", "STATUS", "REQS", "TOKENS", "COST", "CREATED")
			fmt.Println(strings.Repeat("-", 95))
			for _, k := range keys {
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
				status := "active"
				if k.Disabled {
					status = "disabled"
				}
				created := time.Unix(k.CreatedAt, 0).Format("2006-01-02")
				schedInfo := ""
				if k.SchedulingMode != "" || k.NoSticky {
					parts := []string{}
					if k.SchedulingMode != "" {
						parts = append(parts, "mode="+k.SchedulingMode)
					}
					if k.NoSticky {
						parts = append(parts, "no-sticky")
					}
					schedInfo = "[" + strings.Join(parts, ",") + "]"
				}
				fmt.Printf("%-4d %-14s %-20s %-8s %-8d %-10d $%.4f  %s  %s\n",
					k.ID, k.Label, prefix, status, reqs, tokens, cost, created, schedInfo)
			}
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "add [label]",
		Short: "Add a new API key with an optional label for per-key usage tracking",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			label := args[0]
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			newKey := "hydra-" + strings.ReplaceAll(uuid.NewString(), "-", "")
			id, err := account.AddAPIKey(d, newKey, label)
			if err != nil {
				return err
			}
			fmt.Printf("API key #%d added (label: %s)\n  %s\n", id, label, newKey)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all API keys with usage stats",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			keys, err := account.ListAPIKeys(d)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				fmt.Println("No API keys. Run `hydra key add <label>` to create one.")
				return nil
			}
			usage, _ := account.UsageByKey(d, 0)
			fmt.Printf("%-4s %-14s %-20s %-8s %-8s %-10s %-10s %-12s\n",
				"ID", "LABEL", "KEY", "STATUS", "REQS", "TOKENS", "COST", "CREATED")
			fmt.Println(strings.Repeat("-", 95))
			for _, k := range keys {
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
				status := "active"
				if k.Disabled {
					status = "disabled"
				}
				created := time.Unix(k.CreatedAt, 0).Format("2006-01-02")
				schedInfo := ""
				if k.SchedulingMode != "" || k.NoSticky {
					parts := []string{}
					if k.SchedulingMode != "" {
						parts = append(parts, "mode="+k.SchedulingMode)
					}
					if k.NoSticky {
						parts = append(parts, "no-sticky")
					}
					schedInfo = "[" + strings.Join(parts, ",") + "]"
				}
				fmt.Printf("%-4d %-14s %-20s %-8s %-8d %-10d $%.4f  %s  %s\n",
					k.ID, k.Label, prefix, status, reqs, tokens, cost, created, schedInfo)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "rotate [id]",
		Short: "Generate a new key string for an existing key id (old key immediately invalid)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			newKey := "hydra-" + strings.ReplaceAll(uuid.NewString(), "-", "")
			if err := account.RotateAPIKey(d, id, newKey); err != nil {
				return err
			}
			fmt.Printf("API key #%d rotated\n  %s\n", id, newKey)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show [id]",
		Short: "Print the full key string for a given id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			k, err := account.GetAPIKey(d, id)
			if err != nil {
				return err
			}
			fmt.Println(k.Key)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "remove [id]",
		Short: "Remove an API key by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			if err := account.RemoveAPIKey(d, id); err != nil {
				return err
			}
			fmt.Printf("Removed API key #%d\n", id)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "disable [id]",
		Short: "Disable an API key by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			if err := account.SetAPIKeyDisabled(d, id, true); err != nil {
				return err
			}
			fmt.Printf("Disabled API key #%d\n", id)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "enable [id]",
		Short: "Enable a disabled API key by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()
			if err := account.SetAPIKeyDisabled(d, id, false); err != nil {
				return err
			}
			fmt.Printf("Enabled API key #%d\n", id)
			return nil
		},
	})
	// update [id] --scheduling-mode <mode> --no-sticky
	updateCmd := &cobra.Command{
		Use:   "update [id]",
		Short: "Update per-key scheduling override (scheduling-mode, no-sticky)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			d, err := db.Open(config.DBPath())
			if err != nil {
				return err
			}
			defer d.Close()

			schedMode, _ := cmd.Flags().GetString("scheduling-mode")
			noSticky, _ := cmd.Flags().GetBool("no-sticky")
			clearSched, _ := cmd.Flags().GetBool("clear-scheduling")
			clearNoSticky, _ := cmd.Flags().GetBool("clear-no-sticky")

			if clearSched {
				schedMode = ""
			}
			// If --clear-no-sticky is set, we need to write false explicitly.
			if clearNoSticky {
				noSticky = false
			}

			// Validate scheduling mode.
			switch schedMode {
			case "", "cache", "balance", "performance":
			default:
				return fmt.Errorf("invalid scheduling-mode %q: must be cache, balance, or performance", schedMode)
			}

			if err := account.UpdateAPIKeyScheduling(d, id, schedMode, noSticky); err != nil {
				return err
			}

			k, err := account.GetAPIKey(d, id)
			if err != nil {
				return err
			}
			fmt.Printf("API key #%d updated:\n", id)
			if k.SchedulingMode == "" {
				fmt.Printf("  scheduling-mode: (follow global)\n")
			} else {
				fmt.Printf("  scheduling-mode: %s\n", k.SchedulingMode)
			}
			fmt.Printf("  no-sticky: %v\n", k.NoSticky)
			return nil
		},
	}
	updateCmd.Flags().String("scheduling-mode", "", "Override scheduling mode: cache, balance, or performance (empty = follow global)")
	updateCmd.Flags().Bool("no-sticky", false, "Skip sticky session binding for this key's requests")
	updateCmd.Flags().Bool("clear-scheduling", false, "Clear per-key scheduling-mode override (revert to global)")
	updateCmd.Flags().Bool("clear-no-sticky", false, "Clear per-key no-sticky override")
	cmd.AddCommand(updateCmd)
	return cmd
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or reset the config file",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Write the default config to disk (overwrites if present)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Default()
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("✓ Config written to %s\n", config.ConfigPath())
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "paths",
		Short: "Print the paths hydra uses for config/db",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("config: %s\n", config.ConfigPath())
			fmt.Printf("db:     %s\n", config.DBPath())
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the current config as TOML",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			var buf strings.Builder
			enc := toml.NewEncoder(&buf)
			if err := enc.Encode(cfg); err != nil {
				return err
			}
			fmt.Print(buf.String())
			return nil
		},
	})
	return cmd
}

// --- helpers ---

func parseID(s string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(s, "%d", &id)
	if err != nil {
		return 0, fmt.Errorf("invalid id: %s", s)
	}
	return id, nil
}

func formatQuotaWindow(w *account.QuotaWindow) string {
	if w == nil {
		return "-"
	}
	if w.Disabled {
		return fmt.Sprintf("(%d%% off)", w.MaxPercentage)
	}
	return fmt.Sprintf("%d%% (%s)", w.MaxPercentage, w.ResetIn())
}

func orInt64(has bool, v, def int64) int64 {
	if !has {
		return def
	}
	return v
}

// Run is the entry point used by main.go.
func Run() int {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

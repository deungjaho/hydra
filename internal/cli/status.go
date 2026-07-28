package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/deungjaho/hydra/internal/app"
	"github.com/deungjaho/hydra/internal/cli/output"
	"github.com/deungjaho/hydra/internal/config"
	"github.com/deungjaho/hydra/internal/db"
)

// newService creates an app.Service from the global flags + DB.
func newService(cmd *cobra.Command) (*app.Service, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, &app.AppError{
			Code:    app.CodeConfig,
			Message: fmt.Sprintf("config load failed: %v", err),
			Cause:   err,
		}
	}
	dbPath := config.DBPath()
	d, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, &app.AppError{
			Code:    app.CodeUnavailable,
			Message: fmt.Sprintf("database open failed: %v", err),
			Cause:   err,
		}
	}
	cfgPath := config.ConfigPath()
	svc := app.NewService(d, cfg,
		app.WithConfigPath(cfgPath),
		app.WithDBPath(dbPath),
		app.WithVersion(Version),
		app.WithCommit(Commit),
	)
	return svc, func() { d.Close() }, nil
}

// getRenderer extracts the output renderer from command flags.
func getRenderer(cmd *cobra.Command) *output.Renderer {
	formatStr, _ := cmd.Flags().GetString("output")
	noColor, _ := cmd.Flags().GetBool("no-color")
	f := output.FormatTable
	if formatStr == "json" {
		f = output.FormatJSON
	}
	return output.NewRenderer(f, output.WithNoColor(noColor))
}

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show high-level service status",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, cleanup, err := newService(cmd)
			if err != nil {
				return err
			}
			defer cleanup()
			r := getRenderer(cmd)
			status, err := svc.Status(cmd.Context())
			if err != nil {
				ae := app.AsAppError(err)
				_ = r.WriteError(string(ae.Code), ae.Message, ae.Retryable, ae.Details)
				return ae
			}
			return r.WriteSuccess(status, func(w io.Writer) error {
				fmt.Fprintf(w, "Hydra %s\n", status.Version)
				fmt.Fprintf(w, "Config: %s\n", orDash(status.ConfigPath))
				fmt.Fprintf(w, "DB:     %s\n", orDash(status.DBPath))
				fmt.Fprintf(w, "Accounts: %d total, %d active, %d disabled\n",
					status.Accounts.Total, status.Accounts.Active, status.Accounts.Disabled)
				fmt.Fprintf(w, "Keys:     %d total, %d active, %d disabled\n",
					status.Keys.Total, status.Keys.Active, status.Keys.Disabled)
				return nil
			})
		},
	}
	return cmd
}

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostic checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, cleanup, err := newService(cmd)
			if err != nil {
				return err
			}
			defer cleanup()
			r := getRenderer(cmd)

			var checks []app.DoctorCheck
			// Check 1: DB readable.
			_, derr := svc.Accounts.ListAccounts()
			if derr != nil {
				checks = append(checks, app.DoctorCheck{Name: "database", Status: "fail", Detail: derr.Error()})
			} else {
				checks = append(checks, app.DoctorCheck{Name: "database", Status: "ok"})
			}
			// Check 2: config loadable.
			if svc.Config == nil {
				checks = append(checks, app.DoctorCheck{Name: "config", Status: "fail", Detail: "config is nil"})
			} else {
				checks = append(checks, app.DoctorCheck{Name: "config", Status: "ok"})
			}
			// Check 3: config file exists.
			cfgPath := config.ConfigPath()
			if _, err := os.Stat(cfgPath); err != nil {
				checks = append(checks, app.DoctorCheck{Name: "config_file", Status: "warn", Detail: fmt.Sprintf("not found at %s (using defaults)", cfgPath)})
			} else {
				checks = append(checks, app.DoctorCheck{Name: "config_file", Status: "ok"})
			}
			// Check 4: DB file permissions.
			dbPath := config.DBPath()
			if info, err := os.Stat(dbPath); err == nil {
				if info.Mode().Perm() != 0o600 {
					checks = append(checks, app.DoctorCheck{Name: "db_permissions", Status: "warn", Detail: fmt.Sprintf("mode %o, expected 0600", info.Mode().Perm())})
				} else {
					checks = append(checks, app.DoctorCheck{Name: "db_permissions", Status: "ok"})
				}
			}
			// Check 5: WAL/SHM permissions.
			for _, suffix := range []string{"-wal", "-shm"} {
				sidecar := dbPath + suffix
				if info, err := os.Stat(sidecar); err == nil {
					if info.Mode().Perm() != 0o600 {
						checks = append(checks, app.DoctorCheck{Name: "db_" + suffix[1:] + "_permissions", Status: "warn", Detail: fmt.Sprintf("mode %o, expected 0600", info.Mode().Perm())})
					}
				}
			}

			ok := true
			for _, c := range checks {
				if c.Status == "fail" {
					ok = false
				}
			}
			result := app.DoctorView{Checks: checks, OK: ok}
			return r.WriteSuccess(result, func(w io.Writer) error {
				for _, c := range checks {
					fmt.Fprintf(w, "  [%s] %s", c.Status, c.Name)
					if c.Detail != "" {
						fmt.Fprintf(w, " — %s", c.Detail)
					}
					fmt.Fprintln(w)
				}
				if ok {
					fmt.Fprintln(w, "\nAll critical checks passed.")
				} else {
					fmt.Fprintln(w, "\nSome checks failed.")
				}
				return nil
			})
		},
	}
	return cmd
}

func newVersionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := getRenderer(cmd)
			v := app.VersionView{Version: Version, Commit: Commit}
			return r.WriteSuccess(v, func(w io.Writer) error {
				fmt.Fprintf(w, "hydra %s", v.Version)
				if v.Commit != "" {
					fmt.Fprintf(w, " (commit: %s)", v.Commit)
				}
				fmt.Fprintln(w)
				return nil
			})
		},
	}
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

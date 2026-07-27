package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	backendApp "cpa-helper/backend/internal/app"
	"cpa-helper/backend/internal/httpserver"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		log.Fatalf("%v", err)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	command := "start"
	if len(args) > 0 {
		command = strings.TrimSpace(args[0])
	}
	switch command {
	case "", "start":
		if _, err := backendAddr(); err != nil {
			return err
		}
		report, err := backendApp.Migrate(ctx)
		if err != nil {
			return fmt.Errorf("migrate before start: %w", err)
		}
		log.Printf("migration check completed: db_version=%d target_version=%d", report.CurrentVersion, report.TargetVersion)
		return serve(ctx)
	case "serve":
		return serve(ctx)
	case "migrate":
		report, err := backendApp.Migrate(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "migration completed: db=%s previous_version=%d current_version=%d target_version=%d\n", report.DBPath, report.PreviousVersion, report.CurrentVersion, report.TargetVersion)
		return nil
	case "doctor":
		report, err := backendApp.CheckStartup(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "ready: db=%s current_version=%d target_version=%d\n", report.DBPath, report.CurrentVersion, report.TargetVersion)
		return nil
	case "repair-usage-tokens":
		return runRepairUsageTokens(ctx, args[1:], stdout)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stdout)
		return fmt.Errorf("unknown command %q", command)
	}
}

func runRepairUsageTokens(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("repair-usage-tokens requires audit or apply")
	}
	switch args[0] {
	case "audit":
		flags := flag.NewFlagSet("repair-usage-tokens audit", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		reportPath := flags.String("report", "", "write the redacted audit report to this path")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected audit argument %q", flags.Arg(0))
		}
		audit, err := backendApp.AuditUsageTokens(ctx)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(audit, "", "  ")
		if err != nil {
			return err
		}
		if *reportPath != "" {
			if err := writeUsageTokenAuditReport(*reportPath, audit.DatabasePath, append(encoded, '\n')); err != nil {
				return fmt.Errorf("write usage token audit report: %w", err)
			}
		}
		_, err = fmt.Fprintln(stdout, string(encoded))
		return err
	case "apply":
		flags := flag.NewFlagSet("repair-usage-tokens apply", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		auditPath := flags.String("audit-report", "", "path to an audit report from this database")
		backupPath := flags.String("backup", "", "new path where a consistent SQLite backup will be created")
		writersPaused := flags.Bool("confirm-writers-paused", false, "acknowledge that database writers are paused")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected apply argument %q", flags.Arg(0))
		}
		if *auditPath == "" {
			return errors.New("repair-usage-tokens apply requires --audit-report")
		}
		auditFile, err := os.Open(*auditPath)
		if err != nil {
			return fmt.Errorf("open usage token audit report: %w", err)
		}
		var audit backendApp.UsageTokenRepairAudit
		decoder := json.NewDecoder(auditFile)
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&audit)
		if decodeErr == nil {
			var trailing any
			if trailingErr := decoder.Decode(&trailing); !errors.Is(trailingErr, io.EOF) {
				if trailingErr == nil {
					trailingErr = errors.New("multiple JSON values")
				}
				decodeErr = trailingErr
			}
		}
		closeErr := auditFile.Close()
		if decodeErr != nil {
			return fmt.Errorf("decode usage token audit report: %w", decodeErr)
		}
		if closeErr != nil {
			return closeErr
		}
		result, err := backendApp.ApplyUsageTokenRepair(ctx, audit, *backupPath, *writersPaused)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(result)
	default:
		return fmt.Errorf("unknown repair-usage-tokens mode %q", args[0])
	}
}

func writeUsageTokenAuditReport(reportPath, databasePath string, contents []byte) error {
	reportAbsolute, err := filepath.Abs(reportPath)
	if err != nil {
		return err
	}
	databaseAbsolute, err := filepath.Abs(databasePath)
	if err != nil {
		return err
	}
	if usageTokenAuditPathIsProtected(reportAbsolute, databaseAbsolute) {
		return errors.New("audit report path must not overwrite the live SQLite database or its sidecar files")
	}

	reportInfo, err := os.Lstat(reportAbsolute)
	reportExists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect usage token audit report path: %w", err)
	}
	if reportExists && reportInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("audit report path must not overwrite a symbolic link target")
	}

	resolvedDatabase, err := filepath.EvalSymlinks(databaseAbsolute)
	if err != nil {
		return fmt.Errorf("resolve live usage database path: %w", err)
	}
	var resolvedReport string
	if reportExists {
		resolvedReport, err = filepath.EvalSymlinks(reportAbsolute)
		if err != nil {
			return fmt.Errorf("resolve usage token audit report path: %w", err)
		}
	} else {
		resolvedParent, resolveErr := filepath.EvalSymlinks(filepath.Dir(reportAbsolute))
		if resolveErr != nil {
			return fmt.Errorf("resolve usage token audit report directory: %w", resolveErr)
		}
		resolvedReport = filepath.Join(resolvedParent, filepath.Base(reportAbsolute))
	}
	if usageTokenAuditPathIsProtected(resolvedReport, resolvedDatabase) {
		return errors.New("audit report path must not overwrite the live SQLite database or its sidecar files")
	}

	if reportExists {
		reportTargetInfo, statErr := os.Stat(reportAbsolute)
		if statErr != nil {
			return fmt.Errorf("inspect usage token audit report target: %w", statErr)
		}
		for _, protectedPath := range append(usageTokenSQLitePaths(databaseAbsolute), usageTokenSQLitePaths(resolvedDatabase)...) {
			protectedInfo, protectedErr := os.Stat(protectedPath)
			if errors.Is(protectedErr, os.ErrNotExist) {
				continue
			}
			if protectedErr != nil {
				return fmt.Errorf("inspect protected SQLite path: %w", protectedErr)
			}
			if os.SameFile(reportTargetInfo, protectedInfo) {
				return errors.New("audit report path must not overwrite the live SQLite database or its sidecar files")
			}
		}
	}
	return os.WriteFile(reportAbsolute, contents, 0o600)
}

func usageTokenAuditPathIsProtected(candidatePath, databasePath string) bool {
	for _, protectedPath := range usageTokenSQLitePaths(databasePath) {
		if strings.EqualFold(filepath.Clean(candidatePath), filepath.Clean(protectedPath)) {
			return true
		}
	}
	return false
}

func usageTokenSQLitePaths(databasePath string) []string {
	return []string{databasePath, databasePath + "-wal", databasePath + "-shm", databasePath + "-journal"}
}

func serve(ctx context.Context) error {
	addr, err := backendAddr()
	if err != nil {
		return err
	}
	report, err := backendApp.CheckStartup(ctx)
	if err != nil {
		return fmt.Errorf("startup check failed: %w", err)
	}
	log.Printf("startup check passed: db_version=%d target_version=%d", report.CurrentVersion, report.TargetVersion)

	app, err := backendApp.NewWithOptions(ctx, backendApp.NewOptions{
		RequireReady:    true,
		StartBackground: true,
	})
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer app.Close()

	if err := httpserver.Run(ctx, httpserver.Config{
		Addr:    addr,
		Handler: app.Routes(),
	}); err != nil {
		return fmt.Errorf("server failed: %w", err)
	}
	return nil
}

func backendAddr() (string, error) {
	addr := strings.TrimSpace(os.Getenv("CPA_HELPER_ADDR"))
	if addr == "" {
		addr = ":18317"
	}
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid CPA_HELPER_ADDR %q: use host:port or :port", addr)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid CPA_HELPER_ADDR %q: port must be between 1 and 65535", addr)
	}
	return addr, nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  cpa-helper            Run migrations, then start the service
  cpa-helper start      Run migrations, then start the service
  cpa-helper migrate    Run database migrations and exit
  cpa-helper serve      Start only after read-only startup checks pass
  cpa-helper doctor     Run read-only startup checks and exit
  cpa-helper repair-usage-tokens audit [--report <path>]
  cpa-helper repair-usage-tokens apply --audit-report <path> --backup <new-path> --confirm-writers-paused
`)
}

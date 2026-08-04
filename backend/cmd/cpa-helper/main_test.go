package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunHelpListsOperationalSubcommands(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, &output); err != nil {
		t.Fatalf("run help failed: %v", err)
	}
	text := output.String()
	for _, want := range []string{"migrate", "serve", "doctor", "repair-usage-tokens"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help output missing %q: %s", want, text)
		}
	}
	if !strings.Contains(text, "--backup <new-path>") {
		t.Fatalf("help output does not describe backup as a new destination: %s", text)
	}
}

func TestServiceOptionsSeparateUsageMaintenance(t *testing.T) {
	for _, testCase := range []struct {
		name                  string
		startUsageMaintenance bool
	}{
		{name: "serve", startUsageMaintenance: false},
		{name: "start", startUsageMaintenance: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			options := serviceOptions(testCase.startUsageMaintenance)
			if !options.RequireReady || !options.StartBackground {
				t.Fatalf("service options = %#v, want readiness and generic background runners", options)
			}
			if options.StartUsageMaintenance != testCase.startUsageMaintenance {
				t.Fatalf("service usage maintenance = %t, want %t", options.StartUsageMaintenance, testCase.startUsageMaintenance)
			}
		})
	}
}

func TestRunRepairUsageTokensRequiresModeAndAuditReport(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"repair-usage-tokens"}, &output); err == nil || !strings.Contains(err.Error(), "requires audit or apply") {
		t.Fatalf("missing repair mode error = %v", err)
	}
	if err := run(context.Background(), []string{"repair-usage-tokens", "apply"}, &output); err == nil || !strings.Contains(err.Error(), "--audit-report") {
		t.Fatalf("missing audit report error = %v", err)
	}
}

func TestRunRepairUsageTokensRejectsUnknownAuditReportFields(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "audit.json")
	if err := os.WriteFile(reportPath, []byte(`{"version":2,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := run(context.Background(), []string{
		"repair-usage-tokens", "apply",
		"--audit-report", reportPath,
		"--backup", filepath.Join(t.TempDir(), "backup.sqlite3"),
		"--confirm-writers-paused",
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown audit field error = %v", err)
	}
}

func TestWriteUsageTokenAuditReportRejectsLiveSQLitePaths(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "live.sqlite3")
	if err := os.WriteFile(databasePath, []byte("live database"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, reportPath := range []string{databasePath, databasePath + "-wal", databasePath + "-shm", databasePath + "-journal"} {
		if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("report")); err == nil || !strings.Contains(err.Error(), "must not overwrite") {
			t.Fatalf("write report to %q error = %v", reportPath, err)
		}
	}
	contents, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "live database" {
		t.Fatalf("live database was overwritten: %q", contents)
	}
}

func TestWriteUsageTokenAuditReportRejectsAliasedSQLiteDirectory(t *testing.T) {
	databaseDirectory := t.TempDir()
	databasePath := filepath.Join(databaseDirectory, "live.sqlite3")
	if err := os.WriteFile(databasePath, []byte("live database"), 0o600); err != nil {
		t.Fatal(err)
	}
	aliasPath := filepath.Join(t.TempDir(), "live-alias")
	requireTestSymlink(t, databaseDirectory, aliasPath)

	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		reportPath := filepath.Join(aliasPath, filepath.Base(databasePath)+suffix)
		if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("report")); err == nil || !strings.Contains(err.Error(), "must not overwrite") {
			t.Fatalf("write report through aliased directory to %q error = %v", reportPath, err)
		}
	}

	contents, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "live database" {
		t.Fatalf("live database was overwritten through aliased directory: %q", contents)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(databasePath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("protected sidecar %q was created: %v", databasePath+suffix, err)
		}
	}
}

func TestWriteUsageTokenAuditReportRejectsExistingSymlinkTargets(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "live.sqlite3")
	if err := os.WriteFile(databasePath, []byte("live database"), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecarPath := databasePath + "-wal"
	if err := os.WriteFile(sidecarPath, []byte("live wal"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name     string
		target   string
		contents string
	}{
		{name: "database", target: databasePath, contents: "live database"},
		{name: "sidecar", target: sidecarPath, contents: "live wal"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reportPath := filepath.Join(t.TempDir(), "audit.json")
			requireTestSymlink(t, testCase.target, reportPath)
			if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("report")); err == nil || !strings.Contains(err.Error(), "must not overwrite") {
				t.Fatalf("write report through symlink to %q error = %v", testCase.target, err)
			}
			contents, err := os.ReadFile(testCase.target)
			if err != nil {
				t.Fatal(err)
			}
			if string(contents) != testCase.contents {
				t.Fatalf("protected target %q was overwritten: %q", testCase.target, contents)
			}
		})
	}
}

func TestWriteUsageTokenAuditReportRejectsSymlinkToOrdinaryFile(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "live.sqlite3")
	if err := os.WriteFile(databasePath, []byte("live database"), 0o600); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(directory, "ordinary.txt")
	if err := os.WriteFile(targetPath, []byte("ordinary contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(t.TempDir(), "audit.json")
	requireTestSymlink(t, targetPath, reportPath)

	if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("report")); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("write report through symlink to ordinary file error = %v", err)
	}
	contents, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "ordinary contents" {
		t.Fatalf("ordinary symlink target was overwritten: %q", contents)
	}
	if info, err := os.Lstat(reportPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("report symlink was replaced: info=%v err=%v", info, err)
	}
}

func TestWriteUsageTokenAuditReportRejectsExistingHardlinkTargets(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "live.sqlite3")
	if err := os.WriteFile(databasePath, []byte("live database"), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecarPath := databasePath + "-wal"
	if err := os.WriteFile(sidecarPath, []byte("live wal"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name     string
		target   string
		contents string
	}{
		{name: "database", target: databasePath, contents: "live database"},
		{name: "sidecar", target: sidecarPath, contents: "live wal"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reportPath := filepath.Join(t.TempDir(), "audit.json")
			if err := os.Link(testCase.target, reportPath); err != nil {
				t.Fatalf("create hardlink %q -> %q: %v", reportPath, testCase.target, err)
			}
			if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("report")); err == nil || !strings.Contains(err.Error(), "must not overwrite") {
				t.Fatalf("write report through hardlink to %q error = %v", testCase.target, err)
			}
			contents, err := os.ReadFile(testCase.target)
			if err != nil {
				t.Fatal(err)
			}
			if string(contents) != testCase.contents {
				t.Fatalf("protected target %q was overwritten: %q", testCase.target, contents)
			}
		})
	}
}

func TestWriteUsageTokenAuditReportRejectsDanglingSymlink(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "live.sqlite3")
	if err := os.WriteFile(databasePath, []byte("live database"), 0o600); err != nil {
		t.Fatal(err)
	}
	danglingTarget := databasePath + "-shm"
	reportPath := filepath.Join(t.TempDir(), "audit.json")
	requireTestSymlink(t, danglingTarget, reportPath)

	if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("report")); err == nil {
		t.Fatal("write report through dangling symlink unexpectedly succeeded")
	}
	if _, err := os.Lstat(danglingTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling symlink target was created: %v", err)
	}
	if info, err := os.Lstat(reportPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("dangling report symlink was replaced: info=%v err=%v", info, err)
	}
}

func TestWriteUsageTokenAuditReportRejectsUnresolvableParent(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "live.sqlite3")
	if err := os.WriteFile(databasePath, []byte("live database"), 0o600); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(directory, "missing", "audit.json")

	if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("report")); err == nil || !strings.Contains(err.Error(), "resolve usage token audit report directory") {
		t.Fatalf("write report below unresolvable parent error = %v", err)
	}
	if _, err := os.Lstat(reportPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("report was created below unresolvable parent: %v", err)
	}
}

func TestWriteUsageTokenAuditReportRejectsCaseVariantSQLitePaths(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "live.sqlite3")
	if err := os.WriteFile(databasePath, []byte("live database"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, reportName := range []string{"LIVE.SQLITE3", "LIVE.SQLITE3-WAL", "LIVE.SQLITE3-SHM", "LIVE.SQLITE3-JOURNAL"} {
		reportPath := filepath.Join(directory, reportName)
		if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("report")); err == nil || !strings.Contains(err.Error(), "must not overwrite") {
			t.Fatalf("write report to case-variant SQLite path %q error = %v", reportPath, err)
		}
	}
}

func TestWriteUsageTokenAuditReportWritesNormalPath(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "live.sqlite3")
	if err := os.WriteFile(databasePath, []byte("live database"), 0o600); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(directory, "audit.json")

	if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("first report")); err != nil {
		t.Fatalf("write new audit report: %v", err)
	}
	if err := writeUsageTokenAuditReport(reportPath, databasePath, []byte("updated report")); err != nil {
		t.Fatalf("overwrite existing regular audit report: %v", err)
	}
	contents, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "updated report" {
		t.Fatalf("audit report contents = %q", contents)
	}
}

func requireTestSymlink(t *testing.T, targetPath, linkPath string) {
	t.Helper()
	if err := os.Symlink(targetPath, linkPath); err != nil {
		if errors.Is(err, os.ErrPermission) || (runtime.GOOS == "windows" && strings.Contains(strings.ToLower(err.Error()), "privilege")) {
			t.Skipf("symlink creation is not permitted on this system: %v", err)
		}
		t.Fatalf("create symlink %q -> %q: %v", linkPath, targetPath, err)
	}
}

func TestBackendAddrRejectsBarePort(t *testing.T) {
	t.Setenv("CPA_HELPER_ADDR", "18317")
	if _, err := backendAddr(); err == nil {
		t.Fatal("backendAddr accepted a bare port")
	}
}

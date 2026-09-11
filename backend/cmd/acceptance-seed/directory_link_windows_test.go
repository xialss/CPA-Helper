package main

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestAcceptanceSeedRejectsJunctionEscapeBeforeMutation(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		testAcceptanceSeedRejectsDirectoryEscapeBeforeMutation(t, createTestDirectoryJunction)
	})
	t.Run("legacy_junction_symlinks", func(t *testing.T) {
		// Go's legacy mode lets EvalSymlinks traverse junctions. Verify that
		// resolving them still cannot move the marked acceptance boundary.
		t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",winsymlink=0")
		testAcceptanceSeedRejectsDirectoryEscapeBeforeMutation(t, createTestDirectoryJunction)
	})
}

func createTestDirectoryJunction(t *testing.T, targetPath, linkPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// Junction creation does not require the Windows symbolic-link privilege.
	// Pass paths as data so temporary-directory names cannot become shell code.
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command",
		`$ErrorActionPreference = 'Stop'; New-Item -ItemType Junction -Path $env:CPA_HELPER_TEST_LINK_PATH -Value $env:CPA_HELPER_TEST_LINK_TARGET | Out-Null`)
	command.Env = append(os.Environ(), "CPA_HELPER_TEST_LINK_PATH="+linkPath, "CPA_HELPER_TEST_LINK_TARGET="+targetPath)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create directory junction %q -> %q: %v: %s", linkPath, targetPath, err, output)
	}
	if _, err := os.Stat(linkPath); err != nil {
		actualTarget, readErr := os.Readlink(linkPath)
		t.Fatalf("stat created junction %q -> %q: %v (stored target %q, readlink error %v)", linkPath, targetPath, err, actualTarget, readErr)
	}
}

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "git-remote-sync-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, string(out))
	}

	return dir
}

func TestReadRemoteConfig_CommandLineOverridesAll(t *testing.T) {
	dir := setupTestGitRepo(t)
	defer os.RemoveAll(dir)

	// Set git config
	cmd := exec.Command("git", "config", "remote-sync.remote-path", "gituser@githost:/git/path")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}
	cmd = exec.Command("git", "config", "remote-sync.remote-setup", ". /git/env")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	// Create ._remote_sync file
	cfgPath := filepath.Join(dir, "._remote_sync")
	fileContent := "remote-path:fileuser@filehost:/file/path\nremote-setup:. /file/env\n"
	if err := os.WriteFile(cfgPath, []byte(fileContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Change working dir to repo for git config resolution
	origWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origWd)

	// Test 1: Command line overrides both file and git config
	cfg, err := readRemoteConfig(cfgPath, "cmduser@cmdhost:/cmd/path", ". /cmd/env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User != "cmduser" || cfg.Host != "cmdhost" || cfg.Path != "/cmd/path" {
		t.Errorf("expected cmdline path settings, got user=%s host=%s path=%s", cfg.User, cfg.Host, cfg.Path)
	}
	if cfg.Setup != ". /cmd/env" {
		t.Errorf("expected cmdline setup '. /cmd/env', got '%s'", cfg.Setup)
	}
}

func TestReadRemoteConfig_FileOverridesGitConfig(t *testing.T) {
	dir := setupTestGitRepo(t)
	defer os.RemoveAll(dir)

	// Set git config
	cmd := exec.Command("git", "config", "remote-sync.remote-path", "gituser@githost:/git/path")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}
	cmd = exec.Command("git", "config", "remote-sync.remote-setup", ". /git/env")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	// Create ._remote_sync file
	cfgPath := filepath.Join(dir, "._remote_sync")
	fileContent := "remote-path:fileuser@filehost:/file/path\nremote-setup:. /file/env\n"
	if err := os.WriteFile(cfgPath, []byte(fileContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	origWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origWd)

	// Test 2: File overrides git config when no CLI args
	cfg, err := readRemoteConfig(cfgPath, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User != "fileuser" || cfg.Host != "filehost" || cfg.Path != "/file/path" {
		t.Errorf("expected file path settings, got user=%s host=%s path=%s", cfg.User, cfg.Host, cfg.Path)
	}
	if cfg.Setup != ". /file/env" {
		t.Errorf("expected file setup '. /file/env', got '%s'", cfg.Setup)
	}
}

func TestReadRemoteConfig_GitConfigFallback(t *testing.T) {
	dir := setupTestGitRepo(t)
	defer os.RemoveAll(dir)

	// Set git config
	cmd := exec.Command("git", "config", "remote-sync.remote-path", "gituser@githost:/git/path")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}
	cmd = exec.Command("git", "config", "remote-sync.remote-setup", ". /git/env")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	// No ._remote_sync file
	cfgPath := filepath.Join(dir, "._remote_sync")

	origWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origWd)

	// Test 3: Git config is used when no CLI args and no file
	cfg, err := readRemoteConfig(cfgPath, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User != "gituser" || cfg.Host != "githost" || cfg.Path != "/git/path" {
		t.Errorf("expected git config path settings, got user=%s host=%s path=%s", cfg.User, cfg.Host, cfg.Path)
	}
	if cfg.Setup != ". /git/env" {
		t.Errorf("expected git config setup '. /git/env', got '%s'", cfg.Setup)
	}
}

func TestReadRemoteConfig_GitConfigHostOnly(t *testing.T) {
	dir := setupTestGitRepo(t)
	defer os.RemoveAll(dir)

	// Set git config with hostname-only (SSH config style)
	cmd := exec.Command("git", "config", "remote-sync.remote-path", "pok56:/home/ccw/repo")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set git config: %v", err)
	}

	cfgPath := filepath.Join(dir, "._remote_sync")

	origWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origWd)

	cfg, err := readRemoteConfig(cfgPath, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User != "" || cfg.Host != "pok56" || cfg.Path != "/home/ccw/repo" {
		t.Errorf("expected host-only settings, got user=%s host=%s path=%s", cfg.User, cfg.Host, cfg.Path)
	}
}

func TestReadRemoteConfig_MissingConfigError(t *testing.T) {
	dir := setupTestGitRepo(t)
	defer os.RemoveAll(dir)

	cfgPath := filepath.Join(dir, "._remote_sync")

	origWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origWd)

	_, err := readRemoteConfig(cfgPath, "", "")
	if err == nil {
		t.Fatalf("expected error when no configuration is present, got nil")
	}
}

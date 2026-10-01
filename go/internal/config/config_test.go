package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFileDoesNotOverrideExistingVariables(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	body := "# comment\r\nALPHA=1\r\nBETA=two\r\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	os.Setenv("BETA", "preset")
	defer os.Unsetenv("BETA")
	defer os.Unsetenv("ALPHA")
	if err := LoadEnvFile(p); err != nil {
		t.Fatalf("load env: %v", err)
	}
	if got := os.Getenv("ALPHA"); got != "1" {
		t.Fatalf("expected ALPHA=1, got %q", got)
	}
	if got := os.Getenv("BETA"); got != "preset" {
		t.Fatalf("an already set variable must not be overwritten, got %q", got)
	}
}

func TestGetIntFallsBackToTheDefault(t *testing.T) {
	os.Unsetenv("NH_TEST_INT")
	if got := getInt("NH_TEST_INT", 42); got != 42 {
		t.Fatalf("expected the default 42, got %d", got)
	}
	os.Setenv("NH_TEST_INT", "7")
	defer os.Unsetenv("NH_TEST_INT")
	if got := getInt("NH_TEST_INT", 42); got != 7 {
		t.Fatalf("expected 7, got %d", got)
	}
}

func writeDeployment(t *testing.T, net string, contracts string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "contracts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := fmt.Sprintf("{\"network\":%q,\"contracts\":%s}", net, contracts)
	path := filepath.Join(root, "contracts", "deployments.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write deployment: %v", err)
	}
	return root
}

func TestApplyDeploymentOverridesUsesTruffleOutput(t *testing.T) {
	c := &Contracts{HiveToken: "stale", TaskCoordinator: "stale", HiveSnowball: "keep-me"}
	root := writeDeployment(t, "ganache", "{\"HiveToken\":\"0xaaa\",\"TaskCoordinator\":\"0xbbb\"}")
	ApplyDeploymentOverrides(root, c)
	if c.HiveToken != "0xaaa" || c.TaskCoordinator != "0xbbb" {
		t.Fatalf("deployment addresses must override the configured ones: %+v", c)
	}
	if c.HiveSnowball != "keep-me" {
		t.Fatalf("addresses absent from the deployment file must be preserved")
	}
}

func TestApplyDeploymentOverridesIgnoresThrowawayTestNetwork(t *testing.T) {
	c := &Contracts{HiveToken: "keep-me"}
	root := writeDeployment(t, "test", "{\"HiveToken\":\"0xephemeral\"}")
	ApplyDeploymentOverrides(root, c)
	if c.HiveToken != "keep-me" {
		t.Fatalf("a throwaway test deployment must be ignored")
	}
}

func TestApplyDeploymentOverridesIsANoOpWithoutAFile(t *testing.T) {
	c := &Contracts{HiveToken: "keep-me"}
	ApplyDeploymentOverrides(t.TempDir(), c)
	if c.HiveToken != "keep-me" {
		t.Fatalf("a missing deployment file must leave the configuration untouched")
	}
}

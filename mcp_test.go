package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func captureDispatchOutput(line string) string {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	dispatch(line)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	return strings.TrimSpace(buf.String())
}

func TestMCP_Initialize(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	out := captureDispatchOutput(req)

	var resp JSONRPCResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("failed to parse JSON response: %v\nraw: %s", err, out)
	}

	if string(resp.ID) != "1" {
		t.Errorf("expected ID 1, got %s", string(resp.ID))
	}

	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}
	serverInfo, ok := resMap["serverInfo"].(map[string]any)
	if !ok || serverInfo["name"] != "git-remote-sync-mcp" {
		t.Errorf("unexpected serverInfo: %v", serverInfo)
	}
}

func TestMCP_ToolsList(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	out := captureDispatchOutput(req)

	var resp JSONRPCResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("failed to parse JSON response: %v\nraw: %s", err, out)
	}

	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}

	tools, ok := resMap["tools"].([]any)
	if !ok || len(tools) < 4 {
		t.Fatalf("expected at least 4 tools, got %v", tools)
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		tmap := tool.(map[string]any)
		names[tmap["name"].(string)] = true
	}

	expectedTools := []string{
		"git_remote_sync",
		"git_remote_sync_config_get",
		"git_remote_sync_config_set",
		"git_remote_sync_help",
	}
	for _, expected := range expectedTools {
		if !names[expected] {
			t.Errorf("missing tool in tools/list: %s", expected)
		}
	}
}

func TestMCP_HelpTool(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"git_remote_sync_help","arguments":{}}}`
	out := captureDispatchOutput(req)

	var resp JSONRPCResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("failed to parse JSON response: %v\nraw: %s", err, out)
	}

	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}

	content := resMap["content"].([]any)
	firstText := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(firstText, "git_remote_sync") {
		t.Errorf("expected help text to mention git_remote_sync, got: %s", firstText)
	}
}

func TestMCP_ConfigSetAndGet(t *testing.T) {
	dir := setupTestGitRepo(t)
	defer os.RemoveAll(dir)

	// Call config_set
	setArgs := map[string]any{
		"repo_path":    dir,
		"remote_path":  "user@pok56:/home/user/repo",
		"remote_setup": ". ./.env",
	}
	setReqBytes, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "git_remote_sync_config_set",
			"arguments": setArgs,
		},
	})

	setOut := captureDispatchOutput(string(setReqBytes))
	var setResp JSONRPCResponse
	if err := json.Unmarshal([]byte(setOut), &setResp); err != nil {
		t.Fatalf("failed to parse set response: %v\nraw: %s", err, setOut)
	}
	if setResp.Error != nil {
		t.Fatalf("set error: %v", setResp.Error)
	}

	// Verify with git config command directly
	cmd := exec.Command("git", "config", "remote-sync.remote-path")
	cmd.Dir = dir
	val, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(val)) != "user@pok56:/home/user/repo" {
		t.Errorf("unexpected git config remote-path: %s (err: %v)", string(val), err)
	}

	// Call config_get
	getReqBytes, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "git_remote_sync_config_get",
			"arguments": map[string]any{
				"repo_path": dir,
			},
		},
	})

	getOut := captureDispatchOutput(string(getReqBytes))
	var getResp JSONRPCResponse
	if err := json.Unmarshal([]byte(getOut), &getResp); err != nil {
		t.Fatalf("failed to parse get response: %v\nraw: %s", err, getOut)
	}
	resMap := getResp.Result.(map[string]any)
	content := resMap["content"].([]any)
	getText := content[0].(map[string]any)["text"].(string)

	if !strings.Contains(getText, "user@pok56:/home/user/repo") {
		t.Errorf("expected get output to contain configured path, got:\n%s", getText)
	}
	if !strings.Contains(getText, "git config (local)") {
		t.Errorf("expected source to indicate git config (local), got:\n%s", getText)
	}
}

func TestMCP_UnknownTool(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"unknown_tool","arguments":{}}}`
	out := captureDispatchOutput(req)

	var resp JSONRPCResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("failed to parse JSON response: %v\nraw: %s", err, out)
	}

	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("expected method not found error (-32601), got %v", resp.Error)
	}
}

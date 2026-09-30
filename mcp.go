package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	mcpServerName    = "git-remote-sync-mcp"
	mcpServerVersion = version
)

// JSONRPCRequest represents an incoming JSON-RPC 2.0 request or notification
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents an outgoing JSON-RPC 2.0 response
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type CallToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      ServerInfo     `json:"serverInfo"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ToolsListResult struct {
	Tools []ToolDefinition `json:"tools"`
}

// Global path resolution
var gBinPath string

func resolveGitRemoteSyncBin() string {
	if gBinPath != "" {
		return gBinPath
	}

	// 1. $GIT_REMOTE_SYNC_BIN
	if env := os.Getenv("GIT_REMOTE_SYNC_BIN"); env != "" {
		gBinPath = env
		return gBinPath
	}

	// 2. <exedir>/git-remote-sync
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "git-remote-sync")
		if info, err := os.Stat(sibling); err == nil && info.Mode()&0111 != 0 {
			gBinPath = sibling
			return gBinPath
		}
	}

	// 3. ./git-remote-sync in current working directory
	if info, err := os.Stat("./git-remote-sync"); err == nil && info.Mode()&0111 != 0 {
		gBinPath = "./git-remote-sync"
		return gBinPath
	}

	// 4. PATH fallback
	gBinPath = "git-remote-sync"
	return gBinPath
}

func getTools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name: "git_remote_sync",
			Description: "Run git-remote-sync to synchronize the current git repository state (current branch, commits, uncommitted changes, untracked files, and symlinks) with a remote repository over SSH. " +
				"Reads configuration from command arguments, ._remote_sync config file, or git config properties (remote-sync.remote-path and remote-sync.remote-setup).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Local repository directory path. Defaults to current working directory if omitted.",
					},
					"remote_path": map[string]any{
						"type":        "string",
						"description": "Remote path in format [user@]host[:path] (e.g. user@host:/path or host:/path). Overrides config file and git config.",
					},
					"remote_setup": map[string]any{
						"type":        "string",
						"description": "Remote setup/environment command executed before remote operations (e.g. '. ~/.env' or '. ./.env'). Overrides config file and git config.",
					},
					"verbose": map[string]any{
						"type":        "boolean",
						"description": "Enable verbose output to show all local and remote git commands. Default false.",
					},
				},
			},
		},
		{
			Name: "git_remote_sync_config_get",
			Description: "Inspect the effective git-remote-sync configuration for a local repository, showing values resolved from CLI options, ._remote_sync file, and git config properties (remote-sync.remote-path, remote-sync.remote-setup).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Local repository directory path. Defaults to current working directory if omitted.",
					},
				},
			},
		},
		{
			Name: "git_remote_sync_config_set",
			Description: "Set git config properties for git-remote-sync (remote-sync.remote-path and/or remote-sync.remote-setup) either locally for the repository or globally.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo_path": map[string]any{
						"type":        "string",
						"description": "Local repository directory path. Defaults to current working directory if omitted.",
					},
					"remote_path": map[string]any{
						"type":        "string",
						"description": "Remote path to set in git config, format [user@]host[:path] (e.g. 'ccw@pok56:/home/ccw/delve').",
					},
					"remote_setup": map[string]any{
						"type":        "string",
						"description": "Remote setup command to set in git config (e.g. '. ./.env').",
					},
					"global": map[string]any{
						"type":        "boolean",
						"description": "If true, set in global git config (~/.gitconfig) instead of local repository config. Default false.",
					},
				},
			},
		},
		{
			Name:        "git_remote_sync_help",
			Description: "Return git-remote-sync usage, configuration guide, precedence rules, and examples.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

func sendResponse(resp JSONRPCResponse) {
	outBytes, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling response: %v\n", err)
		return
	}
	os.Stdout.Write(outBytes)
	os.Stdout.Write([]byte("\n"))
}

func sendError(id json.RawMessage, code int, message string) {
	sendResponse(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
		},
	})
}

func sendToolResult(id json.RawMessage, text string, isError bool) {
	sendResponse(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: CallToolResult{
			Content: []ToolContent{
				{
					Type: "text",
					Text: text,
				},
			},
			IsError: isError,
		},
	})
}

func handleGitRemoteSync(id json.RawMessage, args map[string]any) {
	repoPath, _ := args["repo_path"].(string)
	remotePath, _ := args["remote_path"].(string)
	remoteSetup, _ := args["remote_setup"].(string)
	verbose, _ := args["verbose"].(bool)

	bin := resolveGitRemoteSyncBin()

	var cmdArgs []string
	if remotePath != "" {
		cmdArgs = append(cmdArgs, "-remote-path", remotePath)
	}
	if remoteSetup != "" {
		cmdArgs = append(cmdArgs, "-remote-setup", remoteSetup)
	}
	if verbose {
		cmdArgs = append(cmdArgs, "-v")
	}

	cmd := exec.Command(bin, cmdArgs...)
	if repoPath != "" {
		cmd.Dir = repoPath
	}

	var outputBuf bytes.Buffer
	cmd.Stdout = &outputBuf
	cmd.Stderr = &outputBuf

	err := cmd.Run()
	outputStr := outputBuf.String()

	var resultText strings.Builder
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	resultText.WriteString(fmt.Sprintf("exit code: %d\ncommand: %s", exitCode, bin))
	if len(cmdArgs) > 0 {
		resultText.WriteString(fmt.Sprintf(" %s", strings.Join(cmdArgs, " ")))
	}
	if repoPath != "" {
		resultText.WriteString(fmt.Sprintf(" (cwd: %s)", repoPath))
	}
	resultText.WriteString("\n\n")
	resultText.WriteString(outputStr)

	sendToolResult(id, resultText.String(), exitCode != 0)
}

func handleConfigGet(id json.RawMessage, args map[string]any) {
	repoPath, _ := args["repo_path"].(string)

	// Determine git root
	revParseCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if repoPath != "" {
		revParseCmd.Dir = repoPath
	}
	gitRootBytes, err := revParseCmd.Output()
	if err != nil {
		sendToolResult(id, fmt.Sprintf("Error: not in a git repository: %v", err), true)
		return
	}
	gitRoot := strings.TrimSpace(string(gitRootBytes))

	// Check ._remote_sync file
	cfgFile := filepath.Join(gitRoot, "._remote_sync")
	fileRemotePath := ""
	fileRemoteSetup := ""
	fileExists := false
	if data, err := os.ReadFile(cfgFile); err == nil {
		fileExists = true
		lines := strings.Split(string(data), "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			parts := strings.SplitN(l, ":", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				v := strings.TrimSpace(parts[1])
				if k == "remote-path" {
					fileRemotePath = v
				} else if k == "remote-setup" {
					fileRemoteSetup = v
				}
			}
		}
	}

	// Helper to get git config with specific flag
	getGitCfg := func(scopeFlag string, key string) string {
		var c *exec.Cmd
		if scopeFlag != "" {
			c = exec.Command("git", "config", scopeFlag, key)
		} else {
			c = exec.Command("git", "config", key)
		}
		c.Dir = gitRoot
		out, err := c.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	localPath := getGitCfg("--local", "remote-sync.remote-path")
	localSetup := getGitCfg("--local", "remote-sync.remote-setup")
	globalPath := getGitCfg("--global", "remote-sync.remote-path")
	globalSetup := getGitCfg("--global", "remote-sync.remote-setup")
	effectiveGitPath := getGitCfg("", "remote-sync.remote-path")
	effectiveGitSetup := getGitCfg("", "remote-sync.remote-setup")

	// Effective resolved config (file > git config)
	effectivePath := fileRemotePath
	effectivePathSource := "file (._remote_sync)"
	if effectivePath == "" {
		if effectiveGitPath != "" {
			effectivePath = effectiveGitPath
			if localPath != "" {
				effectivePathSource = "git config (local)"
			} else {
				effectivePathSource = "git config (global)"
			}
		} else {
			effectivePathSource = "none"
		}
	}

	effectiveSetup := fileRemoteSetup
	effectiveSetupSource := "file (._remote_sync)"
	if effectiveSetup == "" {
		if effectiveGitSetup != "" {
			effectiveSetup = effectiveGitSetup
			if localSetup != "" {
				effectiveSetupSource = "git config (local)"
			} else {
				effectiveSetupSource = "git config (global)"
			}
		} else {
			effectiveSetupSource = "none"
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Repository: %s\n\n", gitRoot))
	sb.WriteString("=== Effective Configuration ===\n")
	sb.WriteString(fmt.Sprintf("remote-path  : %s (source: %s)\n", effectivePath, effectivePathSource))
	sb.WriteString(fmt.Sprintf("remote-setup : %s (source: %s)\n\n", effectiveSetup, effectiveSetupSource))

	sb.WriteString("=== Configuration Sources ===\n")
	sb.WriteString(fmt.Sprintf("1. File: %s\n", cfgFile))
	if fileExists {
		sb.WriteString(fmt.Sprintf("   - remote-path  : %s\n", fileRemotePath))
		sb.WriteString(fmt.Sprintf("   - remote-setup : %s\n", fileRemoteSetup))
	} else {
		sb.WriteString("   - (file not present)\n")
	}

	sb.WriteString("2. Git Config (Local repository):\n")
	sb.WriteString(fmt.Sprintf("   - remote-sync.remote-path  : %s\n", localPath))
	sb.WriteString(fmt.Sprintf("   - remote-sync.remote-setup : %s\n", localSetup))

	sb.WriteString("3. Git Config (Global ~/.gitconfig):\n")
	sb.WriteString(fmt.Sprintf("   - remote-sync.remote-path  : %s\n", globalPath))
	sb.WriteString(fmt.Sprintf("   - remote-sync.remote-setup : %s\n", globalSetup))

	sendToolResult(id, sb.String(), false)
}

func handleConfigSet(id json.RawMessage, args map[string]any) {
	repoPath, _ := args["repo_path"].(string)
	remotePath, _ := args["remote_path"].(string)
	remoteSetup, _ := args["remote_setup"].(string)
	isGlobal, _ := args["global"].(bool)

	if remotePath == "" && remoteSetup == "" {
		sendToolResult(id, "Error: at least one of remote_path or remote_setup must be specified", true)
		return
	}

	var scopeArg string
	if isGlobal {
		scopeArg = "--global"
	} else {
		scopeArg = "--local"
	}

	var results []string

	if remotePath != "" {
		cmdArgs := []string{"config"}
		if scopeArg != "" {
			cmdArgs = append(cmdArgs, scopeArg)
		}
		cmdArgs = append(cmdArgs, "remote-sync.remote-path", remotePath)
		cmd := exec.Command("git", cmdArgs...)
		if repoPath != "" {
			cmd.Dir = repoPath
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			sendToolResult(id, fmt.Sprintf("Error setting remote-sync.remote-path: %v (%s)", err, string(out)), true)
			return
		}
		results = append(results, fmt.Sprintf("Set %s remote-sync.remote-path = %s", scopeArg, remotePath))
	}

	if remoteSetup != "" {
		cmdArgs := []string{"config"}
		if scopeArg != "" {
			cmdArgs = append(cmdArgs, scopeArg)
		}
		cmdArgs = append(cmdArgs, "remote-sync.remote-setup", remoteSetup)
		cmd := exec.Command("git", cmdArgs...)
		if repoPath != "" {
			cmd.Dir = repoPath
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			sendToolResult(id, fmt.Sprintf("Error setting remote-sync.remote-setup: %v (%s)", err, string(out)), true)
			return
		}
		results = append(results, fmt.Sprintf("Set %s remote-sync.remote-setup = %s", scopeArg, remoteSetup))
	}

	sendToolResult(id, strings.Join(results, "\n"), false)
}

func handleHelp(id json.RawMessage) {
	helpText := `git-remote-sync — sync local git repository with remote over SSH without git push.

MCP Tools:
  - git_remote_sync: Run sync on a repository (supports repo_path, remote_path, remote_setup, verbose)
  - git_remote_sync_config_get: Inspect effective and source-specific configuration
  - git_remote_sync_config_set: Set git config properties (locally or globally)
  - git_remote_sync_help: Show this help summary

Configuration Precedence (highest to lowest):
  1. Command-line flags / MCP tool arguments (remote_path, remote_setup)
  2. Config file (._remote_sync in repository root)
  3. Git config properties (local .git/config)
  4. Git config properties (global ~/.gitconfig)

Git Config Property Names:
  remote-sync.remote-path  : [user@]host[:path] (e.g. user@host:/path or host:/path)
  remote-sync.remote-setup : Shell command before remote ops (e.g. '. ~/.env')

Examples:
  # Set repo git config
  git config remote-sync.remote-path "ccw@pok56:/home/ccw/delve"
  git config remote-sync.remote-setup ". ./.env"

  # Or globally
  git config --global remote-sync.remote-setup ". ~/.env"
`
	sendToolResult(id, helpText, false)
}

func handleCallTool(id json.RawMessage, params json.RawMessage) {
	var callParams struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &callParams); err != nil {
		sendError(id, -32602, fmt.Sprintf("Invalid params: %v", err))
		return
	}

	if callParams.Arguments == nil {
		callParams.Arguments = make(map[string]any)
	}

	switch callParams.Name {
	case "git_remote_sync":
		handleGitRemoteSync(id, callParams.Arguments)
	case "git_remote_sync_config_get":
		handleConfigGet(id, callParams.Arguments)
	case "git_remote_sync_config_set":
		handleConfigSet(id, callParams.Arguments)
	case "git_remote_sync_help":
		handleHelp(id)
	default:
		sendError(id, -32601, fmt.Sprintf("Unknown tool: %s", callParams.Name))
	}
}

func dispatch(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	var req JSONRPCRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		sendError(nil, -32700, "Parse error")
		return
	}

	// Notifications have no id
	if len(req.ID) == 0 && req.Method == "notifications/initialized" {
		return
	}

	switch req.Method {
	case "initialize":
		sendResponse(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: InitializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities: map[string]any{
					"tools": map[string]any{},
				},
				ServerInfo: ServerInfo{
					Name:    mcpServerName,
					Version: mcpServerVersion,
				},
			},
		})
	case "tools/list":
		sendResponse(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ToolsListResult{
				Tools: getTools(),
			},
		})
	case "tools/call":
		handleCallTool(req.ID, req.Params)
	case "ping":
		sendResponse(JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		})
	default:
		if len(req.ID) > 0 {
			sendError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
		}
	}
}

func runMCP() {
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			dispatch(line)
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "Read error: %v\n", err)
			}
			break
		}
	}
}

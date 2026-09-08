/*
Copyright © 2025 Docker, Inc.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/mark3labs/mcp-go/mcp"
)

type stdioMCPClient struct {
	command string
	env     []string
	args    []string

	stdin       io.WriteCloser
	requestID   atomic.Int64
	responses   sync.Map
	close       func() error
	initialized atomic.Bool
}

func newMCPClient(command string, env []string, args ...string) *stdioMCPClient {
	return &stdioMCPClient{
		command: command,
		env:     env,
		args:    args,
	}
}

var containerEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// dockerCommand keeps repository-provided examples out of the host CLI's
// environment. Docker receives them only as literal container arguments.
func (c *stdioMCPClient) dockerCommand(ctx context.Context) (*exec.Cmd, error) {
	if c.command != "docker" || len(c.args) == 0 || c.args[0] != "run" {
		return nil, errors.New("expected Docker run transport")
	}
	values := make(map[string]string, len(c.env))
	for _, entry := range c.env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !containerEnvName.MatchString(name) || strings.ContainsRune(value, '\x00') {
			return nil, errors.New("invalid container environment entry")
		}
		values[name] = value
	}
	args := []string{"run"}
	for i := 1; i < len(c.args); i++ {
		arg := c.args[i]
		switch arg {
		case "-e", "--env":
			i++
			if i >= len(c.args) {
				return nil, errors.New("missing container environment name")
			}
			name := c.args[i]
			value, ok := values[name]
			if !ok {
				return nil, errors.New("container environment has no example value")
			}
			args = append(args, "--env", name+"="+value)
		case "--network":
			i++
			if i >= len(c.args) {
				return nil, errors.New("missing container network")
			}
			args = append(args, arg, c.args[i])
		default:
			args = append(args, arg)
			if !strings.HasPrefix(arg, "-") {
				// Image and container command arguments must remain byte-for-byte
				// intact, even when the container command itself contains -e.
				args = append(args, c.args[i+1:]...)
				i = len(c.args)
			}
		}
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = []string{}
	// Connection settings come only from the trusted invoking process. In CI a
	// confined wrapper also fixes the socket and config; examples cannot select
	// another daemon, credential helper, loader or shell initialization file.
	for _, name := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
		"DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY", "DOCKER_API_VERSION", "SystemRoot", "ProgramW6432"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	return cmd, nil
}

func (c *stdioMCPClient) Initialize(ctx context.Context, request mcp.InitializeRequest, debug bool) (*mcp.InitializeResult, error) {
	if c.initialized.Load() {
		return nil, fmt.Errorf("client already initialized")
	}

	ctxCmd, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd, err := c.dockerCommand(ctxCmd)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}

	var stderr bytes.Buffer
	if debug {
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	} else {
		cmd.Stderr = &stderr
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	c.stdin = stdin

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	c.close = func() error {
		cancel()
		return nil
	}
	go func() {
		cmd.Wait()
		cancel()
	}()
	go func() {
		c.readResponses(bufio.NewReader(stdout))
	}()

	var result mcp.InitializeResult
	errs := make(chan error)
	go func() {
		<-ctxCmd.Done()
		errs <- errors.New(stderr.String())
	}()
	go func() {
		errs <- func() error {
			params := struct {
				ProtocolVersion string                 `json:"protocolVersion"`
				ClientInfo      mcp.Implementation     `json:"clientInfo"`
				Capabilities    mcp.ClientCapabilities `json:"capabilities"`
			}{
				ProtocolVersion: request.Params.ProtocolVersion,
				ClientInfo:      request.Params.ClientInfo,
				Capabilities:    request.Params.Capabilities,
			}

			response, err := c.sendRequest(ctx, "initialize", params)
			if err != nil {
				return err
			}

			if err := json.Unmarshal(*response, &result); err != nil {
				return fmt.Errorf("failed to unmarshal response: %w", err)
			}

			encoder := json.NewEncoder(stdin)
			if err := encoder.Encode(mcp.JSONRPCNotification{
				JSONRPC: mcp.JSONRPC_VERSION,
				Notification: mcp.Notification{
					Method: "notifications/initialized",
				},
			}); err != nil {
				return fmt.Errorf("failed to marshal initialized notification: %w", err)
			}

			c.initialized.Store(true)
			return nil
		}()
	}()

	return &result, <-errs
}

func (c *stdioMCPClient) Close() error {
	return c.close()
}

func (c *stdioMCPClient) readResponses(stdout *bufio.Reader) error {
	for {
		buf, err := stdout.ReadBytes('\n')
		if err != nil {
			return err
		}

		var baseMessage BaseMessage
		if err := json.Unmarshal(buf, &baseMessage); err != nil {
			continue
		}

		if baseMessage.ID == nil {
			continue
		}

		if ch, ok := c.responses.LoadAndDelete(*baseMessage.ID); ok {
			responseChan := ch.(chan RPCResponse)

			if baseMessage.Error != nil {
				responseChan <- RPCResponse{
					Error: &baseMessage.Error.Message,
				}
			} else {
				responseChan <- RPCResponse{
					Response: &baseMessage.Result,
				}
			}
		}
	}
}

func (c *stdioMCPClient) sendRequest(ctx context.Context, method string, params any) (*json.RawMessage, error) {
	id := c.requestID.Add(1)
	responseChan := make(chan RPCResponse, 1)
	c.responses.Store(id, responseChan)

	encoder := json.NewEncoder(c.stdin)
	if err := encoder.Encode(mcp.JSONRPCRequest{
		JSONRPC: mcp.JSONRPC_VERSION,
		ID:      id,
		Request: mcp.Request{
			Method: method,
		},
		Params: params,
	}); err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-responseChan:
		if response.Error != nil {
			return nil, errors.New(*response.Error)
		}
		return response.Response, nil
	}
}

func (c *stdioMCPClient) ListTools(ctx context.Context, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	response, err := c.sendRequest(ctx, "tools/list", request.Params)
	if err != nil {
		return nil, err
	}

	var result mcp.ListToolsResult
	if err := json.Unmarshal(*response, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &result, nil
}

func (c *stdioMCPClient) ListPrompts(ctx context.Context, request mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error) {
	response, err := c.sendRequest(ctx, "prompts/list", request.Params)
	if err != nil {
		return nil, err
	}

	var result mcp.ListPromptsResult
	if err := json.Unmarshal(*response, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &result, nil
}

func (c *stdioMCPClient) CallTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	response, err := c.sendRequest(ctx, "tools/call", request.Params)
	if err != nil {
		return nil, err
	}

	return mcp.ParseCallToolResult(response)
}

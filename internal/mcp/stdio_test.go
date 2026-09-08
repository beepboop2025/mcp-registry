package mcp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestRepositoryExamplesCannotInitializeHostShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell execution regression")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "host-code-executed")
	hook := filepath.Join(dir, "host-hook")
	if err := os.WriteFile(hook, []byte("touch '"+marker+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeDocker := "#!/bin/bash\nprintf 'HOST_DOCKER_HOST=%s\\n' \"$DOCKER_HOST\"\nprintf 'HOST_DOCKER_CONFIG=%s\\n' \"$DOCKER_CONFIG\"\nprintf 'HOST_BASH_ENV=%s\\n' \"${BASH_ENV:-}\"\nprintf '%s\\n' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeDocker), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_HOST", "unix:///run/user/1010/docker.sock")
	t.Setenv("DOCKER_CONFIG", "/trusted/empty-docker-config")
	c := newMCPClient("docker", []string{
		"BASH_ENV=" + hook, "LD_PRELOAD=/untrusted/loader.so",
		"DOCKER_HOST=unix:///untrusted.sock", "DOCKER_CONFIG=/untrusted/config",
		"SERVICE_API_KEY=synthetic=value with spaces",
	}, "run", "--rm", "-e", "BASH_ENV", "-e", "LD_PRELOAD", "-e", "DOCKER_HOST",
		"-e", "DOCKER_CONFIG", "-e", "SERVICE_API_KEY", "fixture/image", "sh", "-e", "literal")
	cmd, err := c.dockerCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI failed: %v: %s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("repository BASH_ENV executed on the host")
	}
	text := string(output)
	for _, expected := range []string{
		"HOST_DOCKER_HOST=unix:///run/user/1010/docker.sock\n",
		"HOST_DOCKER_CONFIG=/trusted/empty-docker-config\n", "HOST_BASH_ENV=\n",
		"--env\nBASH_ENV=" + hook + "\n", "--env\nLD_PRELOAD=/untrusted/loader.so\n",
		"--env\nDOCKER_HOST=unix:///untrusted.sock\n", "--env\nDOCKER_CONFIG=/untrusted/config\n",
		"--env\nSERVICE_API_KEY=synthetic=value with spaces\n", "fixture/image\nsh\n-e\nliteral\n",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("missing preserved CLI/container boundary %q in %q", expected, text)
		}
	}
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "LD_PRELOAD=") || strings.HasPrefix(entry, "BASH_ENV=") ||
			strings.HasPrefix(entry, "SERVICE_API_KEY=") {
			t.Errorf("repository value leaked into host CLI environment: %q", entry)
		}
	}
}

func TestContainerEnvironmentValidation(t *testing.T) {
	for _, entry := range []string{"NO_EQUALS", "=empty", "BAD-NAME=value", "VALUE=bad\x00value"} {
		c := newMCPClient("docker", []string{entry}, "run", "-e", "VALUE", "fixture/image")
		if _, err := c.dockerCommand(context.Background()); err == nil {
			t.Errorf("accepted malformed environment entry %q", entry)
		}
	}
	c := newMCPClient("docker", []string{"TOKEN=first", "TOKEN=last"},
		"run", "--network", "container:sidecar", "-e", "TOKEN", "fixture/image")
	cmd, err := c.dockerCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "run", "--network", "container:sidecar", "--env", "TOKEN=last", "fixture/image"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("duplicate precedence or sidecar network changed: got %q want %q", cmd.Args, want)
	}
}

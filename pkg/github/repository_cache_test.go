package github

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/mcp-registry/internal/licenses"
)

func TestRepositoryCachePreservesLicenseDecisionWithoutAPIAccess(t *testing.T) {
	for _, key := range []string{"mit", "gpl-3.0", "agpl-3.0"} {
		t.Run(key, func(t *testing.T) {
			cache, path := prepareRepositoryCache(t)
			repo := cache["repositories"].(map[string]any)["modelcontextprotocol/servers"].(map[string]any)
			repo["license"] = map[string]any{"key": key, "name": key}
			writeRepositoryCache(t, path, cache)
			// A nil underlying API client proves the cache performs no API access.
			repository, err := (&Client{}).GetProjectRepository(context.Background(), "https://github.com/modelcontextprotocol/servers")
			if err != nil {
				t.Fatal(err)
			}
			if licenses.IsValid(repository.License) != (key == "mit") {
				t.Fatalf("cached license changed the existing license gate for %s", key)
			}
		})
	}
}

func TestRepositoryCacheRejectsMismatchedAndUnusableMetadata(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"wrong sha":       func(c map[string]any) { c["candidate_sha"] = strings.Repeat("b", 40) },
		"wrong candidate": func(c map[string]any) { c["candidate_repository"] = "other/repo" },
		"stale":           func(c map[string]any) { c["fetched_at"] = time.Now().Add(-2 * time.Hour) },
		"future":          func(c map[string]any) { c["fetched_at"] = time.Now().Add(time.Minute) },
		"missing project": func(c map[string]any) { c["repositories"] = map[string]any{} },
		"invalid repository": func(c map[string]any) {
			c["repositories"] = map[string]any{"modelcontextprotocol/servers": map[string]any{"full_name": "other/repo"}}
		},
		"missing license": func(c map[string]any) {
			delete(c["repositories"].(map[string]any)["modelcontextprotocol/servers"].(map[string]any), "license")
		},
		"private repo": func(c map[string]any) {
			c["repositories"].(map[string]any)["modelcontextprotocol/servers"].(map[string]any)["private"] = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			cache, path := prepareRepositoryCache(t)
			mutate(cache)
			writeRepositoryCache(t, path, cache)
			if _, err := (&Client{}).GetProjectRepository(context.Background(), "https://github.com/modelcontextprotocol/servers"); err == nil {
				t.Fatal("invalid metadata was accepted")
			}
		})
	}
}

func TestRepositoryCacheMissingMalformedOrOversizedDoesNotFallBack(t *testing.T) {
	_, path := prepareRepositoryCache(t)
	for _, content := range []string{"", "{} {}", strings.Repeat("x", repositoryCacheLimit+1)} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (&Client{}).GetProjectRepository(context.Background(), "https://github.com/modelcontextprotocol/servers"); err == nil {
			t.Fatal("invalid cache fell back or passed")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Client{}).GetProjectRepository(context.Background(), "https://github.com/modelcontextprotocol/servers"); err == nil {
		t.Fatal("missing cache fell back or passed")
	}
}

func TestRepositoryCacheRejectsSymlinkAndGroupWritableFiles(t *testing.T) {
	cache, path := prepareRepositoryCache(t)
	writeRepositoryCache(t, path, cache)
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Client{}).GetProjectRepository(context.Background(), "https://github.com/modelcontextprotocol/servers"); err == nil {
		t.Fatal("group-writable metadata was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_REGISTRY_REPOSITORY_CACHE", link)
	if _, err := (&Client{}).GetProjectRepository(context.Background(), "https://github.com/modelcontextprotocol/servers"); err == nil {
		t.Fatal("symlink metadata was accepted")
	}
}

func prepareRepositoryCache(t *testing.T) (map[string]any, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repositories.json")
	t.Setenv("MCP_REGISTRY_REPOSITORY_CACHE", path)
	t.Setenv("MCP_REGISTRY_SOURCE_SHA", strings.Repeat("a", 40))
	t.Setenv("MCP_REGISTRY_SOURCE_REPOSITORY", "beepboop2025/mcp-registry")
	return map[string]any{"schema": 1, "candidate_repository": "beepboop2025/mcp-registry",
		"candidate_sha": strings.Repeat("a", 40), "fetched_at": time.Now().Add(-time.Second),
		"repositories": map[string]any{"modelcontextprotocol/servers": map[string]any{
			"id": 1, "full_name": "modelcontextprotocol/servers", "html_url": "https://github.com/modelcontextprotocol/servers",
			"owner": map[string]any{"login": "modelcontextprotocol"}, "private": false,
			"license": map[string]any{"key": "mit", "name": "MIT License"}}}}, path
}

func writeRepositoryCache(t *testing.T, path string, cache map[string]any) {
	t.Helper()
	content, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

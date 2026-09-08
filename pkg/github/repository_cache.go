package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v70/github"
)

const repositoryCacheLimit = 2 * 1024 * 1024

// repositoryFromCache reads public API metadata supplied by a trusted controller.
// Setting the cache opts into strict offline lookup: invalid or missing entries
// fail validation rather than silently falling back to another metadata source.
func repositoryFromCache(project string) (*github.Repository, bool, error) {
	path := os.Getenv("MCP_REGISTRY_REPOSITORY_CACHE")
	if path == "" {
		return nil, false, nil
	}
	fail := func(reason string) (*github.Repository, bool, error) {
		return nil, true, fmt.Errorf("repository metadata cache: %s", reason)
	}
	if !regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(project) {
		return fail("project must identify one GitHub repository")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return fail("file must be regular and not writable by group or others")
	}
	file, err := os.Open(path)
	if err != nil {
		return fail("cannot open cache file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o022 != 0 || !os.SameFile(info, opened) {
		return fail("opened cache does not match the validated regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, repositoryCacheLimit+1))
	if err != nil || len(content) > repositoryCacheLimit {
		return fail("content exceeds the size bound or cannot be read")
	}
	var cache struct {
		Schema              int                        `json:"schema"`
		CandidateRepository string                     `json:"candidate_repository"`
		CandidateSHA        string                     `json:"candidate_sha"`
		FetchedAt           time.Time                  `json:"fetched_at"`
		Repositories        map[string]json.RawMessage `json:"repositories"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cache); err != nil || decoder.Decode(new(any)) != io.EOF {
		return fail("invalid cache envelope")
	}
	expectedSHA := os.Getenv("MCP_REGISTRY_SOURCE_SHA")
	expectedRepository := os.Getenv("MCP_REGISTRY_SOURCE_REPOSITORY")
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(expectedSHA) ||
		!regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(expectedRepository) ||
		cache.Schema != 1 || cache.CandidateSHA != expectedSHA || cache.CandidateRepository != expectedRepository {
		return fail("candidate identity does not match the trusted invocation")
	}
	age := time.Since(cache.FetchedAt)
	if age < 0 || age > time.Hour || len(cache.Repositories) == 0 || len(cache.Repositories) > 12 {
		return fail("metadata is stale, future-dated, or exceeds the repository bound")
	}
	fullName := strings.TrimPrefix(project, "https://github.com/")
	raw, ok := cache.Repositories[fullName]
	if !ok {
		return fail("requested project is absent")
	}
	var repository github.Repository
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &repository) != nil || json.Unmarshal(raw, &fields) != nil ||
		repository.GetID() <= 0 || repository.GetFullName() != fullName ||
		repository.GetHTMLURL() != project || repository.GetPrivate() || repository.Private == nil ||
		repository.Owner == nil || repository.Owner.GetLogin() != strings.Split(fullName, "/")[0] {
		return fail("repository response has an invalid identity")
	}
	if _, exists := fields["license"]; !exists {
		return fail("repository response omits the license field")
	}
	if repository.License != nil && (repository.License.GetKey() == "" || repository.License.GetName() == "") {
		return fail("repository license metadata is incomplete")
	}
	return &repository, true, nil
}

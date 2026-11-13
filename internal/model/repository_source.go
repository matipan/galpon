package model

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var scpRepositoryURL = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?([A-Za-z0-9][A-Za-z0-9.-]*):([^\s]+)$`)

// ValidateRepositoryURL accepts Git transport URLs, not local path searches or
// executable Git remote helpers. Local repositories can use a file URL.
func ValidateRepositoryURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsFunc(value, unicode.IsSpace) || strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("enter a Git repository URL")
	}
	if match := scpRepositoryURL.FindStringSubmatch(value); match != nil && !strings.Contains(value, "://") && !strings.HasPrefix(match[2], ":") {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") == "" {
		return fmt.Errorf("enter a Git repository URL")
	}
	switch u.Scheme {
	case "https", "http", "ssh", "git":
		if u.Hostname() != "" {
			return nil
		}
	case "file":
		if u.Host == "" && strings.HasPrefix(u.Path, "/") {
			return nil
		}
	}
	return fmt.Errorf("enter an HTTPS, SSH, Git, or file repository URL")
}

// RepositorySourceKey identifies a source for an exact URL lookup. GitHub
// sources share an identity across HTTPS and SSH. Other hosts keep their
// transport and account, which can determine which repository Git reads.
func RepositorySourceKey(value string) string {
	value = strings.TrimSpace(value)
	if match := scpRepositoryURL.FindStringSubmatch(value); match != nil && !strings.Contains(value, "://") && strings.EqualFold(match[1], "github.com") {
		return "github.com/" + strings.ToLower(strings.TrimSuffix(strings.Trim(match[2], "/"), ".git"))
	}
	if u, err := url.Parse(value); err == nil {
		if strings.EqualFold(u.Host, "github.com") && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "ssh" || u.Scheme == "git") {
			return "github.com/" + strings.ToLower(strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"))
		}
		if u.Scheme == "file" && u.Host == "" {
			value = u.Path
		}
	}
	return strings.TrimRight(value, "/")
}

func (r Repository) HasSourceURL(value string) bool {
	key := RepositorySourceKey(value)
	if key == "" {
		return false
	}
	if RepositorySourceKey(r.SourcePath) == key {
		return true
	}
	for _, remote := range r.Remotes {
		if RepositorySourceKey(remote.FetchURL) == key {
			return true
		}
	}
	return false
}

// Package gitremote validates exact, transport-specific Git remote identities.
package gitremote

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

const maximumRemoteLength = 2000

// Canonicalize returns a transport-specific identity. It deliberately does
// not guess provider-specific aliases such as cross-transport or optional
// .git-suffix equivalence.
func Canonicalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maximumRemoteLength || strings.ContainsAny(raw, "?#") {
		return "", errors.New("invalid remote")
	}
	if !strings.Contains(raw, "://") {
		return canonicalizeSCPRemote(raw)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid remote")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "ssh" && scheme != "git" {
		return "", errors.New("invalid remote")
	}
	if err := validateRemoteUser(parsed, scheme); err != nil {
		return "", err
	}
	host, err := canonicalHost(parsed, scheme)
	if err != nil {
		return "", err
	}
	repositoryPath, err := canonicalRepositoryPath(parsed.EscapedPath(), true)
	if err != nil {
		return "", err
	}
	authority := host
	if scheme == "ssh" {
		authority = "git@" + host
	}
	return boundedCanonicalRemote(scheme + "://" + authority + "/" + repositoryPath)
}

func validateRemoteUser(parsed *url.URL, scheme string) error {
	if scheme == "ssh" {
		if parsed.User == nil || parsed.User.Username() != "git" {
			return errors.New("remote credentials are not allowed")
		}
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return errors.New("remote credentials are not allowed")
		}
		return nil
	}
	if parsed.User != nil {
		return errors.New("remote credentials are not allowed")
	}
	return nil
}

func canonicalizeSCPRemote(raw string) (string, error) {
	if strings.ContainsAny(raw, "?#") {
		return "", errors.New("invalid remote")
	}
	separator := strings.IndexByte(raw, ':')
	if separator <= 0 || separator == len(raw)-1 {
		return "", errors.New("invalid remote")
	}
	hostPart := raw[:separator]
	at := strings.LastIndexByte(hostPart, '@')
	if at <= 0 || at == len(hostPart)-1 || hostPart[:at] != "git" {
		return "", errors.New("invalid remote")
	}
	hostPart = hostPart[at+1:]
	if strings.ContainsAny(hostPart, "/\\:") {
		return "", errors.New("invalid remote")
	}
	repositoryPath, err := canonicalRepositoryPath(raw[separator+1:], false)
	if err != nil {
		return "", err
	}
	return boundedCanonicalRemote("scp://git@" + strings.ToLower(hostPart) + "/" + repositoryPath)
}

func canonicalHost(parsed *url.URL, scheme string) (string, error) {
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", errors.New("invalid remote")
	}
	port := parsed.Port()
	if port == "" || (scheme == "ssh" && port == "22") || (scheme == "https" && port == "443") {
		return hostname, nil
	}
	return net.JoinHostPort(hostname, port), nil
}

func canonicalRepositoryPath(repositoryPath string, requireLeadingSlash bool) (string, error) {
	if strings.Contains(repositoryPath, "%") || strings.Contains(repositoryPath, "\\") {
		return "", errors.New("invalid remote")
	}
	if requireLeadingSlash {
		if !strings.HasPrefix(repositoryPath, "/") || strings.HasPrefix(repositoryPath, "//") {
			return "", errors.New("invalid remote")
		}
		repositoryPath = strings.TrimPrefix(repositoryPath, "/")
	} else if strings.HasPrefix(repositoryPath, "/") {
		return "", errors.New("invalid remote")
	}
	if repositoryPath == "" || strings.HasSuffix(repositoryPath, "/") || strings.Contains(repositoryPath, "//") {
		return "", errors.New("invalid remote")
	}
	for _, segment := range strings.Split(repositoryPath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("invalid remote")
		}
	}
	return repositoryPath, nil
}

func boundedCanonicalRemote(remote string) (string, error) {
	if len(remote) > maximumRemoteLength {
		return "", errors.New("invalid remote")
	}
	return remote, nil
}

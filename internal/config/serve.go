package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

const defaultListenAddress = "127.0.0.1:8080"

var ErrInvalidServeConfig = errors.New("invalid serve configuration")

type ServeConfig struct {
	DatabasePath  string
	ListenAddress string
	TLSCertFile   string
	TLSKeyFile    string
	// AllowCleartextWeb enables Web credentials and non-Secure session cookies
	// on a loopback listener without TLS. Development convenience only: the
	// session cookie and every token then travel in the clear.
	AllowCleartextWeb bool
}

type EnvironmentLookup func(string) (string, bool)

func LoadServe(arguments []string, lookupEnvironment EnvironmentLookup) (ServeConfig, error) {
	databaseDefault := environmentValue(lookupEnvironment, "DBGRAPH_DATABASE_PATH", "")
	listenDefault := environmentValue(lookupEnvironment, "DBGRAPH_LISTEN_ADDRESS", defaultListenAddress)
	tlsCertDefault := environmentValue(lookupEnvironment, "DBGRAPH_TLS_CERT_FILE", "")
	tlsKeyDefault := environmentValue(lookupEnvironment, "DBGRAPH_TLS_KEY_FILE", "")

	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	databasePath := flags.String("database", databaseDefault, "SQLite database path")
	listenAddress := flags.String("listen", listenDefault, "HTTP listen address")
	tlsCertFile := flags.String("tls-cert", tlsCertDefault, "TLS certificate file")
	tlsKeyFile := flags.String("tls-key", tlsKeyDefault, "TLS private key file")
	cleartextWebDefault, err := booleanEnvironmentValue(lookupEnvironment, "DBGRAPH_INSECURE_CLEARTEXT_WEB")
	if err != nil {
		return ServeConfig{}, err
	}
	allowCleartextWeb := flags.Bool(
		"insecure-cleartext-web", cleartextWebDefault,
		"serve the Web UI without TLS on a loopback listener (development only)",
	)
	if err := flags.Parse(arguments); err != nil {
		return ServeConfig{}, fmt.Errorf("%w: %v", ErrInvalidServeConfig, err)
	}

	config := ServeConfig{
		DatabasePath:      strings.TrimSpace(*databasePath),
		ListenAddress:     strings.TrimSpace(*listenAddress),
		TLSCertFile:       strings.TrimSpace(*tlsCertFile),
		TLSKeyFile:        strings.TrimSpace(*tlsKeyFile),
		AllowCleartextWeb: *allowCleartextWeb,
	}
	if config.DatabasePath == "" {
		return ServeConfig{}, fmt.Errorf("%w: database path is required", ErrInvalidServeConfig)
	}
	host, _, err := net.SplitHostPort(config.ListenAddress)
	if err != nil {
		return ServeConfig{}, fmt.Errorf("%w: listen address must be host:port", ErrInvalidServeConfig)
	}
	if (config.TLSCertFile == "") != (config.TLSKeyFile == "") {
		return ServeConfig{}, fmt.Errorf("%w: TLS certificate and key must be configured together", ErrInvalidServeConfig)
	}
	if config.TLSCertFile == "" && !isLoopbackHost(host) {
		return ServeConfig{}, fmt.Errorf("%w: non-loopback listeners require TLS", ErrInvalidServeConfig)
	}
	if config.AllowCleartextWeb && !isLoopbackHost(host) {
		return ServeConfig{}, fmt.Errorf(
			"%w: insecure cleartext Web access requires a loopback listener", ErrInvalidServeConfig)
	}
	if config.AllowCleartextWeb && config.TLSCertFile != "" {
		return ServeConfig{}, fmt.Errorf(
			"%w: insecure cleartext Web access cannot be combined with TLS", ErrInvalidServeConfig)
	}

	return config, nil
}

func booleanEnvironmentValue(lookupEnvironment EnvironmentLookup, key string) (bool, error) {
	raw := environmentValue(lookupEnvironment, key, "")
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("%w: %s must be a boolean", ErrInvalidServeConfig, key)
	}
	return value, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func environmentValue(lookupEnvironment EnvironmentLookup, key string, fallback string) string {
	if lookupEnvironment == nil {
		return fallback
	}
	value, ok := lookupEnvironment(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

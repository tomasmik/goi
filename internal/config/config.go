package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr              string
	DataDir                 string
	DatabasePath            string
	BackupDir               string
	BaseURL                 string
	LLMBaseURL              string
	LLMModel                string
	LLMAPIKey               string
	GoogleDriveClientID     string
	GoogleDriveClientSecret string
	SecureCookies           bool
	TimeZone                *time.Location
	TimeZoneName            string
	AuthEnabled             bool
	AuthUsername            string
	AuthPassword            string
	TrustProxy              bool
}

type StorageConfig struct {
	DataDir      string
	DatabasePath string
	BackupDir    string
}

func LoadStorage() (StorageConfig, error) {
	dataDir := envOr("APP_DATA_DIR", "/data")
	databasePath := envOr("APP_DATABASE_PATH", filepath.Join(dataDir, "vocab.sqlite"))
	backupDir := envOr("APP_BACKUP_DIR", filepath.Join(dataDir, "backups"))
	if err := validateStoragePaths(dataDir, databasePath, backupDir); err != nil {
		return StorageConfig{}, fmt.Errorf("validate storage paths: %w", err)
	}
	return StorageConfig{DataDir: dataDir, DatabasePath: databasePath, BackupDir: backupDir}, nil
}

func Load() (Config, error) {
	storage, err := LoadStorage()
	if err != nil {
		return Config{}, err
	}
	timeZoneName := envOr("APP_TIME_ZONE", "UTC")
	timeZone, err := time.LoadLocation(timeZoneName)
	if err != nil {
		return Config{}, fmt.Errorf("load app timezone %q: %w", timeZoneName, err)
	}

	authEnabledValue := envOr("APP_AUTH_MODE", "false")
	if authEnabledValue != "true" && authEnabledValue != "false" {
		return Config{}, errors.New("APP_AUTH_MODE must be true or false")
	}
	authEnabled := authEnabledValue == "true"
	authUsername := strings.TrimSpace(os.Getenv("APP_AUTH_USERNAME"))
	authPassword := os.Getenv("APP_AUTH_PASSWORD")
	if authEnabled && (authUsername == "" || authPassword == "") {
		return Config{}, errors.New("APP_AUTH_USERNAME and APP_AUTH_PASSWORD are required when APP_AUTH_MODE is true")
	}
	baseURL, secure, err := publicOrigin(envOr("APP_BASE_URL", "http://localhost:8080"))
	if err != nil {
		return Config{}, fmt.Errorf("parse APP_BASE_URL: %w", err)
	}

	trustProxy, err := strconv.ParseBool(envOr("APP_TRUST_PROXY", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("parse APP_TRUST_PROXY: %w", err)
	}

	llmBaseURL, llmModel, llmAPIKey, err := loadLLMConfig()
	if err != nil {
		return Config{}, err
	}
	googleClientID, googleClientSecret, err := loadGoogleDriveConfig()
	if err != nil {
		return Config{}, err
	}

	return Config{
		ListenAddr:              envOr("APP_LISTEN", "127.0.0.1:8080"),
		DataDir:                 storage.DataDir,
		DatabasePath:            storage.DatabasePath,
		BackupDir:               storage.BackupDir,
		BaseURL:                 baseURL,
		LLMBaseURL:              llmBaseURL,
		LLMModel:                llmModel,
		LLMAPIKey:               llmAPIKey,
		GoogleDriveClientID:     googleClientID,
		GoogleDriveClientSecret: googleClientSecret,
		SecureCookies:           secure,
		TimeZone:                timeZone,
		TimeZoneName:            timeZoneName,
		AuthEnabled:             authEnabled,
		AuthUsername:            authUsername,
		AuthPassword:            authPassword,
		TrustProxy:              trustProxy,
	}, nil
}

func loadGoogleDriveConfig() (string, string, error) {
	clientID := strings.TrimSpace(os.Getenv("APP_GOOGLE_DRIVE_CLIENT_ID"))
	clientSecret := os.Getenv("APP_GOOGLE_DRIVE_CLIENT_SECRET")
	if (clientID == "") != (clientSecret == "") {
		return "", "", errors.New("APP_GOOGLE_DRIVE_CLIENT_ID and APP_GOOGLE_DRIVE_CLIENT_SECRET must be set together")
	}
	return clientID, clientSecret, nil
}

func loadLLMConfig() (string, string, string, error) {
	baseURL := strings.TrimSpace(os.Getenv("APP_LLM_BASE_URL"))
	if baseURL == "" {
		return "", "", "", nil
	}

	model := strings.TrimSpace(os.Getenv("APP_LLM_MODEL"))
	if model == "" {
		return "", "", "", errors.New("APP_LLM_MODEL is required when APP_LLM_BASE_URL is set")
	}

	normalizedBaseURL, err := llmBaseEndpoint(baseURL)
	if err != nil {
		return "", "", "", fmt.Errorf("parse APP_LLM_BASE_URL: %w", err)
	}
	return normalizedBaseURL, model, os.Getenv("APP_LLM_API_KEY"), nil
}

func llmBaseEndpoint(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("must use http or https")
	}
	if parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", errors.New("must be an absolute URL without user information")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("must not contain a query or fragment")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("must use HTTPS unless the host is loopback")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func publicOrigin(value string) (string, bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false, err
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false, errors.New("must use http or https")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" || parsed.User != nil {
		return "", false, errors.New("must be an absolute URL without user information")
	}
	if strings.Contains(hostname, "%") {
		return "", false, errors.New("must not contain an IPv6 zone identifier")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false, errors.New("must be an origin without a path, query, or fragment")
	}
	port := parsed.Port()
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", false, errors.New("must contain a valid port")
		}
		port = strconv.Itoa(portNumber)
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			port = ""
		}
	}
	if address := net.ParseIP(hostname); address != nil {
		hostname = address.String()
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return scheme + "://" + host, scheme == "https", nil
}

func validateStoragePaths(dataDir, databasePath, backupDir string) error {
	databaseResolved, err := resolveStoragePath(databasePath)
	if err != nil {
		return fmt.Errorf("resolve database path: %w", err)
	}
	cachePath := filepath.Join(dataDir, "jmdict.sqlite")
	cacheResolved, err := resolveStoragePath(cachePath)
	if err != nil {
		return fmt.Errorf("resolve JMdict cache path: %w", err)
	}
	if databaseResolved == cacheResolved {
		return errors.New("database path must differ from the JMdict cache path")
	}
	if databaseInfo, databaseErr := os.Stat(databasePath); databaseErr == nil {
		if cacheInfo, cacheErr := os.Stat(cachePath); cacheErr == nil && os.SameFile(databaseInfo, cacheInfo) {
			return errors.New("database path must differ from the JMdict cache path")
		}
	}

	dataResolved, err := resolveStoragePath(dataDir)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	backupsResolved, err := resolveStoragePath(backupDir)
	if err != nil {
		return fmt.Errorf("resolve backup directory: %w", err)
	}
	if pathInside(backupsResolved, databaseResolved) {
		return errors.New("database path must be outside the managed backup directory")
	}
	for _, name := range []string{
		"pending-restore.goi-backup.zip",
		"restore-status.json",
		"restore-queue",
		"restore-queue.lock",
		"restore-receipt.json",
		"google-drive.json",
		"google-drive-client.json",
		"example-generation.json",
		"installation-id",
		"wanikani-token",
	} {
		reservedResolved, err := resolveStoragePath(filepath.Join(dataDir, name))
		if err != nil {
			return fmt.Errorf("resolve reserved data path %q: %w", name, err)
		}
		if databaseResolved == reservedResolved {
			return fmt.Errorf("database path must differ from reserved data file %q", name)
		}
	}
	if filepath.Dir(databaseResolved) == dataResolved {
		name := filepath.Base(databaseResolved)
		if strings.HasPrefix(name, "failed-restore-") && strings.HasSuffix(name, ".goi-backup.zip") {
			return errors.New("database path must not use a failed restore filename")
		}
	}

	importsResolved, err := resolveStoragePath(filepath.Join(dataDir, "imports"))
	if err != nil {
		return fmt.Errorf("resolve Anki staging path: %w", err)
	}
	if pathInside(importsResolved, databaseResolved) {
		return errors.New("database path must be outside the Anki staging directory")
	}
	return nil
}

func pathInside(directory, path string) bool {
	relative, err := filepath.Rel(directory, path)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func resolveStoragePath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	missingParts := make([]string, 0)
	currentPath := absolutePath
	for {
		resolvedPath, err := filepath.EvalSymlinks(currentPath)
		if err == nil {
			for index := len(missingParts) - 1; index >= 0; index-- {
				resolvedPath = filepath.Join(resolvedPath, missingParts[index])
			}
			return filepath.Clean(resolvedPath), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(currentPath)
		if parent == currentPath {
			return filepath.Clean(absolutePath), nil
		}
		missingParts = append(missingParts, filepath.Base(currentPath))
		currentPath = parent
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	defaultAppName                 = "Go Admin"
	defaultAppEnvironment          = "development"
	defaultAppURL                  = "http://localhost:8080"
	defaultAppBindHost             = "127.0.0.1"
	defaultAppPort                 = 8080
	defaultAppShutdownTimeout      = 45 * time.Second
	defaultDatabaseHost            = "127.0.0.1"
	defaultDatabasePort            = 3306
	defaultSessionCookieName       = "admin_session"
	defaultSessionLifetime         = 24 * time.Hour
	defaultSessionRememberLifetime = 30 * 24 * time.Hour
	defaultFincloudHTTPTimeout     = 30 * time.Second
)

type Config struct {
	App      AppConfig
	Database DatabaseConfig
	Session  SessionConfig
}

// RuntimeConfig contains configuration required by the long-running web
// application. Operational commands that do not use Fincloud load Config
// instead, so migrations and administrator bootstrap remain independent.
type RuntimeConfig struct {
	Config
	Fincloud FincloudConfig
}

type FincloudConfig struct {
	BaseURL            string
	Username           string
	Password           string
	LocationID         string
	RoleID             string
	HTTPTimeout        time.Duration
	InsecureSkipVerify bool
}

type AppConfig struct {
	Name              string
	Environment       string
	URL               string
	BindHost          string
	Port              int
	ShutdownTimeout   time.Duration
	AllowRegistration bool
}

func (c AppConfig) Address() string {
	return net.JoinHostPort(c.BindHost, strconv.Itoa(c.Port))
}

func (c AppConfig) IsDevelopment() bool {
	return c.Environment == "development"
}

type DatabaseConfig struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
}

type SessionConfig struct {
	CookieName       string
	Lifetime         time.Duration
	RememberLifetime time.Duration
	Secure           bool
}

type lookupEnv func(string) (string, bool)

func Load() (Config, error) {
	if err := loadDotEnv(); err != nil {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}

	return parse(os.LookupEnv)
}

func LoadRuntime() (RuntimeConfig, error) {
	if err := loadDotEnv(); err != nil {
		return RuntimeConfig{}, fmt.Errorf("load .env: %w", err)
	}

	return parseRuntime(os.LookupEnv)
}

func loadDotEnv() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func parseRuntime(lookup lookupEnv) (RuntimeConfig, error) {
	base, err := parse(lookup)
	if err != nil {
		return RuntimeConfig{}, err
	}
	fincloudConfig, err := parseFincloud(lookup)
	if err != nil {
		return RuntimeConfig{}, err
	}
	return RuntimeConfig{Config: base, Fincloud: fincloudConfig}, nil
}

func parseFincloud(lookup lookupEnv) (FincloudConfig, error) {
	value := func(key, fallback string) string {
		if result, ok := lookup(key); ok {
			return result
		}
		return fallback
	}

	timeout, err := parseDuration("FINCLOUD_HTTP_TIMEOUT", value("FINCLOUD_HTTP_TIMEOUT", defaultFincloudHTTPTimeout.String()))
	if err != nil {
		return FincloudConfig{}, err
	}
	insecureSkipVerify, err := parseBool("FINCLOUD_INSECURE_SKIP_VERIFY", value("FINCLOUD_INSECURE_SKIP_VERIFY", "false"))
	if err != nil {
		return FincloudConfig{}, err
	}

	config := FincloudConfig{
		BaseURL:            strings.TrimSpace(value("FINCLOUD_BASE_URL", "")),
		Username:           strings.TrimSpace(value("FINCLOUD_USERNAME", "")),
		Password:           value("FINCLOUD_PASSWORD", ""),
		LocationID:         strings.TrimSpace(value("FINCLOUD_LOCATION_ID", "")),
		RoleID:             strings.TrimSpace(value("FINCLOUD_ROLE_ID", "")),
		HTTPTimeout:        timeout,
		InsecureSkipVerify: insecureSkipVerify,
	}

	required := []struct {
		key   string
		value string
	}{
		{"FINCLOUD_BASE_URL", config.BaseURL},
		{"FINCLOUD_USERNAME", config.Username},
		{"FINCLOUD_PASSWORD", strings.TrimSpace(config.Password)},
		{"FINCLOUD_LOCATION_ID", config.LocationID},
		{"FINCLOUD_ROLE_ID", config.RoleID},
	}
	for _, field := range required {
		if field.value == "" {
			return FincloudConfig{}, fmt.Errorf("%s must not be empty", field.key)
		}
	}

	baseURL, err := url.Parse(config.BaseURL)
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return FincloudConfig{}, fmt.Errorf("FINCLOUD_BASE_URL must be an absolute https URL without credentials, query, or fragment")
	}

	return config, nil
}

func parse(lookup lookupEnv) (Config, error) {
	value := func(key, fallback string) string {
		if result, ok := lookup(key); ok {
			return result
		}
		return fallback
	}

	appPort, err := parsePort("APP_PORT", value("APP_PORT", strconv.Itoa(defaultAppPort)))
	if err != nil {
		return Config{}, err
	}
	databasePort, err := parsePort("DB_PORT", value("DB_PORT", strconv.Itoa(defaultDatabasePort)))
	if err != nil {
		return Config{}, err
	}
	allowRegistration, err := parseBool("ALLOW_REGISTRATION", value("ALLOW_REGISTRATION", "false"))
	if err != nil {
		return Config{}, err
	}
	sessionSecure, err := parseBool("SESSION_SECURE", value("SESSION_SECURE", "false"))
	if err != nil {
		return Config{}, err
	}
	sessionLifetime, err := parseDuration("SESSION_LIFETIME", value("SESSION_LIFETIME", defaultSessionLifetime.String()))
	if err != nil {
		return Config{}, err
	}
	rememberLifetime, err := parseDuration("SESSION_REMEMBER_LIFETIME", value("SESSION_REMEMBER_LIFETIME", defaultSessionRememberLifetime.String()))
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := parseDuration("APP_SHUTDOWN_TIMEOUT", value("APP_SHUTDOWN_TIMEOUT", defaultAppShutdownTimeout.String()))
	if err != nil {
		return Config{}, err
	}

	config := Config{
		App: AppConfig{
			Name:              strings.TrimSpace(value("APP_NAME", defaultAppName)),
			Environment:       strings.TrimSpace(value("APP_ENV", defaultAppEnvironment)),
			URL:               strings.TrimSpace(value("APP_URL", defaultAppURL)),
			BindHost:          strings.TrimSpace(value("APP_BIND_HOST", defaultAppBindHost)),
			Port:              appPort,
			ShutdownTimeout:   shutdownTimeout,
			AllowRegistration: allowRegistration,
		},
		Database: DatabaseConfig{
			Host:     strings.TrimSpace(value("DB_HOST", defaultDatabaseHost)),
			Port:     databasePort,
			Name:     strings.TrimSpace(value("DB_NAME", "")),
			User:     strings.TrimSpace(value("DB_USER", "")),
			Password: value("DB_PASSWORD", ""),
		},
		Session: SessionConfig{
			CookieName:       strings.TrimSpace(value("SESSION_COOKIE_NAME", defaultSessionCookieName)),
			Lifetime:         sessionLifetime,
			RememberLifetime: rememberLifetime,
			Secure:           sessionSecure,
		},
	}

	if err := validate(config); err != nil {
		return Config{}, err
	}

	return config, nil
}

func validate(config Config) error {
	required := []struct {
		key   string
		value string
	}{
		{"APP_NAME", config.App.Name},
		{"APP_ENV", config.App.Environment},
		{"APP_BIND_HOST", config.App.BindHost},
		{"DB_HOST", config.Database.Host},
		{"DB_NAME", config.Database.Name},
		{"DB_USER", config.Database.User},
		{"SESSION_COOKIE_NAME", config.Session.CookieName},
	}
	for _, field := range required {
		if field.value == "" {
			return fmt.Errorf("%s must not be empty", field.key)
		}
	}

	switch config.App.Environment {
	case "development", "production", "test":
	default:
		return fmt.Errorf("APP_ENV must be one of development, production, or test")
	}

	appURL, err := url.Parse(config.App.URL)
	if err != nil || appURL.Host == "" || (appURL.Scheme != "http" && appURL.Scheme != "https") {
		return fmt.Errorf("APP_URL must be an absolute http or https URL")
	}
	if config.App.Environment == "production" {
		bindIP := net.ParseIP(config.App.BindHost)
		if bindIP == nil || !bindIP.IsLoopback() {
			return fmt.Errorf("APP_BIND_HOST must be a loopback IP in production")
		}
		if appURL.Scheme != "https" {
			return fmt.Errorf("APP_URL must use https in production")
		}
		if config.App.AllowRegistration {
			return fmt.Errorf("ALLOW_REGISTRATION must be false in production")
		}
		if !config.Session.Secure {
			return fmt.Errorf("SESSION_SECURE must be true in production")
		}
		if config.Database.Password == "" {
			return fmt.Errorf("DB_PASSWORD must not be empty in production")
		}
	}

	return nil
}

func parsePort(key, value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be an integer between 1 and 65535", key)
	}
	return port, nil
}

func parseBool(key, value string) (bool, error) {
	result, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return result, nil
}

func parseDuration(key, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return duration, nil
}

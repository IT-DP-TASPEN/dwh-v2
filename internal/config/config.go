package config

import (
	"errors"
	"fmt"
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
	defaultAppPort                 = 8080
	defaultDatabaseHost            = "127.0.0.1"
	defaultDatabasePort            = 3306
	defaultSessionCookieName       = "admin_session"
	defaultSessionLifetime         = 24 * time.Hour
	defaultSessionRememberLifetime = 30 * 24 * time.Hour
)

type Config struct {
	App      AppConfig
	Database DatabaseConfig
	Session  SessionConfig
}

type AppConfig struct {
	Name              string
	Environment       string
	URL               string
	Port              int
	AllowRegistration bool
}

func (c AppConfig) Address() string {
	return ":" + strconv.Itoa(c.Port)
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
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}

	return parse(os.LookupEnv)
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

	config := Config{
		App: AppConfig{
			Name:              strings.TrimSpace(value("APP_NAME", defaultAppName)),
			Environment:       strings.TrimSpace(value("APP_ENV", defaultAppEnvironment)),
			URL:               strings.TrimSpace(value("APP_URL", defaultAppURL)),
			Port:              appPort,
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

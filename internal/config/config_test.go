package config

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	config, err := parse(mapLookup(map[string]string{
		"DB_NAME": "go_admin",
		"DB_USER": "root",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if config.App.Name != "Go Admin" || config.App.BindHost != "127.0.0.1" || config.App.Port != 8080 || config.App.ShutdownTimeout != 45*time.Second || !config.App.IsDevelopment() {
		t.Fatalf("unexpected app defaults: %+v", config.App)
	}
	if config.Database.Host != "127.0.0.1" || config.Database.Port != 3306 {
		t.Fatalf("unexpected database defaults: %+v", config.Database)
	}
	if config.Session.Lifetime != 24*time.Hour || config.Session.RememberLifetime != 30*24*time.Hour {
		t.Fatalf("unexpected session defaults: %+v", config.Session)
	}
}

func TestParseValidation(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		wantErr string
	}{
		{"missing database name", map[string]string{"DB_USER": "root"}, "DB_NAME"},
		{"missing database user", map[string]string{"DB_NAME": "go_admin"}, "DB_USER"},
		{"invalid app port", baseValues("APP_PORT", "70000"), "APP_PORT"},
		{"invalid database port", baseValues("DB_PORT", "mysql"), "DB_PORT"},
		{"invalid app url", baseValues("APP_URL", "localhost:8080"), "APP_URL"},
		{"invalid app environment", baseValues("APP_ENV", "staging"), "APP_ENV"},
		{"invalid registration flag", baseValues("ALLOW_REGISTRATION", "sometimes"), "ALLOW_REGISTRATION"},
		{"invalid secure flag", baseValues("SESSION_SECURE", "sometimes"), "SESSION_SECURE"},
		{"invalid session lifetime", baseValues("SESSION_LIFETIME", "0s"), "SESSION_LIFETIME"},
		{"invalid remember lifetime", baseValues("SESSION_REMEMBER_LIFETIME", "later"), "SESSION_REMEMBER_LIFETIME"},
		{"invalid shutdown timeout", baseValues("APP_SHUTDOWN_TIMEOUT", "0s"), "APP_SHUTDOWN_TIMEOUT"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parse(mapLookup(test.values))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestParseSupportedEnvironments(t *testing.T) {
	for _, environment := range []string{"development", "production", "test"} {
		t.Run(environment, func(t *testing.T) {
			values := baseValues("APP_ENV", environment)
			if environment == "production" {
				productionValues(values)
			}
			config, err := parse(mapLookup(values))
			if err != nil {
				t.Fatal(err)
			}
			if config.App.Environment != environment {
				t.Fatalf("expected %q, got %q", environment, config.App.Environment)
			}
		})
	}
}

func TestProductionValidation(t *testing.T) {
	valid := baseValues("APP_ENV", "production")
	productionValues(valid)
	if _, err := parse(mapLookup(valid)); err != nil {
		t.Fatalf("valid production configuration: %v", err)
	}
	for _, host := range []string{"127.0.0.1", "::1", "127.42.0.7"} {
		values := cloneValues(valid)
		values["APP_BIND_HOST"] = host
		if _, err := parse(mapLookup(values)); err != nil {
			t.Fatalf("loopback %q rejected: %v", host, err)
		}
	}
	tests := []struct {
		name, key, value, want string
	}{
		{"http URL", "APP_URL", "http://dwh.example.test", "https"},
		{"non-loopback bind", "APP_BIND_HOST", "0.0.0.0", "loopback"},
		{"hostname bind", "APP_BIND_HOST", "localhost", "loopback"},
		{"registration", "ALLOW_REGISTRATION", "true", "ALLOW_REGISTRATION"},
		{"insecure session", "SESSION_SECURE", "false", "SESSION_SECURE"},
		{"missing database password", "DB_PASSWORD", "", "DB_PASSWORD"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := cloneValues(valid)
			values[test.key] = test.value
			_, err := parse(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want %q", err, test.want)
			}
		})
	}
}

func TestParseRuntimeRequiresFincloudOnlyAtRuntimeBoundary(t *testing.T) {
	if _, err := parse(mapLookup(baseValues("", ""))); err != nil {
		t.Fatalf("core config unexpectedly requires Fincloud: %v", err)
	}

	_, err := parseRuntime(mapLookup(baseValues("", "")))
	if err == nil || !strings.Contains(err.Error(), "FINCLOUD_BASE_URL") {
		t.Fatalf("runtime config error = %v, want missing Fincloud base URL", err)
	}

	values := runtimeValues()
	got, err := parseRuntime(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if got.Fincloud.LocationID != "001" || got.Fincloud.RoleID != "role-1" || got.Fincloud.HTTPTimeout != 30*time.Second {
		t.Fatalf("unexpected Fincloud config: %+v", got.Fincloud)
	}
	if got.Reporting.InteractiveMaxRows != 10000 || got.Reporting.InteractivePayloadBytes != 8<<20 || got.Reporting.DynamicOptionMaxRows != 1000 || got.Reporting.DynamicOptionPayloadBytes != 1<<20 || got.Reporting.CellPreviewBytes != 16<<10 {
		t.Fatalf("unexpected reporting config: %+v", got.Reporting)
	}
}

func TestParseFincloudValidation(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{"http URL", "FINCLOUD_BASE_URL", "http://fincloud.test", "absolute https"},
		{"embedded credentials", "FINCLOUD_BASE_URL", "https://user:pass@fincloud.test", "without credentials"},
		{"query", "FINCLOUD_BASE_URL", "https://fincloud.test?token=x", "without credentials"},
		{"blank location", "FINCLOUD_LOCATION_ID", " ", "FINCLOUD_LOCATION_ID"},
		{"invalid timeout", "FINCLOUD_HTTP_TIMEOUT", "0s", "FINCLOUD_HTTP_TIMEOUT"},
		{"invalid insecure flag", "FINCLOUD_INSECURE_SKIP_VERIFY", "sometimes", "FINCLOUD_INSECURE_SKIP_VERIFY"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := runtimeValues()
			values[test.key] = test.value
			_, err := parseRuntime(mapLookup(values))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestParseReportingValidation(t *testing.T) {
	tests := []struct{ key, value, want string }{
		{"REPORT_DATASOURCE_MASTER_KEY", "short", "exactly 32 bytes"},
		{"REPORT_INTERACTIVE_PAYLOAD_BYTES", "0", "positive integer"},
		{"REPORT_DYNAMIC_OPTION_MAX_ROWS", "0", "positive integer"},
		{"REPORT_DYNAMIC_OPTION_PAYLOAD_BYTES", "100", "at least 4096"},
		{"REPORT_CELL_PREVIEW_BYTES", "many", "positive integer"},
		{"REPORT_EXPORT_HEARTBEAT_INTERVAL", "30s", "shorter"},
	}
	for _, test := range tests {
		values := runtimeValues()
		values[test.key] = test.value
		if _, err := parseRuntime(mapLookup(values)); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s error=%v want %q", test.key, err, test.want)
		}
	}
	values := runtimeValues()
	productionValues(values)
	values["APP_ENV"] = "production"
	delete(values, "REPORT_EXPORT_DIR")
	if _, err := parseRuntime(mapLookup(values)); err == nil || !strings.Contains(err.Error(), "REPORT_EXPORT_DIR") {
		t.Fatalf("production export directory error=%v", err)
	}
}

func TestLoadEnvironmentOverridesDotEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("DB_NAME=from_file\nDB_USER=file_user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_NAME", "from_environment")
	t.Setenv("DB_USER", "environment_user")

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.Database.Name != "from_environment" || config.Database.User != "environment_user" {
		t.Fatalf("environment did not win: %+v", config.Database)
	}
}

func baseValues(key, value string) map[string]string {
	values := map[string]string{"DB_NAME": "go_admin", "DB_USER": "root"}
	if key != "" {
		values[key] = value
	}
	return values
}

func runtimeValues() map[string]string {
	values := baseValues("", "")
	values["FINCLOUD_BASE_URL"] = "https://fincloud.test/base"
	values["FINCLOUD_USERNAME"] = "user"
	values["FINCLOUD_PASSWORD"] = "secret"
	values["FINCLOUD_LOCATION_ID"] = "001"
	values["FINCLOUD_ROLE_ID"] = "role-1"
	values["REPORT_DATASOURCE_MASTER_KEY"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	return values
}

func productionValues(values map[string]string) {
	values["APP_URL"] = "https://dwh.example.test"
	values["APP_BIND_HOST"] = "127.0.0.1"
	values["ALLOW_REGISTRATION"] = "false"
	values["SESSION_SECURE"] = "true"
	values["DB_PASSWORD"] = "secret"
}

func cloneValues(values map[string]string) map[string]string {
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func mapLookup(values map[string]string) lookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

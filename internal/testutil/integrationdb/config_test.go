//go:build integration

package integrationdb

import "testing"

func TestMatchesRuntimeDatabase(t *testing.T) {
	values := map[string]string{"TEST_DB_HOST": "127.0.0.1", "TEST_DB_PORT": "3306", "TEST_DB_NAME": "dwh"}
	for _, test := range []struct {
		name    string
		runtime map[string]string
		want    bool
	}{
		{"legacy TCP match", map[string]string{"DB_HOST": "127.0.0.1", "DB_PORT": "3306", "DB_NAME": "dwh"}, true},
		{"explicit TCP match", map[string]string{"DB_NETWORK": "tcp", "DB_HOST": "127.0.0.1", "DB_PORT": "3306", "DB_NAME": "dwh"}, true},
		{"isolated TCP port", map[string]string{"DB_NETWORK": "tcp", "DB_HOST": "127.0.0.1", "DB_PORT": "3307", "DB_NAME": "dwh"}, false},
		{"isolated TCP host", map[string]string{"DB_NETWORK": "tcp", "DB_HOST": "mysql.example.test", "DB_PORT": "3306", "DB_NAME": "dwh"}, false},
		{"isolated TCP schema", map[string]string{"DB_NETWORK": "tcp", "DB_HOST": "127.0.0.1", "DB_PORT": "3306", "DB_NAME": "production_dwh"}, false},
		{"Unix without TCP fields", map[string]string{"DB_NETWORK": "unix", "DB_NAME": "dwh"}, true},
		{"Unix stale TCP fields", map[string]string{"DB_NETWORK": "unix", "DB_HOST": "ignored.example.test", "DB_PORT": "3307", "DB_NAME": "dwh"}, true},
		{"Unix trimmed values", map[string]string{"DB_NETWORK": " unix ", "DB_NAME": " dwh "}, true},
		{"isolated Unix schema", map[string]string{"DB_NETWORK": "unix", "DB_NAME": "production_dwh"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := matchesRuntimeDatabase(values, test.runtime); got != test.want {
				t.Fatalf("matchesRuntimeDatabase=%v want %v", got, test.want)
			}
		})
	}
}

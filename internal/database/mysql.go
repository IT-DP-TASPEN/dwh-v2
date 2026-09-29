package database

import (
	"context"
	"fmt"
	"net"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/config"
)

func Open(ctx context.Context, config config.DatabaseConfig) (*sqlx.DB, error) {
	return open(ctx, config, false)
}

func OpenMigrations(ctx context.Context, config config.DatabaseConfig) (*sqlx.DB, error) {
	return open(ctx, config, true)
}

func open(ctx context.Context, databaseConfig config.DatabaseConfig, migrations bool) (*sqlx.DB, error) {
	driverConfig, err := mysqlConfig(databaseConfig, migrations)
	if err != nil {
		return nil, fmt.Errorf("configure mysql: %w", err)
	}

	database, err := sqlx.Open("mysql", driverConfig.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	database.SetMaxOpenConns(25)
	database.SetMaxIdleConns(5)
	database.SetConnMaxLifetime(5 * time.Minute)
	database.SetConnMaxIdleTime(time.Minute)

	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("ping mysql at %s: %w", driverConfig.Addr, err)
	}

	return database, nil
}

func mysqlConfig(databaseConfig config.DatabaseConfig, migrations bool) (*mysql.Config, error) {
	driverConfig, err := MySQLConfig(databaseConfig)
	if err != nil {
		return nil, err
	}
	driverConfig.Collation = "utf8mb4_unicode_ci"
	driverConfig.Params = map[string]string{"time_zone": "'+00:00'"}
	driverConfig.MultiStatements = migrations
	driverConfig.MaxAllowedPacket = 0
	return driverConfig, nil
}

// MySQLConfig builds the shared transport and credential settings. Callers retain
// ownership of TLS, session options, timeouts, and connection pool lifecycles.
func MySQLConfig(databaseConfig config.DatabaseConfig) (*mysql.Config, error) {
	driverConfig := mysql.NewConfig()
	driverConfig.User = databaseConfig.User
	driverConfig.Passwd = databaseConfig.Password
	switch databaseConfig.Network {
	case "", "tcp": // Preserve callers that construct DatabaseConfig without Network.
		driverConfig.Net = "tcp"
		driverConfig.Addr = net.JoinHostPort(databaseConfig.Host, fmt.Sprintf("%d", databaseConfig.Port))
	case "unix":
		if databaseConfig.Socket == "" {
			return nil, fmt.Errorf("DB_SOCKET must not be empty when DB_NETWORK=unix")
		}
		driverConfig.Net = "unix"
		driverConfig.Addr = databaseConfig.Socket
	default:
		return nil, fmt.Errorf("DB_NETWORK must be one of tcp or unix")
	}
	driverConfig.DBName = databaseConfig.Name
	driverConfig.ParseTime = true
	driverConfig.Loc = time.UTC

	return driverConfig, nil
}

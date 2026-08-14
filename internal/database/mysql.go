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
	driverConfig := mysql.NewConfig()
	driverConfig.User = databaseConfig.User
	driverConfig.Passwd = databaseConfig.Password
	driverConfig.Net = "tcp"
	driverConfig.Addr = net.JoinHostPort(databaseConfig.Host, fmt.Sprintf("%d", databaseConfig.Port))
	driverConfig.DBName = databaseConfig.Name
	driverConfig.Collation = "utf8mb4_unicode_ci"
	driverConfig.ParseTime = true
	driverConfig.Loc = time.UTC
	driverConfig.Params = map[string]string{"time_zone": "'+00:00'"}
	driverConfig.MultiStatements = migrations

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

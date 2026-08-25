package reporting

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

type PoolConfig struct {
	ConnectTimeout      time.Duration
	MySQLMaxPacketBytes int
	MaxOpen             int
	MaxIdle             int
	MaxLifetime         time.Duration
	MaxIdleTime         time.Duration
}

type poolEntry struct {
	mu       sync.Mutex
	revision uint64
	database *sql.DB
}

type PoolManager struct {
	cipher  *Cipher
	config  PoolConfig
	mu      sync.Mutex
	entries map[uint64]*poolEntry
}

func NewPoolManager(cipher *Cipher, config PoolConfig) (*PoolManager, error) {
	if cipher == nil || config.ConnectTimeout <= 0 || config.MySQLMaxPacketBytes <= 0 {
		return nil, fmt.Errorf("report pool cipher and bounds are required")
	}
	if config.MaxOpen == 0 {
		config.MaxOpen = 10
	}
	if config.MaxIdle == 0 {
		config.MaxIdle = 2
	}
	if config.MaxLifetime == 0 {
		config.MaxLifetime = 5 * time.Minute
	}
	if config.MaxIdleTime == 0 {
		config.MaxIdleTime = time.Minute
	}
	return &PoolManager{cipher: cipher, config: config, entries: make(map[uint64]*poolEntry)}, nil
}

func (manager *PoolManager) Database(ctx context.Context, datasource Datasource, allowDisabled bool) (*sql.DB, error) {
	if datasource.Status != StatusActive && !(allowDisabled && datasource.Status == StatusDisabled) {
		return nil, ErrInactive
	}
	entry := manager.entry(datasource.ID)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.database != nil && entry.revision == datasource.Revision {
		return entry.database, nil
	}
	password, err := manager.cipher.Decrypt(datasource.ID, datasource.PasswordCiphertext)
	if err != nil {
		return nil, err
	}
	config := mysql.NewConfig()
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(datasource.Host, strconv.Itoa(int(datasource.Port)))
	config.User = datasource.Username
	config.Passwd = password
	config.DBName = datasource.DatabaseName
	config.ParseTime = true
	config.Loc = time.UTC
	config.Timeout = manager.config.ConnectTimeout
	config.MaxAllowedPacket = manager.config.MySQLMaxPacketBytes
	config.MultiStatements = false
	if datasource.TLSPolicy == TLSRequired {
		config.TLSConfig = "true"
	}
	database, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open report datasource: %w", err)
	}
	database.SetMaxOpenConns(manager.config.MaxOpen)
	database.SetMaxIdleConns(manager.config.MaxIdle)
	database.SetConnMaxLifetime(manager.config.MaxLifetime)
	database.SetConnMaxIdleTime(manager.config.MaxIdleTime)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect report datasource: %w", err)
	}
	old := entry.database
	entry.database, entry.revision = database, datasource.Revision
	if old != nil {
		go old.Close()
	}
	return database, nil
}

func (manager *PoolManager) Invalidate(id uint64) {
	entry := manager.entry(id)
	entry.mu.Lock()
	old := entry.database
	entry.database, entry.revision = nil, 0
	entry.mu.Unlock()
	if old != nil {
		go old.Close()
	}
}

func (manager *PoolManager) Close() error {
	manager.mu.Lock()
	entries := make([]*poolEntry, 0, len(manager.entries))
	for _, entry := range manager.entries {
		entries = append(entries, entry)
	}
	manager.mu.Unlock()
	var first error
	for _, entry := range entries {
		entry.mu.Lock()
		database := entry.database
		entry.database = nil
		entry.mu.Unlock()
		if database != nil {
			if err := database.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (manager *PoolManager) entry(id uint64) *poolEntry {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.entries[id] == nil {
		manager.entries[id] = &poolEntry{}
	}
	return manager.entries[id]
}

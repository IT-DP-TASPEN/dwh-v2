package auth

import "time"

type Session struct {
	ID                 uint64
	UserID             uint64
	ImpersonatedUserID *uint64
	TokenHash          [32]byte
	RememberMe         bool
	ExpiresAt          time.Time
	LastSeenAt         time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type CreateSessionParams struct {
	UserID     uint64
	TokenHash  [32]byte
	RememberMe bool
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

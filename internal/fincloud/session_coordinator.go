package fincloud

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrSessionCoordinatorClosed = errors.New("Fincloud session coordinator is closed")

type AuthContext struct {
	ProfileID   uint64
	Revision    uint64
	ProfileName string
	Username    string
	Password    string
	RoleID      string
	LocationID  string
}

func (auth AuthContext) Validate() error {
	if auth.ProfileID == 0 || auth.Revision == 0 || auth.ProfileName == "" || auth.Password == "" ||
		invalidAuthIdentifier(auth.Username) || invalidAuthIdentifier(auth.RoleID) || invalidAuthIdentifier(auth.LocationID) {
		return fmt.Errorf("invalid Fincloud auth context")
	}
	return nil
}

type SessionCoordinatorConfig struct {
	BaseURL            string
	HTTPTimeout        time.Duration
	InsecureSkipVerify bool
}

type Lease interface {
	Client() *Client
	Release()
}

type SessionCoordinator struct {
	config SessionCoordinatorConfig
	mu     sync.Mutex
	lanes  map[string]*sessionLane
	closed bool
}

func NewSessionCoordinator(config SessionCoordinatorConfig) (*SessionCoordinator, error) {
	probe, err := NewClient(Config{BaseURL: config.BaseURL, Username: "validation", Password: "validation", LocationID: "validation", RoleID: "validation",
		HTTPTimeout: config.HTTPTimeout, InsecureSkipVerify: config.InsecureSkipVerify})
	if err != nil {
		return nil, err
	}
	probe.CloseIdleConnections()
	return &SessionCoordinator{config: config, lanes: map[string]*sessionLane{}}, nil
}

func (coordinator *SessionCoordinator) Acquire(ctx context.Context, auth AuthContext) (Lease, error) {
	return coordinator.acquire(ctx, auth, false)
}

func (coordinator *SessionCoordinator) Test(ctx context.Context, auth AuthContext) error {
	lease, err := coordinator.acquire(ctx, auth, true)
	if err != nil {
		return err
	}
	sessionLease := lease.(*coordinatedLease)
	err = sessionLease.client.Authenticate(ctx)
	if err != nil {
		sessionLease.lane.invalidate(sessionLease.client)
	}
	lease.Release()
	return err
}

func (coordinator *SessionCoordinator) acquire(ctx context.Context, auth AuthContext, exclusive bool) (Lease, error) {
	if err := auth.Validate(); err != nil {
		return nil, err
	}
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return nil, ErrSessionCoordinatorClosed
	}
	lane := coordinator.lanes[auth.Username]
	if lane == nil {
		lane = &sessionLane{coordinator: coordinator}
		coordinator.lanes[auth.Username] = lane
	}
	coordinator.mu.Unlock()
	return lane.acquire(ctx, auth, exclusive)
}

func (coordinator *SessionCoordinator) newClient(auth AuthContext) (*Client, error) {
	return NewClient(Config{BaseURL: coordinator.config.BaseURL, Username: auth.Username, Password: auth.Password,
		LocationID: auth.LocationID, RoleID: auth.RoleID, HTTPTimeout: coordinator.config.HTTPTimeout,
		InsecureSkipVerify: coordinator.config.InsecureSkipVerify})
}

func (coordinator *SessionCoordinator) Close() {
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return
	}
	coordinator.closed = true
	lanes := make([]*sessionLane, 0, len(coordinator.lanes))
	for _, lane := range coordinator.lanes {
		lanes = append(lanes, lane)
	}
	coordinator.mu.Unlock()
	for _, lane := range lanes {
		lane.close()
	}
}

type sessionEpoch struct {
	auth      AuthContext
	client    *Client
	exclusive bool
}

type sessionGrant struct {
	lease Lease
	err   error
}

type sessionWaiter struct {
	auth      AuthContext
	exclusive bool
	done      chan sessionGrant
}

type sessionLane struct {
	coordinator *SessionCoordinator
	mu          sync.Mutex
	current     *sessionEpoch
	active      int
	waiters     []*sessionWaiter
	closed      bool
}

func (lane *sessionLane) acquire(ctx context.Context, auth AuthContext, exclusive bool) (Lease, error) {
	waiter := &sessionWaiter{auth: auth, exclusive: exclusive, done: make(chan sessionGrant, 1)}
	lane.mu.Lock()
	if lane.closed {
		lane.mu.Unlock()
		return nil, ErrSessionCoordinatorClosed
	}
	lane.waiters = append(lane.waiters, waiter)
	lane.grantLocked()
	lane.mu.Unlock()

	select {
	case result := <-waiter.done:
		return result.lease, result.err
	case <-ctx.Done():
		if lane.cancel(waiter) {
			return nil, ctx.Err()
		}
		result := <-waiter.done
		if result.lease != nil {
			result.lease.Release()
		}
		return nil, ctx.Err()
	}
}

func (lane *sessionLane) cancel(target *sessionWaiter) bool {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	for index, waiter := range lane.waiters {
		if waiter == target {
			lane.waiters = append(lane.waiters[:index], lane.waiters[index+1:]...)
			lane.grantLocked()
			return true
		}
	}
	return false
}

func (lane *sessionLane) grantLocked() {
	for len(lane.waiters) != 0 {
		if lane.closed {
			waiter := lane.popLocked()
			waiter.done <- sessionGrant{err: ErrSessionCoordinatorClosed}
			continue
		}
		if lane.active == 0 {
			waiter := lane.waiters[0]
			var client *Client
			if lane.current != nil && !waiter.exclusive && sameSessionContext(lane.current.auth, waiter.auth) {
				client = lane.current.client
			} else {
				var err error
				client, err = lane.coordinator.newClient(waiter.auth)
				if err != nil {
					lane.popLocked().done <- sessionGrant{err: err}
					continue
				}
				if lane.current != nil {
					lane.current.client.CloseIdleConnections()
				}
			}
			lane.current = &sessionEpoch{auth: waiter.auth, client: client, exclusive: waiter.exclusive}
			lane.active = 1
			lane.popLocked().done <- sessionGrant{lease: &coordinatedLease{lane: lane, client: client}}
			if waiter.exclusive {
				return
			}
		}
		if len(lane.waiters) == 0 {
			return
		}
		if lane.current == nil || lane.current.exclusive {
			return
		}
		waiter := lane.waiters[0]
		if waiter.exclusive || !sameSessionContext(lane.current.auth, waiter.auth) {
			return
		}
		lane.active++
		lane.popLocked().done <- sessionGrant{lease: &coordinatedLease{lane: lane, client: lane.current.client}}
	}
}

func (lane *sessionLane) popLocked() *sessionWaiter {
	waiter := lane.waiters[0]
	lane.waiters = lane.waiters[1:]
	return waiter
}

func (lane *sessionLane) release(client *Client) {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.active > 0 {
		lane.active--
	}
	if lane.active == 0 && lane.current != nil {
		lane.current.exclusive = false
		if lane.closed {
			lane.current.client.CloseIdleConnections()
			lane.current = nil
		}
	}
	lane.grantLocked()
}

func (lane *sessionLane) invalidate(client *Client) {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.current != nil && lane.current.client == client {
		lane.current.client.CloseIdleConnections()
		lane.current = nil
	}
}

func (lane *sessionLane) close() {
	lane.mu.Lock()
	lane.closed = true
	lane.grantLocked()
	if lane.active == 0 && lane.current != nil {
		lane.current.client.CloseIdleConnections()
		lane.current = nil
	}
	lane.mu.Unlock()
}

func sameSessionContext(left, right AuthContext) bool {
	return left.Username == right.Username && left.Password == right.Password && left.RoleID == right.RoleID && left.LocationID == right.LocationID
}

type coordinatedLease struct {
	lane   *sessionLane
	client *Client
	once   sync.Once
}

func (lease *coordinatedLease) Client() *Client { return lease.client }
func (lease *coordinatedLease) Release() {
	lease.once.Do(func() { lease.lane.release(lease.client) })
}

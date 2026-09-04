package fincloud

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionCoordinatorSharesExactContextAndSerializesSwitches(t *testing.T) {
	server := loginServer(t, nil)
	coordinator := testSessionCoordinator(t, server)
	auth := testAuth("User")
	first, err := coordinator.Acquire(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.Acquire(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if first.Client() != second.Client() {
		t.Fatal("compatible leases did not share a client")
	}

	switched := auth
	switched.RoleID = "other"
	granted := make(chan Lease, 1)
	go func() {
		lease, _ := coordinator.Acquire(context.Background(), switched)
		granted <- lease
	}()
	select {
	case <-granted:
		t.Fatal("context switch granted while compatible leases were active")
	case <-time.After(30 * time.Millisecond):
	}
	first.Release()
	second.Release()
	select {
	case lease := <-granted:
		lease.Release()
	case <-time.After(time.Second):
		t.Fatal("context switch was not granted")
	}
}

func TestSessionCoordinatorUsernameIsExactLaneKey(t *testing.T) {
	server := loginServer(t, nil)
	coordinator := testSessionCoordinator(t, server)
	upper, err := coordinator.Acquire(context.Background(), testAuth("User"))
	if err != nil {
		t.Fatal(err)
	}
	lowerAuth := testAuth("user")
	lowerAuth.RoleID = "other"
	lower, err := coordinator.Acquire(context.Background(), lowerAuth)
	if err != nil {
		t.Fatal(err)
	}
	upper.Release()
	lower.Release()
}

func TestSessionCoordinatorFIFOWithoutCompatibleBarging(t *testing.T) {
	server := loginServer(t, nil)
	coordinator := testSessionCoordinator(t, server)
	currentAuth := testAuth("User")
	current, err := coordinator.Acquire(context.Background(), currentAuth)
	if err != nil {
		t.Fatal(err)
	}
	switchedAuth := currentAuth
	switchedAuth.RoleID = "next"
	switched := make(chan Lease, 1)
	compatible := make(chan Lease, 1)
	go func() { lease, _ := coordinator.Acquire(context.Background(), switchedAuth); switched <- lease }()
	time.Sleep(20 * time.Millisecond)
	go func() { lease, _ := coordinator.Acquire(context.Background(), currentAuth); compatible <- lease }()
	current.Release()
	var next Lease
	select {
	case next = <-switched:
	case <-compatible:
		t.Fatal("compatible waiter barged ahead of context switch")
	case <-time.After(time.Second):
		t.Fatal("first waiter was not granted")
	}
	select {
	case <-compatible:
		t.Fatal("incompatible epochs overlapped")
	case <-time.After(30 * time.Millisecond):
	}
	next.Release()
	select {
	case lease := <-compatible:
		lease.Release()
	case <-time.After(time.Second):
		t.Fatal("compatible waiter was not eventually granted")
	}
}

func TestSessionCoordinatorCancellationShutdownAndIdempotentRelease(t *testing.T) {
	server := loginServer(t, nil)
	coordinator := testSessionCoordinator(t, server)
	auth := testAuth("User")
	holder, err := coordinator.Acquire(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	switched := auth
	switched.Password = "other"
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() { _, acquireErr := coordinator.Acquire(ctx, switched); cancelled <- acquireErr }()
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire error=%v", err)
	}
	waiting := make(chan error, 1)
	go func() { _, acquireErr := coordinator.Acquire(context.Background(), switched); waiting <- acquireErr }()
	time.Sleep(20 * time.Millisecond)
	coordinator.Close()
	if err := <-waiting; !errors.Is(err, ErrSessionCoordinatorClosed) {
		t.Fatalf("shutdown acquire error=%v", err)
	}
	holder.Release()
	holder.Release()
	if _, err := coordinator.Acquire(context.Background(), auth); !errors.Is(err, ErrSessionCoordinatorClosed) {
		t.Fatalf("post-close acquire error=%v", err)
	}
}

func TestConnectionUsesOnlyFreshLogin(t *testing.T) {
	var requests atomic.Int32
	server := loginServer(t, func(form url.Values) {
		requests.Add(1)
		if form.Get("username") != "CaseSensitive" || form.Get("roleid") != "Role" || form.Get("locationid") != "Location" || form.Get("pwd") != " secret " {
			t.Errorf("unexpected login form: %v", form)
		}
	})
	coordinator := testSessionCoordinator(t, server)
	auth := testAuth("CaseSensitive")
	auth.RoleID, auth.LocationID, auth.Password = "Role", "Location", " secret "
	active, err := coordinator.Acquire(context.Background(), auth)
	if err != nil {
		t.Fatal(err)
	}
	tested := make(chan error, 1)
	go func() { tested <- coordinator.Test(context.Background(), auth) }()
	select {
	case <-tested:
		t.Fatal("connection test did not wait for the active lane")
	case <-time.After(30 * time.Millisecond):
	}
	active.Release()
	select {
	case err := <-tested:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("connection test was not granted")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestAuthContextRejectsBoundaryWhitespace(t *testing.T) {
	for _, mutate := range []func(*AuthContext){
		func(auth *AuthContext) { auth.Username = " user" },
		func(auth *AuthContext) { auth.RoleID = "role " },
		func(auth *AuthContext) { auth.LocationID = " location " },
	} {
		auth := testAuth("user")
		mutate(&auth)
		if err := auth.Validate(); err == nil {
			t.Fatalf("invalid auth accepted: %+v", auth)
		}
	}
}

func loginServer(t *testing.T, inspect func(url.Values)) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/admin/access/login" {
			t.Errorf("unexpected endpoint %s", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		if inspect != nil {
			inspect(request.Form)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok","data":{"result":{"sessionid":"session"}}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func testSessionCoordinator(t *testing.T, server *httptest.Server) *SessionCoordinator {
	t.Helper()
	coordinator, err := NewSessionCoordinator(SessionCoordinatorConfig{BaseURL: server.URL, HTTPTimeout: time.Second, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(coordinator.Close)
	return coordinator
}

func testAuth(username string) AuthContext {
	return AuthContext{ProfileID: 1, Revision: 1, ProfileName: "profile", Username: username, Password: "secret", RoleID: "role", LocationID: "location"}
}

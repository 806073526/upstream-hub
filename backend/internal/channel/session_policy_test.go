package channel

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/worryzyy/upstream-hub/internal/connector"
	appcrypto "github.com/worryzyy/upstream-hub/internal/crypto"
	"github.com/worryzyy/upstream-hub/internal/storage"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCooldownActiveReturnsTheStoredNextAttempt(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	next := now.Add(30 * time.Minute)
	state := &storage.MonitorState{NextAttemptAt: &next, LastError: "status 522"}

	err, active := cooldownActive(state, now)
	if !active {
		t.Fatal("cooldown should be active")
	}
	if err == nil || !err.Until.Equal(next) {
		t.Fatalf("cooldown error = %#v, want until %v", err, next)
	}
}

func TestAuthCheckDueUsesFiveMinuteCache(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	checked := now.Add(-4 * time.Minute)
	state := &storage.MonitorState{LastCheckedAt: &checked}
	if authCheckDue(state, now, 5*time.Minute) {
		t.Fatal("auth check should still be cached")
	}

	checked = now.Add(-6 * time.Minute)
	state.LastCheckedAt = &checked
	if !authCheckDue(state, now, 5*time.Minute) {
		t.Fatal("auth check cache should have expired")
	}
}

func TestManualEnsureSessionRechecksCachedSessionAndLogsInWhenExpired(t *testing.T) {
	svc, mock, cipher := newSessionPolicyService(t)
	now := time.Now()
	nextAttempt := now.Add(time.Hour)
	oldExpires := now.Add(time.Hour)
	oldToken, err := cipher.Encrypt("old-token")
	if err != nil {
		t.Fatal(err)
	}
	oldCookie, err := cipher.Encrypt("old-cookie")
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery(`SELECT \* FROM "channel_monitor_states"`).
		WillReturnRows(sqlmock.NewRows([]string{
			"channel_id", "failure_count", "next_attempt_at", "last_failure_type", "last_error", "last_checked_at", "updated_at",
		}).AddRow(1, 2, nextAttempt, "auth", "status 401", now, now))
	mock.ExpectQuery(`SELECT \* FROM "auth_sessions"`).
		WillReturnRows(sqlmock.NewRows([]string{
			"channel_id", "user_id", "access_token_cipher", "cookie_cipher", "csrf_token_cipher", "expires_at", "last_login_at", "updated_at",
		}).AddRow(1, "1", oldToken, oldCookie, "", oldExpires, now, now))
	mock.ExpectQuery(`INSERT INTO "monitor_logs"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT \* FROM "auth_sessions"`).
		WillReturnRows(sqlmock.NewRows([]string{
			"channel_id", "user_id", "access_token_cipher", "cookie_cipher", "csrf_token_cipher", "expires_at", "last_login_at", "updated_at",
		}).AddRow(1, "1", oldToken, oldCookie, "", oldExpires, now, now))
	mock.ExpectExec(`UPDATE "auth_sessions"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT \* FROM "channel_monitor_states"`).
		WillReturnRows(sqlmock.NewRows([]string{
			"channel_id", "failure_count", "next_attempt_at", "last_failure_type", "last_error", "last_checked_at", "updated_at",
		}).AddRow(1, 2, nextAttempt, "auth", "status 401", now, now))
	mock.ExpectExec(`UPDATE "channel_monitor_states"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "channels"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "channels"`).WillReturnResult(sqlmock.NewResult(0, 1))

	conn := &expiredSessionConnector{}
	channel := &storage.Channel{ID: 1, CredentialMode: storage.CredentialModePassword}
	resolved := &connector.Channel{ID: 1}
	session, err := svc.ensureSession(context.Background(), channel, resolved, conn, true)
	if err != nil {
		t.Fatalf("manual ensure session: %v", err)
	}
	if session.AccessToken != "fresh-token" {
		t.Errorf("manual ensure session returned token %q, want fresh-token", session.AccessToken)
	}
	if conn.checkCalls != 1 {
		t.Errorf("CheckAuth calls = %d, want 1", conn.checkCalls)
	}
	if conn.loginCalls != 1 {
		t.Errorf("Login calls = %d, want 1", conn.loginCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestRecoverSessionReusesSessionAlreadyRecoveredByAnotherRequest(t *testing.T) {
	svc, mock, cipher := newSessionPolicyService(t)
	now := time.Now()
	expires := now.Add(time.Hour)
	freshToken, err := cipher.Encrypt("fresh-token")
	if err != nil {
		t.Fatal(err)
	}
	freshCookie, err := cipher.Encrypt("fresh-cookie")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT \* FROM "auth_sessions"`).
		WillReturnRows(sqlmock.NewRows([]string{
			"channel_id", "user_id", "access_token_cipher", "cookie_cipher", "csrf_token_cipher", "expires_at", "last_login_at", "updated_at",
		}).AddRow(1, "1", freshToken, freshCookie, "", expires, now, now))

	conn := &expiredSessionConnector{}
	c := &storage.Channel{ID: 1, CredentialMode: storage.CredentialModePassword}
	resolved := &connector.Channel{ID: 1}
	stale := &connector.AuthSession{UserID: "1", AccessToken: "old-token", Cookie: "old-cookie", ExpiresAt: expires}

	session, err := svc.RecoverSession(context.Background(), c, resolved, conn, stale)
	if err != nil {
		t.Fatalf("RecoverSession: %v", err)
	}
	if session.AccessToken != "fresh-token" {
		t.Errorf("recovered token = %q, want fresh-token", session.AccessToken)
	}
	if conn.loginCalls != 0 {
		t.Errorf("Login calls = %d, want 0", conn.loginCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func newSessionPolicyService(t *testing.T) (*Service, sqlmock.Sqlmock, *appcrypto.Cipher) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	mock.MatchExpectationsInOrder(false)
	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB, PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent), SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := appcrypto.NewCipher("session-policy-test")
	if err != nil {
		t.Fatal(err)
	}
	return NewService(
		storage.NewChannels(db),
		storage.NewAuthSessions(db),
		storage.NewCaptchas(db),
		storage.NewMonitorLogs(db),
		storage.NewMonitorStates(db),
		cipher,
		SessionPolicy{AuthCheckCache: 5 * time.Minute},
	), mock, cipher
}

type expiredSessionConnector struct {
	checkCalls int
	loginCalls int
}

func (c *expiredSessionConnector) GetTurnstileSiteKey(context.Context, *connector.Channel) (string, error) {
	return "", nil
}

func (c *expiredSessionConnector) Login(context.Context, *connector.Channel) (*connector.AuthSession, error) {
	c.loginCalls++
	return &connector.AuthSession{
		UserID: "1", AccessToken: "fresh-token", Cookie: "fresh-cookie", ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func (c *expiredSessionConnector) CheckAuth(context.Context, *connector.Channel, *connector.AuthSession) error {
	c.checkCalls++
	return connector.HTTPStatusError(401, nil)
}

func (c *expiredSessionConnector) GetBalance(context.Context, *connector.Channel, *connector.AuthSession) (*connector.BalanceResult, error) {
	panic("unexpected GetBalance call")
}

func (c *expiredSessionConnector) GetRates(context.Context, *connector.Channel, *connector.AuthSession) ([]connector.RateResult, error) {
	panic("unexpected GetRates call")
}

func (c *expiredSessionConnector) GetUsage(context.Context, *connector.Channel, *connector.AuthSession, connector.UsageQuery) (*connector.UsageResult, error) {
	panic("unexpected GetUsage call")
}

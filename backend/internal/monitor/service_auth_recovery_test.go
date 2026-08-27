package monitor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/worryzyy/upstream-hub/internal/channel"
	"github.com/worryzyy/upstream-hub/internal/connector"
	appcrypto "github.com/worryzyy/upstream-hub/internal/crypto"
	"github.com/worryzyy/upstream-hub/internal/notify"
	"github.com/worryzyy/upstream-hub/internal/storage"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const authRecoveryConnectorType connector.ChannelType = "auth-recovery-test"

func TestRefreshBalanceRecoversExpiredSessionAndRetriesOnce(t *testing.T) {
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
	cipher, err := appcrypto.NewCipher("auth-recovery-test")
	if err != nil {
		t.Fatal(err)
	}
	passwordCipher, err := cipher.Encrypt("password")
	if err != nil {
		t.Fatal(err)
	}
	oldTokenCipher, err := cipher.Encrypt("old-token")
	if err != nil {
		t.Fatal(err)
	}
	oldCookieCipher, err := cipher.Encrypt("old-cookie")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	expires := now.Add(time.Hour)

	mock.ExpectQuery(`SELECT \* FROM "auth_sessions"`).
		WillReturnRows(authSessionRows(1, oldTokenCipher, oldCookieCipher, expires, now))
	mock.ExpectExec(`UPDATE "channels"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO "monitor_logs"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`SELECT \* FROM "auth_sessions"`).
		WillReturnRows(authSessionRows(1, oldTokenCipher, oldCookieCipher, expires, now))
	mock.ExpectQuery(`SELECT \* FROM "auth_sessions"`).
		WillReturnRows(authSessionRows(1, oldTokenCipher, oldCookieCipher, expires, now))
	mock.ExpectExec(`UPDATE "auth_sessions"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "channels"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "channels"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO "monitor_logs"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
	mock.ExpectExec(`UPDATE "channels"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "channels"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO "balance_snapshots"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	upstream := &authRecoveryConnector{}
	connector.Register(authRecoveryConnectorType, func() connector.Connector { return upstream })
	channels := storage.NewChannels(db)
	channelSvc := channel.NewService(
		channels,
		storage.NewAuthSessions(db),
		storage.NewCaptchas(db),
		storage.NewMonitorLogs(db),
		nil,
		cipher,
		channel.SessionPolicy{AuthCheckCache: 5 * time.Minute},
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher := notify.NewDispatcherWithCooldown(
		nil,
		cipher,
		log,
		notify.Policy{MonitorFailedCooldown: time.Hour},
		rejectNotificationClaim{},
	)
	svc := NewService(
		channels,
		storage.NewRates(db),
		nil,
		storage.NewMonitorLogs(db),
		channelSvc,
		dispatcher,
		log,
		false,
	)
	c := &storage.Channel{
		ID: 1, Name: "expired", Type: storage.ChannelType(authRecoveryConnectorType),
		SiteURL: "https://example.test", Username: "user", PasswordCipher: passwordCipher,
		CredentialMode: storage.CredentialModePassword,
	}

	if err := svc.RefreshBalance(context.Background(), c); err != nil {
		t.Errorf("RefreshBalance returned error: %v", err)
	}
	if upstream.balanceCalls != 2 {
		t.Errorf("GetBalance calls = %d, want 2", upstream.balanceCalls)
	}
	if upstream.loginCalls != 1 {
		t.Errorf("Login calls = %d, want 1", upstream.loginCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func authSessionRows(channelID uint, tokenCipher, cookieCipher string, expires, now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"channel_id", "user_id", "access_token_cipher", "cookie_cipher", "csrf_token_cipher", "expires_at", "last_login_at", "updated_at",
	}).AddRow(channelID, "1", tokenCipher, cookieCipher, "", expires, now, now)
}

type authRecoveryConnector struct {
	loginCalls   int
	balanceCalls int
}

func (c *authRecoveryConnector) GetTurnstileSiteKey(context.Context, *connector.Channel) (string, error) {
	return "", nil
}

func (c *authRecoveryConnector) Login(context.Context, *connector.Channel) (*connector.AuthSession, error) {
	c.loginCalls++
	return &connector.AuthSession{
		UserID: "1", AccessToken: "fresh-token", Cookie: "fresh-cookie", ExpiresAt: time.Now().Add(time.Hour),
	}, nil
}

func (c *authRecoveryConnector) CheckAuth(context.Context, *connector.Channel, *connector.AuthSession) error {
	return nil
}

func (c *authRecoveryConnector) GetBalance(_ context.Context, _ *connector.Channel, session *connector.AuthSession) (*connector.BalanceResult, error) {
	c.balanceCalls++
	if session.AccessToken == "old-token" {
		return nil, connector.HTTPStatusError(401, nil)
	}
	return &connector.BalanceResult{Balance: 12.5, SampledAt: time.Now()}, nil
}

func (c *authRecoveryConnector) GetRates(context.Context, *connector.Channel, *connector.AuthSession) ([]connector.RateResult, error) {
	panic("unexpected GetRates call")
}

func (c *authRecoveryConnector) GetUsage(context.Context, *connector.Channel, *connector.AuthSession, connector.UsageQuery) (*connector.UsageResult, error) {
	panic("unexpected GetUsage call")
}

type rejectNotificationClaim struct{}

func (rejectNotificationClaim) TryClaimCooldown(uint, storage.NotificationEvent, time.Duration) (bool, error) {
	return false, nil
}

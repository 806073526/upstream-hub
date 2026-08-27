// Package channel 提供渠道领域服务：把存储层的加密字段解开成 connector.Channel，
// 处理登录会话的复用与刷新、手动测试登录、手动刷新余额 / 倍率等。
package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/worryzyy/upstream-hub/internal/captcha"
	"github.com/worryzyy/upstream-hub/internal/connector"
	"github.com/worryzyy/upstream-hub/internal/crypto"
	"github.com/worryzyy/upstream-hub/internal/progress"
	"github.com/worryzyy/upstream-hub/internal/storage"
)

// SessionRefreshThreshold 距离过期还有多久就提前刷新登录。
const SessionRefreshThreshold = 5 * time.Minute

// tokenSessionTTL token 模式下"假装"给 AuthSession 的有效期。
// token 由用户提供，我们没法续期，这里设一年只是为了避免 SessionRefreshThreshold 把它判过期。
// 真正失效检测靠 connector.CheckAuth + 上游 401/403。
const tokenSessionTTL = 365 * 24 * time.Hour

type SessionPolicy struct {
	AuthCheckCache time.Duration
	RetryBase      time.Duration
	RetryMax       time.Duration
	AuthRetryBase  time.Duration
	AuthRetryMax   time.Duration
}

func cooldownActive(state *storage.MonitorState, now time.Time) (*CooldownError, bool) {
	if state == nil || state.NextAttemptAt == nil || !state.NextAttemptAt.After(now) {
		return nil, false
	}
	return &CooldownError{Until: *state.NextAttemptAt}, true
}

func authCheckDue(state *storage.MonitorState, now time.Time, cache time.Duration) bool {
	if cache <= 0 || state == nil || state.LastCheckedAt == nil {
		return true
	}
	return !state.LastCheckedAt.Add(cache).After(now)
}

type CooldownError struct {
	Until time.Time
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("channel monitoring is cooling down until %s", e.Until.Format(time.RFC3339))
}

func IsCooldown(err error) bool {
	var cooldown *CooldownError
	return errors.As(err, &cooldown)
}

// Service 渠道领域服务。
type Service struct {
	Channels      *storage.Channels
	AuthSessions  *storage.AuthSessions
	Captchas      *storage.Captchas
	MonitorLogs   *storage.MonitorLogs
	MonitorStates *storage.MonitorStates
	Cipher        *crypto.Cipher
	Policy        SessionPolicy
	locksMu       sync.Mutex
	locks         map[uint]*sync.Mutex
	recovered     map[uint]bool
}

func NewService(
	channels *storage.Channels,
	authSessions *storage.AuthSessions,
	captchas *storage.Captchas,
	monitorLogs *storage.MonitorLogs,
	monitorStates *storage.MonitorStates,
	cipher *crypto.Cipher,
	policy SessionPolicy,
) *Service {
	if policy.AuthCheckCache <= 0 {
		policy.AuthCheckCache = 5 * time.Minute
	}
	if policy.RetryBase <= 0 {
		policy.RetryBase = 15 * time.Minute
	}
	if policy.RetryMax <= 0 {
		policy.RetryMax = 2 * time.Hour
	}
	return &Service{
		Channels:      channels,
		AuthSessions:  authSessions,
		Captchas:      captchas,
		MonitorLogs:   monitorLogs,
		MonitorStates: monitorStates,
		Cipher:        cipher,
		Policy:        policy,
		locks:         make(map[uint]*sync.Mutex),
		recovered:     make(map[uint]bool),
	}
}

// NewAPITokenCredential token 模式下 NewAPI 的凭据 JSON 结构。
//
// Cookie：浏览器 DevTools 里拷出来的整条 Cookie 头
// UserID：上游账号 ID（NewAPI 个人设置页可见，作为 New-Api-User 请求头必填）
type NewAPITokenCredential struct {
	Cookie string `json:"cookie"`
	UserID string `json:"user_id"`
}

// Sub2APITokenCredential token 模式下 Sub2API 的凭据。
type Sub2APITokenCredential struct {
	AccessToken string `json:"access_token"`
}

// CreateInput 新建渠道使用的明文输入。
//
// CredentialMode 决定字段语义：
//   - password: Password 必填；Username 为登录账号
//   - token:    TokenCredential 必填（已序列化为 JSON 字符串）；Username 仅作展示备注
type CreateInput struct {
	Name             string
	Type             storage.ChannelType
	SiteURL          string
	Username         string
	Password         string
	CredentialMode   storage.CredentialMode
	TokenCredential  string // JSON：password 模式时为空
	TurnstileEnabled bool
	CaptchaConfigID  *uint
	BalanceThreshold float64
	MonitorEnabled   bool
}

func (s *Service) Create(in CreateInput) (*storage.Channel, error) {
	mode := in.CredentialMode
	if mode == "" {
		mode = storage.CredentialModePassword
	}
	rawCred, err := selectRawCredential(mode, in.Password, in.TokenCredential)
	if err != nil {
		return nil, err
	}
	if err := validateCredential(in.Type, mode, rawCred); err != nil {
		return nil, err
	}

	enc, err := s.Cipher.Encrypt(rawCred)
	if err != nil {
		return nil, fmt.Errorf("encrypt credential: %w", err)
	}
	c := &storage.Channel{
		Name:             in.Name,
		Type:             in.Type,
		SiteURL:          in.SiteURL,
		Username:         in.Username,
		PasswordCipher:   enc,
		CredentialMode:   mode,
		TurnstileEnabled: in.TurnstileEnabled && mode == storage.CredentialModePassword, // token 模式不需要打码
		CaptchaConfigID:  in.CaptchaConfigID,
		BalanceThreshold: in.BalanceThreshold,
		MonitorEnabled:   in.MonitorEnabled,
	}
	if mode == storage.CredentialModeToken {
		// token 模式不依赖打码 provider
		c.CaptchaConfigID = nil
	}
	if err := s.Channels.Create(c); err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateInput 编辑渠道的可选字段。Password / TokenCredential 为空表示不修改凭据。
type UpdateInput struct {
	Name             *string
	SiteURL          *string
	Username         *string
	Password         *string
	CredentialMode   *storage.CredentialMode
	TokenCredential  *string // JSON
	TurnstileEnabled *bool
	CaptchaConfigID  *uint
	BalanceThreshold *float64
	MonitorEnabled   *bool
}

func (s *Service) Update(id uint, in UpdateInput) (*storage.Channel, error) {
	c, err := s.Channels.FindByID(id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		c.Name = *in.Name
	}
	if in.SiteURL != nil {
		c.SiteURL = *in.SiteURL
	}
	if in.Username != nil {
		c.Username = *in.Username
	}

	// 决定本次更新后的最终凭据模式。
	finalMode := c.CredentialMode
	if in.CredentialMode != nil && *in.CredentialMode != "" {
		finalMode = *in.CredentialMode
	}
	if finalMode == "" {
		finalMode = storage.CredentialModePassword
	}

	// 是否切换了模式 → 强制重写凭据并清空 session
	modeChanged := finalMode != c.CredentialMode

	var rawCred string
	switch finalMode {
	case storage.CredentialModePassword:
		if in.Password != nil && *in.Password != "" {
			rawCred = *in.Password
		} else if modeChanged {
			return nil, errors.New("切换到账号密码模式时必须填写密码")
		}
	case storage.CredentialModeToken:
		if in.TokenCredential != nil && *in.TokenCredential != "" {
			rawCred = *in.TokenCredential
		} else if modeChanged {
			return nil, errors.New("切换到 token 模式时必须填写凭据")
		}
	default:
		return nil, fmt.Errorf("unknown credential mode: %s", finalMode)
	}

	if rawCred != "" {
		if err := validateCredential(c.Type, finalMode, rawCred); err != nil {
			return nil, err
		}
		enc, err := s.Cipher.Encrypt(rawCred)
		if err != nil {
			return nil, fmt.Errorf("encrypt credential: %w", err)
		}
		c.PasswordCipher = enc
		c.CredentialMode = finalMode
		// 凭据或模式变了，强制下次重新构造 session
		_ = s.AuthSessions.Delete(c.ID)
	} else if modeChanged {
		// 理论上面已挡住，这里兜底
		return nil, errors.New("凭据模式变更必须同时提供新凭据")
	}

	if in.TurnstileEnabled != nil {
		c.TurnstileEnabled = *in.TurnstileEnabled && finalMode == storage.CredentialModePassword
	}
	if in.CaptchaConfigID != nil {
		if finalMode == storage.CredentialModePassword {
			c.CaptchaConfigID = in.CaptchaConfigID
		} else {
			c.CaptchaConfigID = nil
		}
	} else if finalMode == storage.CredentialModeToken {
		// token 模式强制清空打码绑定
		c.CaptchaConfigID = nil
	}
	if in.BalanceThreshold != nil {
		c.BalanceThreshold = *in.BalanceThreshold
	}
	if in.MonitorEnabled != nil {
		c.MonitorEnabled = *in.MonitorEnabled
	}
	if err := s.Channels.Update(c); err != nil {
		return nil, err
	}
	return c, nil
}

// selectRawCredential 在 Create 时根据 mode 决定要落库的明文凭据字符串。
func selectRawCredential(mode storage.CredentialMode, password, tokenCredential string) (string, error) {
	switch mode {
	case storage.CredentialModePassword:
		if password == "" {
			return "", errors.New("账号密码模式下密码不能为空")
		}
		return password, nil
	case storage.CredentialModeToken:
		if tokenCredential == "" {
			return "", errors.New("token 模式下必须提供凭据")
		}
		return tokenCredential, nil
	default:
		return "", fmt.Errorf("unknown credential mode: %s", mode)
	}
}

// validateCredential 在保存前对凭据做语法 / 必填字段校验，能尽早把无效输入挡在 connector 外。
//
// 注意：这里只做语法层校验，不做"凭据是否真的有效"的网络验证——
// 那个交给后续 TestLogin / 第一次同步去发现。
func validateCredential(channelType storage.ChannelType, mode storage.CredentialMode, raw string) error {
	if mode != storage.CredentialModeToken {
		return nil
	}
	switch channelType {
	case storage.ChannelTypeNewAPI:
		var cred NewAPITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return fmt.Errorf("解析 NewAPI 凭据 JSON 失败：%w", err)
		}
		if strings.TrimSpace(cred.Cookie) == "" {
			return errors.New("NewAPI token 模式需要 Cookie")
		}
		if strings.TrimSpace(cred.UserID) == "" {
			return errors.New("NewAPI token 模式需要 User ID（在 NewAPI 个人设置页查看）")
		}
	case storage.ChannelTypeSub2API:
		var cred Sub2APITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return fmt.Errorf("解析 Sub2API 凭据 JSON 失败：%w", err)
		}
		if strings.TrimSpace(cred.AccessToken) == "" {
			return errors.New("Sub2API token 模式需要 access_token")
		}
	default:
		return fmt.Errorf("unknown channel type: %s", channelType)
	}
	return nil
}

func (s *Service) Delete(id uint) error {
	_ = s.AuthSessions.Delete(id)
	if s.MonitorStates != nil {
		_ = s.MonitorStates.Delete(id)
	}
	return s.Channels.Delete(id)
}

// Resolve 把存储层的加密渠道解密成 connector 可用的 Channel。
//
// 注意：这一步**不**求解 Turnstile —— 打码只在真正要登录时做（见 prepareTurnstile），
// 复用现有 session 的路径无需任何打码消耗。
//
// token 模式下 connector.Channel.Password 留空——connector 永远不会读到它。
func (s *Service) Resolve(ctx context.Context, c *storage.Channel) (*connector.Channel, error) {
	_ = ctx
	raw, err := s.Cipher.Decrypt(c.PasswordCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	resolved := &connector.Channel{
		ID:               c.ID,
		Name:             c.Name,
		Type:             connector.ChannelType(c.Type),
		SiteURL:          c.SiteURL,
		Username:         c.Username,
		TurnstileEnabled: c.TurnstileEnabled,
	}
	if c.CredentialMode == storage.CredentialModeToken {
		// token 模式：raw 是 JSON，Password 留空避免被 connector 误用
		resolved.Password = ""
	} else {
		resolved.Password = raw
	}
	return resolved, nil
}

// buildSessionFromToken 在 token 模式下，把用户提供的凭据 JSON 解析成 AuthSession。
// 不发任何 HTTP 请求——失效检测留给 connector.CheckAuth + 后续 GetBalance / GetRates。
func (s *Service) buildSessionFromToken(c *storage.Channel) (*connector.AuthSession, error) {
	raw, err := s.Cipher.Decrypt(c.PasswordCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	switch c.Type {
	case storage.ChannelTypeNewAPI:
		var cred NewAPITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, fmt.Errorf("parse newapi token credential: %w", err)
		}
		return &connector.AuthSession{
			UserID:    cred.UserID,
			Cookie:    cred.Cookie,
			ExpiresAt: time.Now().Add(tokenSessionTTL),
		}, nil
	case storage.ChannelTypeSub2API:
		var cred Sub2APITokenCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, fmt.Errorf("parse sub2api token credential: %w", err)
		}
		return &connector.AuthSession{
			AccessToken: cred.AccessToken,
			ExpiresAt:   time.Now().Add(tokenSessionTTL),
		}, nil
	default:
		return nil, fmt.Errorf("unknown channel type: %s", c.Type)
	}
}

// prepareTurnstile 在调用 conn.Login 之前求解 Turnstile token。
// 没启用 turnstile 或者上游 site 公开接口说"未开启 Turnstile"时是空操作。
func (s *Service) prepareTurnstile(
	ctx context.Context,
	c *storage.Channel,
	resolved *connector.Channel,
	conn connector.Connector,
) error {
	if !c.TurnstileEnabled || c.CaptchaConfigID == nil {
		return nil
	}
	progress.Start(ctx, progress.StageCaptcha, "求解 Turnstile…")
	siteKey, err := conn.GetTurnstileSiteKey(ctx, resolved)
	if err != nil {
		progress.Fail(ctx, progress.StageCaptcha, err.Error())
		return fmt.Errorf("fetch turnstile site key: %w", err)
	}
	if siteKey == "" {
		progress.OK(ctx, progress.StageCaptcha, "上游未开启 Turnstile，跳过")
		return nil
	}
	token, err := s.solveCaptcha(ctx, *c.CaptchaConfigID, siteKey, c.SiteURL)
	if err != nil {
		progress.Fail(ctx, progress.StageCaptcha, err.Error())
		return fmt.Errorf("solve captcha: %w", err)
	}
	resolved.TurnstileToken = token
	progress.OK(ctx, progress.StageCaptcha, "打码完成")
	return nil
}

func (s *Service) solveCaptcha(ctx context.Context, captchaID uint, siteKey, pageURL string) (string, error) {
	cfg, err := s.Captchas.FindByID(captchaID)
	if err != nil {
		return "", err
	}
	if !cfg.Enabled {
		return "", errors.New("captcha config disabled")
	}
	apiKey, err := s.Cipher.Decrypt(cfg.APIKeyCipher)
	if err != nil {
		return "", err
	}
	provider, err := captcha.Build(cfg, apiKey)
	if err != nil {
		return "", err
	}
	return provider.SolveTurnstile(ctx, siteKey, pageURL)
}

// EnsureSession 优先复用未过期的 session，否则重新登录并加密回写。
//
// token 模式：
//   - 跳过 AuthSessions 表与 Login 调用
//   - 每次构造一个临时 AuthSession（基于用户提供的凭据）返回
//   - CheckAuth 用来发现 token 是否还有效；失效会在 last_error 显示
func (s *Service) EnsureSession(
	ctx context.Context,
	c *storage.Channel,
	resolved *connector.Channel,
	conn connector.Connector,
) (*connector.AuthSession, error) {
	return s.ensureSession(ctx, c, resolved, conn, false)
}

// EnsureSessionManual bypasses automatic monitoring cooldowns and always
// validates a cached session. It is used only for user-triggered refreshes.
func (s *Service) EnsureSessionManual(
	ctx context.Context,
	c *storage.Channel,
	resolved *connector.Channel,
	conn connector.Connector,
) (*connector.AuthSession, error) {
	return s.ensureSession(ctx, c, resolved, conn, true)
}

func (s *Service) ensureSession(
	ctx context.Context,
	c *storage.Channel,
	resolved *connector.Channel,
	conn connector.Connector,
	manual bool,
) (*connector.AuthSession, error) {
	unlock := s.lockChannel(c.ID)
	defer unlock()

	now := time.Now()
	state, err := s.monitorState(c.ID)
	if err != nil {
		return nil, err
	}
	if !manual {
		if cooldown, active := cooldownActive(state, now); active {
			return nil, cooldown
		}
	}

	if c.CredentialMode == storage.CredentialModeToken {
		progress.Start(ctx, progress.StageSession, "使用用户提供的 token…")
		session, err := s.buildSessionFromToken(c)
		if err != nil {
			progress.Fail(ctx, progress.StageSession, err.Error())
			s.recordFailure(c.ID, "transient", err, manual)
			return nil, err
		}
		if manual || authCheckDue(state, now, s.Policy.AuthCheckCache) {
			// token 没有可续期的登录流程，校验失败直接进入渠道退避。
			if err := conn.CheckAuth(ctx, resolved, session); err != nil {
				msg := "token 校验失败：" + err.Error()
				progress.Fail(ctx, progress.StageSession, msg)
				s.recordFailure(c.ID, failureKind(err), errors.New(msg), manual)
				return nil, errors.New(msg)
			}
			_, _ = s.recordSuccess(c.ID)
		}
		_ = s.Channels.SetLastError(c.ID, "")
		progress.OK(ctx, progress.StageSession, "token 有效，跳过登录")
		return session, nil
	}

	saved, err := s.AuthSessions.FindByChannel(c.ID)
	if err != nil {
		return nil, err
	}
	if saved != nil {
		session, err := s.decryptSession(saved)
		if err != nil {
			return nil, err
		}
		if saved.ExpiresAt != nil && time.Until(*saved.ExpiresAt) <= SessionRefreshThreshold {
			if refresher, ok := conn.(connector.SessionRefresher); ok && session.Cookie != "" {
				progress.Start(ctx, progress.StageSession, "刷新已有会话…")
				refreshed, refreshErr := refresher.Refresh(ctx, resolved, session)
				if refreshErr == nil {
					if err := s.persistSession(c.ID, refreshed); err != nil {
						progress.Fail(ctx, progress.StageSession, err.Error())
						s.recordFailure(c.ID, "transient", err, manual)
						return nil, err
					}
					_, _ = s.recordSuccess(c.ID)
					_ = s.Channels.SetLastError(c.ID, "")
					progress.OK(ctx, progress.StageSession, "会话刷新成功")
					return refreshed, nil
				}
				if !manual && failureKind(refreshErr) != "auth" {
					s.recordFailure(c.ID, failureKind(refreshErr), refreshErr, false)
					progress.Fail(ctx, progress.StageSession, refreshErr.Error())
					return nil, refreshErr
				}
				if manual {
					progress.OK(ctx, progress.StageSession, "会话刷新失败，继续手动登录")
				} else {
					progress.OK(ctx, progress.StageSession, "会话已失效，重新登录")
				}
			}
		}
		if saved.ExpiresAt == nil || time.Until(*saved.ExpiresAt) <= SessionRefreshThreshold {
			return s.loginUnlocked(ctx, c, resolved, conn, manual)
		}
		if !manual && !authCheckDue(state, now, s.Policy.AuthCheckCache) {
			progress.OK(ctx, progress.StageSession, "复用已校验会话")
			return session, nil
		}
		// 轻量校验现有 session。只有 401/403 才允许重新登录，其余错误进入退避。
		progress.Start(ctx, progress.StageSession, "校验已有会话…")
		if err := conn.CheckAuth(ctx, resolved, session); err == nil {
			_, _ = s.recordSuccess(c.ID)
			progress.OK(ctx, progress.StageSession, "复用现有会话")
			return session, nil
		} else if failureKind(err) != "auth" && !manual {
			s.recordFailure(c.ID, failureKind(err), err, false)
			progress.Fail(ctx, progress.StageSession, err.Error())
			return nil, err
		} else if failureKind(err) != "auth" && manual {
			// 手动操作可以继续尝试登录，但不修改自动冷却状态。
			progress.OK(ctx, progress.StageSession, "会话校验失败，继续手动登录")
		} else {
			progress.OK(ctx, progress.StageSession, "会话已失效，重新登录")
		}
	}
	return s.loginUnlocked(ctx, c, resolved, conn, manual)
}

// RecoverSession replaces a session rejected by a data endpoint. Recovery is
// attempted exactly once by the caller: refresh first when supported, then
// fall back to a full password login. Token credentials cannot self-renew.
func (s *Service) RecoverSession(
	ctx context.Context,
	c *storage.Channel,
	resolved *connector.Channel,
	conn connector.Connector,
	stale *connector.AuthSession,
) (*connector.AuthSession, error) {
	if c.CredentialMode == storage.CredentialModeToken {
		return nil, errors.New("token 凭据已失效，请重新粘贴凭据")
	}

	unlock := s.lockChannel(c.ID)
	defer unlock()

	// Another balance/rates/usage request may have recovered this channel while
	// this request waited for the per-channel lock. Reuse the persisted session
	// instead of submitting credentials (and possibly a captcha) again.
	saved, err := s.AuthSessions.FindByChannel(c.ID)
	if err != nil {
		return nil, err
	}
	if saved != nil {
		current, err := s.decryptSession(saved)
		if err != nil {
			return nil, err
		}
		if sessionChanged(stale, current) {
			progress.OK(ctx, progress.StageSession, "复用其他任务已恢复的会话")
			return current, nil
		}
	}

	if refresher, ok := conn.(connector.SessionRefresher); ok && stale != nil && stale.Cookie != "" {
		progress.Start(ctx, progress.StageSession, "业务请求鉴权失效，刷新会话…")
		refreshed, err := refresher.Refresh(ctx, resolved, stale)
		if err == nil {
			if err := s.persistSession(c.ID, refreshed); err != nil {
				progress.Fail(ctx, progress.StageSession, err.Error())
				return nil, err
			}
			_, _ = s.recordSuccess(c.ID)
			_ = s.Channels.SetLastError(c.ID, "")
			progress.OK(ctx, progress.StageSession, "会话刷新成功，重试采集")
			return refreshed, nil
		}
		progress.OK(ctx, progress.StageSession, "会话刷新失败，重新登录")
	}

	// Recovery failures are recorded once by the monitor after this method
	// returns, so the internal login uses manual failure semantics here.
	resolved.TurnstileToken = ""
	return s.loginUnlocked(ctx, c, resolved, conn, true)
}

func sessionChanged(stale, current *connector.AuthSession) bool {
	if stale == nil || current == nil {
		return false
	}
	return stale.UserID != current.UserID ||
		stale.AccessToken != current.AccessToken ||
		stale.Cookie != current.Cookie ||
		stale.CSRFToken != current.CSRFToken ||
		!stale.ExpiresAt.Equal(current.ExpiresAt)
}

func (s *Service) loginUnlocked(
	ctx context.Context,
	c *storage.Channel,
	resolved *connector.Channel,
	conn connector.Connector,
	manual bool,
) (*connector.AuthSession, error) {
	if err := s.prepareTurnstile(ctx, c, resolved, conn); err != nil {
		s.recordFailure(c.ID, failureKind(err), err, manual)
		return nil, err
	}
	progress.Start(ctx, progress.StageLogin, "登录上游…")
	started := time.Now()
	session, err := conn.Login(ctx, resolved)
	finished := time.Now()
	_ = s.MonitorLogs.Append(&storage.MonitorLog{
		ChannelID:    c.ID,
		Job:          storage.MonitorJobLogin,
		Success:      err == nil,
		ErrorMessage: errString(err),
		StartedAt:    started,
		FinishedAt:   finished,
	})
	if err != nil {
		progress.Fail(ctx, progress.StageLogin, err.Error())
		s.recordFailure(c.ID, failureKind(err), err, manual)
		_ = s.Channels.SetLastError(c.ID, err.Error())
		return nil, err
	}
	if err := s.persistSession(c.ID, session); err != nil {
		progress.Fail(ctx, progress.StageLogin, err.Error())
		s.recordFailure(c.ID, "transient", err, manual)
		return nil, err
	}
	_, _ = s.recordSuccess(c.ID)
	_ = s.Channels.SetLastError(c.ID, "")
	progress.OK(ctx, progress.StageLogin, "登录成功")
	return session, nil
}

func (s *Service) persistSession(channelID uint, session *connector.AuthSession) error {
	acc, err := s.Cipher.Encrypt(session.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	cookie, err := s.Cipher.Encrypt(session.Cookie)
	if err != nil {
		return fmt.Errorf("encrypt cookie: %w", err)
	}
	csrf, err := s.Cipher.Encrypt(session.CSRFToken)
	if err != nil {
		return fmt.Errorf("encrypt csrf: %w", err)
	}
	now := time.Now()
	expires := session.ExpiresAt
	return s.AuthSessions.Upsert(&storage.AuthSession{
		ChannelID:         channelID,
		UserID:            session.UserID,
		AccessTokenCipher: acc,
		CookieCipher:      cookie,
		CSRFTokenCipher:   csrf,
		ExpiresAt:         &expires,
		LastLoginAt:       &now,
	})
}

func (s *Service) decryptSession(saved *storage.AuthSession) (*connector.AuthSession, error) {
	acc, err := s.Cipher.Decrypt(saved.AccessTokenCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	cookie, err := s.Cipher.Decrypt(saved.CookieCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt cookie: %w", err)
	}
	csrf, err := s.Cipher.Decrypt(saved.CSRFTokenCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt csrf: %w", err)
	}
	expires := time.Time{}
	if saved.ExpiresAt != nil {
		expires = *saved.ExpiresAt
	}
	return &connector.AuthSession{
		UserID:      saved.UserID,
		AccessToken: acc,
		Cookie:      cookie,
		CSRFToken:   csrf,
		ExpiresAt:   expires,
	}, nil
}

// TestLogin 手动测试登录：
//   - password 模式：复用 login() 的完整流程（打码 → 登录 → 持久化）
//   - token 模式：直接走 EnsureSession，等同于检查 CheckAuth 是否通过
func (s *Service) TestLogin(ctx context.Context, channelID uint) error {
	c, err := s.Channels.FindByID(channelID)
	if err != nil {
		return err
	}
	resolved, err := s.Resolve(ctx, c)
	if err != nil {
		return err
	}
	conn, err := connector.For(connector.ChannelType(c.Type))
	if err != nil {
		return err
	}
	if c.CredentialMode == storage.CredentialModeToken {
		_, err = s.ensureSession(ctx, c, resolved, conn, true)
		return err
	}
	unlock := s.lockChannel(c.ID)
	defer unlock()
	_, err = s.loginUnlocked(ctx, c, resolved, conn, true)
	return err
}

// RecordMonitorFailure records a scheduled data collection failure. It shares
// the same channel-wide backoff as login failures.
func (s *Service) RecordMonitorFailure(channelID uint, err error) error {
	if err == nil || IsCooldown(err) {
		return nil
	}
	unlock := s.lockChannel(channelID)
	defer unlock()
	return s.recordFailure(channelID, failureKind(err), err, false)
}

// RecordMonitorSuccess clears a channel backoff and reports whether this was
// a recovery from a previous automatic failure.
func (s *Service) RecordMonitorSuccess(channelID uint) (bool, error) {
	unlock := s.lockChannel(channelID)
	defer unlock()
	recovered := s.recovered[channelID]
	if _, err := s.recordSuccess(channelID); err != nil {
		return false, err
	}
	if s.recovered[channelID] {
		recovered = true
		delete(s.recovered, channelID)
	}
	return recovered, nil
}

func (s *Service) lockChannel(channelID uint) func() {
	s.locksMu.Lock()
	lock := s.locks[channelID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[channelID] = lock
	}
	s.locksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *Service) monitorState(channelID uint) (*storage.MonitorState, error) {
	if s.MonitorStates == nil {
		return &storage.MonitorState{ChannelID: channelID}, nil
	}
	return s.MonitorStates.FindByChannel(channelID)
}

func (s *Service) recordFailure(channelID uint, kind string, err error, manual bool) error {
	if manual || s.MonitorStates == nil {
		_ = s.Channels.SetLastError(channelID, errString(err))
		return nil
	}
	state, findErr := s.MonitorStates.FindByChannel(channelID)
	if findErr != nil {
		return findErr
	}
	now := time.Now()
	base, max := s.Policy.RetryBase, s.Policy.RetryMax
	if kind == "auth" {
		if s.Policy.AuthRetryBase > 0 {
			base = s.Policy.AuthRetryBase
		}
		if s.Policy.AuthRetryMax > 0 {
			max = s.Policy.AuthRetryMax
		}
	}
	next := now.Add(backoffDuration(state.FailureCount+1, base, max))
	state.RecordFailure(kind, err, next, now)
	if saveErr := s.MonitorStates.Save(state); saveErr != nil {
		return saveErr
	}
	_ = s.Channels.SetLastError(channelID, errString(err))
	return nil
}

func (s *Service) recordSuccess(channelID uint) (bool, error) {
	if s.MonitorStates == nil {
		_ = s.Channels.SetLastError(channelID, "")
		return false, nil
	}
	recovered, err := s.MonitorStates.RecordSuccess(channelID, time.Now())
	if err != nil {
		return false, err
	}
	_ = s.Channels.SetLastError(channelID, "")
	if recovered {
		s.recovered[channelID] = true
	}
	return recovered, nil
}

func backoffDuration(failureCount int, base, max time.Duration) time.Duration {
	if failureCount < 1 {
		failureCount = 1
	}
	if base <= 0 {
		base = 15 * time.Minute
	}
	if max <= 0 {
		max = 2 * time.Hour
	}
	delay := base
	for i := 1; i < failureCount; i++ {
		if delay >= max/2 {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}

func failureKind(err error) string {
	var statusErr *connector.HTTPError
	if errors.As(err, &statusErr) && (statusErr.StatusCode == 401 || statusErr.StatusCode == 403) {
		return "auth"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "transient"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "transient"
	}
	return "transient"
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

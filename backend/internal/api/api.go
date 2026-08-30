// Package api 注册所有 HTTP 路由，组装各业务 handler。
//
// 单用户场景下走 HMAC token 鉴权：账号密码写在 config 里，登录后下发 token。
package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/auth"
	"github.com/worryzyy/upstream-hub/internal/channel"
	"github.com/worryzyy/upstream-hub/internal/crypto"
	"github.com/worryzyy/upstream-hub/internal/monitor"
	"github.com/worryzyy/upstream-hub/internal/notify"
	"github.com/worryzyy/upstream-hub/internal/storage"
	"gorm.io/gorm"
)

// Deps 把所有 handler 需要的依赖打包传入。
type Deps struct {
	DB            *gorm.DB
	Cipher        *crypto.Cipher
	Auth          *auth.Service
	Channels      *storage.Channels
	Sessions      *storage.AuthSessions
	Captchas      *storage.Captchas
	Notifies      *storage.Notifications
	Rates         *storage.Rates
	Usage         *storage.Usage
	Profit        *storage.Profit
	Billing       *storage.Billing
	PersonalUsage PersonalUsageReader
	MonLogs       *storage.MonitorLogs
	MonitorStates *storage.MonitorStates
	ChannelSvc    *channel.Service
	Monitor       *monitor.Service
	Dispatcher    *notify.Dispatcher
	Log           *slog.Logger

	// Frontend 可选：传入嵌入的前端 dist 文件系统。nil 表示不挂载（本地开发用 vite dev server）。
	Frontend fs.FS
}

// PersonalUsageReader is optional so older installations and focused API
// tests can continue serving the gross-profit view while the personal-use
// ledger is being migrated.
type PersonalUsageReader interface {
	ListPersonalUsageBuckets(start, end time.Time, resolutionSeconds int) ([]storage.NewAPIPersonalUsageBucket, error)
}

// PersonalUsageStatusReader is implemented by readers that can distinguish an
// explicit successful zero-use response from an unavailable or not-yet-synced
// personal-use ledger.
type PersonalUsageStatusReader interface {
	ListPersonalUsageBucketsWithStatus(start, end time.Time, resolutionSeconds int) ([]storage.NewAPIPersonalUsageBucket, bool, error)
}

func listPersonalUsageBucketsWithStatus(reader PersonalUsageReader, start, end time.Time, resolutionSeconds int) ([]storage.NewAPIPersonalUsageBucket, bool, error) {
	if reader == nil {
		return nil, false, nil
	}
	if statusReader, ok := reader.(PersonalUsageStatusReader); ok {
		return statusReader.ListPersonalUsageBucketsWithStatus(start, end, resolutionSeconds)
	}
	rows, err := reader.ListPersonalUsageBuckets(start, end, resolutionSeconds)
	if err != nil {
		return nil, false, err
	}
	complete := false
	if len(rows) > 0 {
		_, complete = storage.SumPersonalUsageCNY(rows, start, end, resolutionSeconds)
	}
	return rows, complete, nil
}

// Register 把所有路由挂到给定 gin engine。
func Register(r *gin.Engine, d *Deps) {
	r.GET("/healthz", func(c *gin.Context) {
		sqlDB, err := d.DB.DB()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "down", "err": err.Error()})
			return
		}
		if err := sqlDB.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "db_down", "err": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	if d.Auth != nil {
		api.Use(d.Auth.Middleware())
	}
	{
		api.GET("/version", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"name": "upstream-hub", "version": "0.1.0-dev"})
		})

		registerAuth(api, d)
		registerChannels(api, d)
		registerCaptchas(api, d)
		registerNotifications(api, d)
		registerRates(api, d)
		registerMonitorLogs(api, d)
		registerDashboard(api, d)
	}

	if d.Frontend != nil {
		registerFrontend(r, d.Frontend)
	}
}

// registerFrontend 把嵌入的前端 dist 挂在根路径，并处理 SPA fallback：
//
//   - GET /assets/*  → 直接返回文件
//   - GET /          → 返回 index.html
//   - GET /channels  → 返回 index.html（React Router 客户端路由）
//
// /api/*、/healthz 都已被前面的具体路由占了，不会走到这里。
// 安全起见仍然做一次前缀拦截，避免任何意外情况下"未鉴权读 index.html"压到 /api 上。
func registerFrontend(r *gin.Engine, dist fs.FS) {
	fileServer := http.FileServer(http.FS(dist))

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		// 永远不让 SPA fallback 覆盖 API / 健康检查路径。
		if strings.HasPrefix(path, "/api/") || path == "/healthz" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		// 文件存在就直接 serve，否则回落到 index.html。
		clean := strings.TrimPrefix(path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(dist, clean); err != nil {
			c.Request.URL.Path = "/"
		}
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}

// fail 统一错误响应。
func fail(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"error": err.Error()})
}

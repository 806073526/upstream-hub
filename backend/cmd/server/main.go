package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/worryzyy/upstream-hub/internal/api"
	"github.com/worryzyy/upstream-hub/internal/auth"
	"github.com/worryzyy/upstream-hub/internal/billing"
	"github.com/worryzyy/upstream-hub/internal/channel"
	"github.com/worryzyy/upstream-hub/internal/config"
	"github.com/worryzyy/upstream-hub/internal/crypto"
	newapiintegration "github.com/worryzyy/upstream-hub/internal/integration/newapi"
	"github.com/worryzyy/upstream-hub/internal/logger"
	"github.com/worryzyy/upstream-hub/internal/monitor"
	"github.com/worryzyy/upstream-hub/internal/notify"
	"github.com/worryzyy/upstream-hub/internal/scheduler"
	"github.com/worryzyy/upstream-hub/internal/storage"
	"github.com/worryzyy/upstream-hub/web"

	// 注册 connector 实现。
	_ "github.com/worryzyy/upstream-hub/internal/connector/newapi"
	_ "github.com/worryzyy/upstream-hub/internal/connector/sub2api"
)

func main() {
	configPath := flag.String("config", "", "path to config.yaml (optional; env vars also supported)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config failed: %v\n", err)
		os.Exit(1)
	}

	log := logger.New(cfg.Log.Level, cfg.Log.Format)
	log.Info("starting upstream-hub", "port", cfg.Server.Port, "mode", cfg.Server.Mode)

	cipher, err := crypto.NewCipher(cfg.Security.AppSecret)
	if err != nil {
		log.Error("init cipher failed (set APP_SECRET)", "err", err)
		os.Exit(1)
	}

	// Auth：默认禁用（AUTH_ENABLED=false），所有 /api/* 免 token；
	// 显式开启时账号/密码必填，token secret 缺省回退到 AppSecret。
	var authSvc *auth.Service
	if cfg.Auth.Enabled {
		tokenSecret := cfg.Auth.TokenSecret
		if tokenSecret == "" {
			tokenSecret = cfg.Security.AppSecret
		}
		authSvc, err = auth.New(
			cfg.Auth.Username,
			cfg.Auth.Password,
			tokenSecret,
			time.Duration(cfg.Auth.SessionTTLHours)*time.Hour,
		)
		if err != nil {
			log.Error("init auth failed (set ADMIN_USERNAME / ADMIN_PASSWORD or AUTH_ENABLED=false)", "err", err)
			os.Exit(1)
		}
		log.Info("auth enabled", "username", cfg.Auth.Username)
	} else {
		log.Warn("auth disabled — all /api/* endpoints are open; set AUTH_ENABLED=true for production exposure")
	}

	db, err := storage.Open(cfg.Database.ToStorageConfig())
	if err != nil {
		log.Error("open database failed", "err", err)
		os.Exit(1)
	}
	if err := storage.AutoMigrate(db); err != nil {
		log.Error("auto migrate failed", "err", err)
		os.Exit(1)
	}

	channels := storage.NewChannels(db)
	authSessions := storage.NewAuthSessions(db)
	captchas := storage.NewCaptchas(db)
	notifies := storage.NewNotifications(db)
	rates := storage.NewRates(db)
	usage := storage.NewUsage(db)
	profit := storage.NewProfit(db)
	billingRepo := storage.NewBillingWithCostRate(db, cfg.Billing.UpstreamCostCNYPerUSD)
	monLogs := storage.NewMonitorLogs(db)
	monitorStates := storage.NewMonitorStates(db)

	channelSvc := channel.NewService(channels, authSessions, captchas, monLogs, monitorStates, cipher, channel.SessionPolicy{
		AuthCheckCache: time.Duration(cfg.Monitor.AuthCheckCacheMinutes) * time.Minute,
		RetryBase:      time.Duration(cfg.Monitor.TransientRetryBaseMinutes) * time.Minute,
		RetryMax:       time.Duration(cfg.Monitor.TransientRetryMaxMinutes) * time.Minute,
		AuthRetryBase:  time.Duration(cfg.Monitor.LoginFailureCooldownMinutes) * time.Minute,
		AuthRetryMax:   time.Duration(cfg.Monitor.LoginFailureMaxCooldownMinutes) * time.Minute,
	})
	dispatcher := notify.NewDispatcher(notifies, cipher, log, notify.Policy{
		BatchRateChanges:      cfg.Notifications.BatchRateChanges,
		MinChangePct:          cfg.Notifications.MinChangePct,
		BalanceLowCooldown:    time.Duration(cfg.Notifications.BalanceLowCooldownMinutes) * time.Minute,
		LoginFailedCooldown:   time.Duration(cfg.Notifications.LoginFailedCooldownMinutes) * time.Minute,
		MonitorFailedCooldown: time.Duration(cfg.Notifications.MonitorFailedCooldownMinutes) * time.Minute,
		SendMaxAttempts:       cfg.Notifications.SendMaxAttempts,
	})
	monitorSvc := monitor.NewService(channels, rates, usage, monLogs, channelSvc, dispatcher, log, cfg.Notifications.RecoveryNotifications, cfg.Scheduler.Concurrency)

	sch := scheduler.New(cfg.Scheduler, monitorSvc, monLogs, rates, usage, notifies, log)
	var newAPIClient *newapiintegration.Client
	if cfg.Integration.NewAPI.Enabled {
		newAPIClient = newapiintegration.NewClient(
			cfg.Integration.NewAPI.BaseURL,
			cfg.Integration.NewAPI.APIToken,
			time.Duration(cfg.Integration.NewAPI.TimeoutSeconds)*time.Second,
		)
		sch.SetNewAPIIntegration(newAPIClient, cfg.Integration.NewAPI)
		log.Info("new-api integration enabled", "baseURL", cfg.Integration.NewAPI.BaseURL, "autoPriority", cfg.Integration.NewAPI.AutoPriority)
	}
	billingSvc := billing.NewService(newAPIClient, billingRepo, cfg.Billing, time.Now)
	sch.SetBillingService(billingSvc, cfg.Billing.Cron)
	if err := sch.Start(); err != nil {
		log.Error("start scheduler failed", "err", err)
		os.Exit(1)
	}
	defer sch.Stop()

	gin.SetMode(cfg.Server.Mode)
	router := gin.New()
	router.Use(gin.Recovery())
	if len(cfg.Server.TrustedProxies) > 0 {
		_ = router.SetTrustedProxies(cfg.Server.TrustedProxies)
	}

	// 仅在嵌入了真实前端产物时挂载静态 handler。
	// 本地 `go run` 跑出来的二进制 dist 是空占位，此时由 vite dev server 接管 :3010。
	var frontendFS fs.FS
	if web.HasFrontend() {
		frontendFS = web.DistFS()
		log.Info("frontend embedded, serving SPA on /")
	} else {
		log.Info("no embedded frontend, run vite dev server separately for UI")
	}

	api.Register(router, &api.Deps{
		DB:            db,
		Cipher:        cipher,
		Auth:          authSvc,
		Channels:      channels,
		Sessions:      authSessions,
		Captchas:      captchas,
		Notifies:      notifies,
		Rates:         rates,
		Usage:         usage,
		Profit:        profit,
		Billing:       billingRepo,
		MonLogs:       monLogs,
		MonitorStates: monitorStates,
		ChannelSvc:    channelSvc,
		Monitor:       monitorSvc,
		Dispatcher:    dispatcher,
		Log:           log,
		Frontend:      frontendFS,
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server error", "err", err)
			os.Exit(1)
		}
	}()
	log.Info("http server listening", "addr", srv.Addr)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Info("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("http shutdown error", "err", err)
	}
	log.Info("bye")
}

// Command web runs the Krabber website. On Elastic Beanstalk it listens on
// :5000 behind nginx; locally it runs against DynamoDB Local (make dev).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/t0ul/krabber-net/internal/config"
	"github.com/t0ul/krabber-net/internal/jobs"
	"github.com/t0ul/krabber-net/internal/mail"
	"github.com/t0ul/krabber-net/internal/platform"
	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/web"
)

func main() {
	log := platform.NewLogger(slog.LevelInfo)
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cfg, err := config.Load(startCtx, func(region string) (config.SSMGetter, error) {
		awsCfg, err := platform.AWSConfig(startCtx, region)
		if err != nil {
			return nil, err
		}
		return ssm.NewFromConfig(awsCfg), nil
	})
	if err != nil {
		return err
	}

	awsCfg, err := platform.AWSConfig(startCtx, cfg.Region)
	if err != nil {
		return err
	}
	db := platform.DynamoDB(awsCfg, cfg.DynamoEndpoint)
	st := store.New(db, cfg.TableName)

	var sender mail.Sender = &mail.ConsoleSender{Log: log}
	if cfg.MailSender == "ses" {
		sender = &mail.SESSender{
			Client:           sesv2.NewFromConfig(awsCfg),
			From:             cfg.MailFrom,
			ConfigurationSet: cfg.MailConfiguration,
		}
	}
	mailer := mail.New(sender, st, cfg.MailDailyCap, log)

	runner := jobs.New(st, log)
	app, err := web.New(web.Deps{
		Config: cfg,
		Log:    log,
		Store:  st,
		Mailer: mailer,
		Fanout: runner,
	})
	if err != nil {
		return err
	}

	jobsCtx, stopJobs := context.WithCancel(context.Background())
	defer stopJobs()
	runner.Start(jobsCtx)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "env", cfg.Env, "table", cfg.TableName)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	stopJobs()
	runner.Wait(10 * time.Second)
	log.Info("stopped")
	return nil
}

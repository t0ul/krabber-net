package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/t0ul/krabber-net/internal/auth"
	"github.com/t0ul/krabber-net/internal/store"
)

// @system is a verified krab that molts the site's status: once when a new
// version goes live, and a daily report. Every server runs this, so each
// molt is claimed in the table first and only one server posts it.
const (
	systemName  = "system"
	systemEmail = "system@krabber.net"
	// systemReportHour is when (UTC) the daily report goes out: 9am Eastern.
	systemReportHour = 13
	systemTick       = 10 * time.Minute
)

// RunSystemKrab posts @system's molts until ctx ends.
func (app *App) RunSystemKrab(ctx context.Context) {
	c, err := app.systemKrab(ctx)
	if err != nil {
		app.log.Warn("system krab unavailable", "err", err)
		return
	}
	app.systemMolt(ctx, c, "system#version#"+app.version, 30*24*time.Hour, func() string {
		return fmt.Sprintf("A new version of Krabber just washed up on shore (%s). %%status", app.version)
	})
	posted := ""
	t := time.NewTicker(systemTick)
	defer t.Stop()
	for {
		now := time.Now().UTC()
		if day := now.Format(time.DateOnly); now.Hour() >= systemReportHour && posted != day {
			app.systemMolt(ctx, c, "system#daily#"+day, 3*24*time.Hour, func() string { return app.dailyReport(ctx, now) })
			posted = day
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// systemKrab loads @system, creating it (activated, verified, with no
// password anyone knows) the first time.
func (app *App) systemKrab(ctx context.Context) (*store.Crab, error) {
	c, err := app.store.CrabByUsername(ctx, systemName)
	switch {
	case err == nil:
		if !strings.EqualFold(c.Email, systemEmail) {
			return nil, fmt.Errorf("@%s belongs to someone else", systemName)
		}
		return c, nil
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(hex.EncodeToString(secret))
	if err != nil {
		return nil, err
	}
	if c, err = app.store.CreateCrab(ctx, systemName, systemEmail, hash); err != nil {
		return nil, fmt.Errorf("create @%s: %w", systemName, err)
	}
	if err := app.store.ActivateCrab(ctx, c.ID); err != nil {
		return nil, err
	}
	if c, err = app.store.CrabByKey(ctx, c.PK, c.SK); err != nil {
		return nil, err
	}
	if err := app.store.SetVerified(ctx, c, true); err != nil {
		return nil, err
	}
	p := store.Profile{DisplayName: "Krabber System", Bio: "Status reports from the machinery under the reef. Beep boop."}
	if err := app.store.UpdateProfile(ctx, c, p); err != nil {
		return nil, err
	}
	c.Verified, c.Profile = true, p
	app.dir.putCrab(*c)
	app.log.Info("created the system krab", "crab", c.ID)
	return c, nil
}

// systemMolt posts text() as @system unless another server (or an earlier
// run) already claimed key.
func (app *App) systemMolt(ctx context.Context, c *store.Crab, key string, keep time.Duration, text func() string) {
	first, err := app.store.ClaimOnce(ctx, key, keep)
	if err != nil || !first {
		if err != nil {
			app.log.Warn("system molt claim", "key", key, "err", err)
		}
		return
	}
	m, err := app.store.CreateMolt(ctx, c, text())
	if err != nil {
		app.log.Warn("system molt", "key", key, "err", err)
		return
	}
	app.fanout.Enqueue(m)
	app.dir.addMolt(*m)
}

// dailyReport sums up the last day from the directory, which costs nothing.
func (app *App) dailyReport(ctx context.Context, now time.Time) string {
	d := app.loadDirectory(ctx)
	since := now.Add(-24 * time.Hour)
	newKrabs := 0
	for _, c := range d.crabs {
		if c.CreatedAt.After(since) {
			newKrabs++
		}
	}
	molts := 0
	for _, m := range d.recent {
		if m.CreatedAt.After(since) && !m.Remolt {
			molts++
		}
	}
	moltCount := commas(molts)
	if len(d.recent) == directoryMolts && molts == len(d.recent) {
		moltCount += "+"
	}
	return fmt.Sprintf("Daily report from the reef: all systems nominal.\n%s krabs (%s new), %s molts in the last day. Up %s on %s. %%status",
		commas(len(d.crabs)), commas(newKrabs), moltCount, uptime(now.Sub(app.started)), app.version)
}

func uptime(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
}

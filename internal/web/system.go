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
	"github.com/t0ul/krabber-net/internal/writing"
)

// @system is a verified krab that molts the site's status: once when a new
// version goes live, and a daily report. Every server runs this, so each
// molt is claimed in the table first and only one server posts it. Its
// molts are haikus, as every krab's are by default, counted five-seven-five
// by the site's own counter.
const (
	systemName  = "system"
	systemEmail = "system@krabber.net"
	systemForm  = "haiku"
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
	app.systemMolt(ctx, c, "system#version#"+app.version, 30*24*time.Hour, func() string { return versionMolt(app.version) })
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
// password anyone knows) the first time, and finishing that setup if an
// earlier run stopped partway.
func (app *App) systemKrab(ctx context.Context) (*store.Crab, error) {
	c, err := app.store.CrabByUsername(ctx, systemName)
	switch {
	case err == nil:
		if !strings.EqualFold(c.Email, systemEmail) {
			return nil, fmt.Errorf("@%s belongs to someone else", systemName)
		}
		if c.Activated && c.Verified {
			return c, nil
		}
	case errors.Is(err, store.ErrNotFound):
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
	default:
		return nil, err
	}
	// By table key: GSI2 may not have the new krab yet.
	if err := app.store.ActivateCrabKey(ctx, c.PK, c.SK); err != nil {
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
	t, form := text(), systemForm
	// The molt skips the composer's check, so it's held to its form here: a
	// text that isn't the haiku it claims to be goes out as a plain note, or
	// not at all where every molt must take a form.
	if f, ok := writing.ByID(form); !ok || len(writing.Broken(f.Check(t), false)) > 0 {
		app.log.Warn("system molt is not a haiku", "key", key, "text", t)
		if app.cfg.FormsRequired() {
			return
		}
		form = ""
	}
	m, err := app.store.CreateMolt(ctx, c, t, store.WithForm(form))
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
	return reportMolt(commas(len(d.crabs)), commas(newKrabs), moltCount, now.Sub(app.started))
}

// systemFillers pad a line of @system's haiku that comes up short.
var systemFillers = []string{"all is well", "today", "crabs", "now"}

// versionMolt announces a new version as a haiku. The version is a commit
// hash, whose letters the counter hears as anything from no syllable to a
// few, so the middle line has a phrasing for each.
func versionMolt(version string) string {
	return writing.Haiku(systemFillers,
		[]string{"a new Krabber build"},
		[]string{
			"just washed up on the shore, " + version,
			"washed up on the shore, " + version,
			"washed up on shore, " + version,
			"washed ashore, " + version,
			"ashore: " + version,
		},
		[]string{"%status: all afloat"})
}

// reportMolt is the daily report as a haiku: all is well, the day's counts
// (digits carry no syllables, so the line holds whatever the numbers are),
// and how long this server has been up.
func reportMolt(krabs, newKrabs, molts string, up time.Duration) string {
	return writing.Haiku(systemFillers,
		[]string{"all's well on the reef"},
		[]string{fmt.Sprintf("%s krabs, %s new, %s molts since yesterday", krabs, newKrabs, molts)},
		[]string{"up " + uptime(up) + " %status", "up for " + uptime(up) + " %status"})
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

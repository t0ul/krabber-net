package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/t0ul/krabber-net/internal/mail"
	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
)

const (
	reportLimit       = 20 // reports per crab per window
	reportWindow      = time.Hour
	reportQueueLength = 100
)

type reportForm struct {
	Reason              string `form:"reason"`
	Note                string `form:"note"`
	validator.Validator `form:"-"`
}

// reportPage asks why a molt is being reported. Remolts are reported as
// their original, and nobody reports their own molt.
func (app *App) reportPage(w http.ResponseWriter, r *http.Request) {
	m, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	if m.AuthorID == currentCrab(r).ID {
		app.notFound(w)
		return
	}
	app.renderReport(w, r, http.StatusOK, m, reportForm{})
}

func (app *App) renderReport(w http.ResponseWriter, r *http.Request, status int, m *store.Molt, f reportForm) {
	data := app.newTemplateData(r)
	data.Molt = *m
	data.Form = f
	app.render(w, r, status, "report.html", data)
}

func (app *App) reportPost(w http.ResponseWriter, r *http.Request) {
	m, ok := app.moltFromPath(w, r)
	if !ok {
		return
	}
	c := currentCrab(r)
	if m.AuthorID == c.ID {
		app.notFound(w)
		return
	}
	var f reportForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	f.Note = strings.TrimSpace(strings.ReplaceAll(f.Note, "\r\n", "\n"))
	f.CheckField(store.ValidReportReason(f.Reason), "reason", "Choose a reason")
	f.CheckField(validator.MaxChars(f.Note, store.MaxReportNote), "note", "Keep it under 280 characters")
	if !f.Valid() {
		app.renderReport(w, r, http.StatusUnprocessableEntity, m, f)
		return
	}
	ok, err := app.underLimit(r, "report", c.ID, reportLimit, reportWindow)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !ok {
		f.AddFieldError("reason", "You've sent a lot of reports. Please try again in an hour.")
		app.renderReport(w, r, http.StatusTooManyRequests, m, f)
		return
	}

	msg := "Thanks for the report. A moderator will take a look. You can also block @" + m.Author + " from the molt's menu."
	switch err := app.store.ReportMolt(r.Context(), c, m, f.Reason, f.Note); {
	case errors.Is(err, store.ErrAlreadyExists):
		msg = "You've already reported this molt."
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	app.sessions.Put(r.Context(), sessionFlash, msg)
	http.Redirect(w, r, "/molt/view/"+url.PathEscape(m.ID), http.StatusSeeOther)
}

// crabminReports is the report queue, oldest first.
func (app *App) crabminReports(w http.ResponseWriter, r *http.Request) {
	reports, err := app.store.OpenReports(r.Context(), reportQueueLength)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Reports = reports
	data.Gone = app.goneCrabs(r)
	app.render(w, r, http.StatusOK, "crabmin-reports.html", data)
}

// sendBanEmail tells a crab why they were banned. A failure is logged: the
// ban itself already happened.
func (app *App) sendBanEmail(r *http.Request, c *store.Crab, reason string) {
	if c.Email == "" {
		return
	}
	err := app.mailer.Send(r.Context(), c.Email, "banned", map[string]string{
		"UserName": c.UserName,
		"Reason":   reason,
		"TermsURL": app.cfg.BaseURL.JoinPath("/terms").String(),
		"Contact":  app.cfg.ContactEmail,
	})
	if err != nil && !errors.Is(err, mail.ErrDailyCapReached) {
		app.log.Warn("ban email not sent", "err", err, "crab", c.ID)
	}
}

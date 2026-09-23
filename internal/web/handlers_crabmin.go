package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/t0ul/krabber-net/internal/store"
	"github.com/t0ul/krabber-net/internal/validator"
)

const (
	crabminLogPreview = 20
	crabminLogPage    = 200
	maxModNote        = 280
)

type modCrabForm struct {
	Action              string `form:"action"`
	Note                string `form:"note"` // ban reason or warning text
	validator.Validator `form:"-"`
}

type modMoltForm struct {
	Action              string `form:"action"`
	validator.Validator `form:"-"`
}

// crabmin is the moderation home: look up a crab or molt, see recent actions.
func (app *App) crabmin(w http.ResponseWriter, r *http.Request) {
	if q := strings.TrimPrefix(strings.TrimSpace(r.URL.Query().Get("q")), "@"); q != "" {
		if c, err := app.store.CrabByUsername(r.Context(), q); err == nil {
			http.Redirect(w, r, "/crabmin/crabs/"+url.PathEscape(c.UserName), http.StatusSeeOther)
			return
		}
		if _, err := app.store.MoltForModeration(r.Context(), q); err == nil {
			http.Redirect(w, r, "/crabmin/molts/"+url.PathEscape(q), http.StatusSeeOther)
			return
		}
		app.sessions.Put(r.Context(), sessionFlash, "No crab or molt matches “"+q+"”.")
		http.Redirect(w, r, "/crabmin", http.StatusSeeOther)
		return
	}
	app.renderModLog(w, r, "crabmin.html", crabminLogPreview)
}

func (app *App) crabminLog(w http.ResponseWriter, r *http.Request) {
	app.renderModLog(w, r, "crabmin-log.html", crabminLogPage)
}

func (app *App) renderModLog(w http.ResponseWriter, r *http.Request, page string, limit int) {
	entries, err := app.store.ModLog(r.Context(), limit)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.ModLog = entries
	app.render(w, r, http.StatusOK, page, data)
}

func (app *App) crabminCrab(w http.ResponseWriter, r *http.Request) {
	c, err := app.store.CrabByUsername(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		app.notFound(w)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	molts, err := app.store.MoltsByOwner(r.Context(), c.ID, pageSize)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Profile = c
	data.Molts = molts
	data.CanModerate = app.canModerate(currentCrab(r), c) == nil
	app.render(w, r, http.StatusOK, "crabmin-crab.html", data)
}

func (app *App) crabminMolt(w http.ResponseWriter, r *http.Request) {
	m, err := app.store.MoltForModeration(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		app.notFound(w)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	data := app.newTemplateData(r)
	data.Molt = *m
	if author, err := app.store.CrabByID(r.Context(), m.AuthorID); err == nil {
		data.Profile = author
		data.CanModerate = app.canModerate(currentCrab(r), author) == nil
	}
	app.render(w, r, http.StatusOK, "crabmin-molt.html", data)
}

var errModForbidden = errors.New("only an admin can moderate a moderator, and admins can't be moderated from the web")

// canModerate applies Crabber's rule (moderators can't act on moderators) and
// one more: nobody acts on an admin from the web, so an admin can't be locked
// out with a stolen session.
func (app *App) canModerate(mod, target *store.Crab) error {
	switch {
	case mod.ID == target.ID:
		return errors.New("you can't moderate yourself")
	case target.Deleted:
		return errors.New("the account is deleted")
	case target.IsAdmin():
		return errModForbidden
	case target.IsModerator() && !mod.IsAdmin():
		return errModForbidden
	}
	return nil
}

func (app *App) crabminCrabPost(w http.ResponseWriter, r *http.Request) {
	var f modCrabForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	target, err := app.store.CrabByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		app.notFound(w)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	mod := currentCrab(r)
	back := "/crabmin/crabs/" + url.PathEscape(target.UserName)
	done := func(msg string) {
		app.sessions.Put(r.Context(), sessionFlash, msg)
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
	entry := store.ModAction{ModeratorID: mod.ID, Moderator: mod.UserName, Action: f.Action, CrabID: target.ID, Crab: target.UserName}

	if err := app.canModerate(mod, target); err != nil {
		entry.Action = "attempted_" + f.Action
		app.logMod(r, entry)
		done("Not allowed: " + err.Error() + ".")
		return
	}
	f.Note = strings.TrimSpace(f.Note)
	if !validator.MaxChars(f.Note, maxModNote) {
		done("Keep the note under 280 characters.")
		return
	}

	ctx := r.Context()
	var msg string
	switch f.Action {
	case "ban":
		if f.Note == "" {
			done("Give a reason for the ban.")
			return
		}
		err = app.store.SetBanned(ctx, target, true, f.Note)
		entry.Note = f.Note
		app.dir.setGone(target.ID, true)
		msg = "@" + target.UserName + " is banned and signed out everywhere."
	case "unban":
		err = app.store.SetBanned(ctx, target, false, "")
		app.dir.setGone(target.ID, false)
		msg = "@" + target.UserName + " is unbanned."
	case "warn":
		if f.Note == "" {
			done("Write the warning.")
			return
		}
		err = app.store.AddNotification(ctx, store.Notification{
			RecipientID: target.ID,
			Type:        store.NotifyWarning,
			Actor:       "Krabber moderators",
			Snippet:     f.Note,
		})
		entry.Note = f.Note
		msg = "Warning sent to @" + target.UserName + "."
	case "clear_display_name", "clear_bio", "clear_location", "clear_website":
		p := target.Profile
		field := strings.TrimPrefix(f.Action, "clear_")
		switch field {
		case "display_name":
			entry.Note, p.DisplayName = p.DisplayName, ""
		case "bio":
			entry.Note, p.Bio = p.Bio, ""
		case "location":
			entry.Note, p.Location = p.Location, ""
		case "website":
			entry.Note, p.Website = p.Website, ""
		}
		err = app.store.UpdateProfile(ctx, target, p)
		app.dir.invalidate()
		msg = "Cleared @" + target.UserName + "'s " + strings.ReplaceAll(field, "_", " ") + "."
	case "make_moderator", "remove_moderator":
		if !mod.IsAdmin() {
			entry.Action = "attempted_" + f.Action
			app.logMod(r, entry)
			done("Only admins can appoint moderators.")
			return
		}
		role, verb := store.RoleModerator, " is now a moderator."
		if f.Action == "remove_moderator" {
			role, verb = "", " is no longer a moderator."
		}
		err = app.store.SetRole(ctx, target, role)
		msg = "@" + target.UserName + verb
	default:
		app.clientError(w, http.StatusBadRequest)
		return
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.logMod(r, entry)
	done(msg)
}

func (app *App) crabminMoltPost(w http.ResponseWriter, r *http.Request) {
	var f modMoltForm
	if err := app.decodePostForm(w, r, &f); err != nil {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	m, err := app.store.MoltForModeration(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		app.notFound(w)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if f.Action != "remove" && f.Action != "restore" {
		app.clientError(w, http.StatusBadRequest)
		return
	}
	mod := currentCrab(r)
	back := "/crabmin/molts/" + url.PathEscape(m.ID)
	entry := store.ModAction{ModeratorID: mod.ID, Moderator: mod.UserName, Action: f.Action + "_molt", CrabID: m.AuthorID, Crab: m.Author, MoltID: m.ID, Note: store.Snippet(m.Content)}
	author, err := app.store.CrabByID(r.Context(), m.AuthorID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		app.serverError(w, r, err)
		return
	}
	if author != nil {
		if err := app.canModerate(mod, author); err != nil {
			entry.Action = "attempted_" + entry.Action
			app.logMod(r, entry)
			app.sessions.Put(r.Context(), sessionFlash, "Not allowed: "+err.Error()+".")
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
	}
	if err := app.store.SetMoltRemoved(r.Context(), m, f.Action == "remove"); err != nil {
		app.serverError(w, r, err)
		return
	}
	if f.Action == "remove" {
		app.dir.removeMolt(m.ID)
	} else {
		app.dir.invalidate()
	}
	app.logMod(r, entry)
	app.sessions.Put(r.Context(), sessionFlash, "Molt "+map[bool]string{true: "removed", false: "restored"}[f.Action == "remove"]+".")
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// logMod records a moderation action; a failed write is logged, not fatal,
// because the action itself already happened.
func (app *App) logMod(r *http.Request, a store.ModAction) {
	if err := app.store.LogModAction(r.Context(), a); err != nil {
		app.log.Error("mod log write failed", "err", err, "action", a.Action, "crab", a.CrabID)
	}
}

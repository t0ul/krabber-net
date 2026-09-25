package web

import (
	"net/http"
	"slices"
	"time"

	"github.com/t0ul/krabber-net/internal/store"
)

// statsPage is Crabber's Stats page, worked out from the directory snapshot
// (every active crab and the last week's molts), so it costs no extra reads.
type statsPage struct {
	Crabs       int
	Molts       int // molts, replies and remolts crabs have now
	Follows     int
	Trophies    int
	WeekMolts   int
	WeekMore    bool // the week has more molts than the snapshot holds
	King        *store.Crab
	Starter     *store.Crab // most invites; nil until someone joins with a code
	Baby        *store.Crab
	BabyDays    int
	Best        *store.Molt
	Talked      *store.Molt
	Trendy      string
	TrendyMolts []store.Molt
}

func (app *App) statsPage(w http.ResponseWriter, r *http.Request) {
	crabs, _, recent := app.snapshot(r)
	hidden := app.hidden(r)
	var st statsPage
	for i := range crabs {
		c := &crabs[i]
		st.Crabs++
		st.Molts += c.MoltCount
		st.Follows += c.FollowingCount
		st.Trophies += c.Trophies
		if hidden(c.ID) {
			continue
		}
		if st.King == nil || c.FollowerCount > st.King.FollowerCount ||
			(c.FollowerCount == st.King.FollowerCount && c.CreatedAt.Before(st.King.CreatedAt)) {
			st.King = c
		}
		if st.Baby == nil || c.CreatedAt.After(st.Baby.CreatedAt) {
			st.Baby = c
		}
		if c.Invites > 0 && (st.Starter == nil || c.Invites > st.Starter.Invites) {
			st.Starter = c
		}
	}
	if st.Baby != nil {
		st.BabyDays = int(time.Since(st.Baby.CreatedAt).Hours() / 24)
	}

	var originals []store.Molt
	for _, m := range recent {
		if !m.Remolt {
			originals = append(originals, m)
		}
	}
	st.WeekMolts, st.WeekMore = len(originals), len(recent) >= directoryMolts
	originals = withoutMuted(r, app.visibleMolts(r, originals))

	var picks []store.Molt
	best, talked := -1, -1
	for i, m := range originals {
		if m.LikeCount > 0 && (best < 0 || m.LikeCount > originals[best].LikeCount) {
			best = i
		}
		if m.ReplyCount > 0 && (talked < 0 || m.ReplyCount > originals[talked].ReplyCount) {
			talked = i
		}
	}
	if best >= 0 {
		picks = append(picks, originals[best])
	}
	if talked >= 0 {
		picks = append(picks, originals[talked])
	}
	var trendyIDs []string
	if tags := trendingTags(originals); len(tags) > 0 {
		st.Trendy = tags[0].Name
		var tagged []store.Molt
		for _, m := range originals {
			if slices.Contains(m.Tags, st.Trendy) {
				tagged = append(tagged, m)
			}
		}
		slices.SortStableFunc(tagged, func(a, b store.Molt) int { return b.LikeCount - a.LikeCount })
		for _, m := range tagged[:min(3, len(tagged))] {
			trendyIDs = append(trendyIDs, m.ID)
			picks = append(picks, m)
		}
	}

	shown, err := app.withLikes(r, picks)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	byID := make(map[string]*store.Molt, len(shown))
	for i := range shown {
		byID[shown[i].ID] = &shown[i]
	}
	if best >= 0 {
		st.Best = byID[originals[best].ID]
	}
	if talked >= 0 {
		st.Talked = byID[originals[talked].ID]
	}
	for _, id := range trendyIDs {
		if m, ok := byID[id]; ok {
			st.TrendyMolts = append(st.TrendyMolts, *m)
		}
	}

	data := app.newTemplateData(r)
	data.Stats = &st
	app.render(w, r, http.StatusOK, "stats.html", data)
}

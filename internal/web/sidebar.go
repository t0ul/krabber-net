package web

import (
	"net/http"
	"sort"
	"strings"

	"github.com/t0ul/krabber-net/internal/richtext"
	"github.com/t0ul/krabber-net/internal/store"
)

const (
	whoToFollowCount  = 3
	trendingCount     = 3
	searchResultLimit = 25
)

type sidebar struct {
	WhoToFollow []crabRow
	Trending    []trendingTag
}

type trendingTag struct {
	Name  string
	Crabs int // distinct crabs who used it recently
}

// following returns the IDs the viewer follows (empty when signed out).
func (app *App) following(r *http.Request) map[string]bool {
	c := currentCrab(r)
	if c == nil {
		return map[string]bool{}
	}
	ids, err := app.store.FollowingIDs(r.Context(), c.ID)
	if err != nil {
		app.log.Warn("following ids", "err", err)
		return map[string]bool{}
	}
	return ids
}

func (app *App) sidebarFor(r *http.Request) sidebar {
	crabs, byID, recent := app.snapshot(r)
	var sb sidebar

	me := ""
	if c := currentCrab(r); c != nil {
		me = c.ID
	}
	followed := app.following(r)
	blocks := blocksOf(r)
	// Friends of friends first, as Crabber does, then the most followed crabs
	// the viewer doesn't follow, then the rest.
	var candidates []store.Crab
	picked := map[string]bool{}
	for _, id := range app.friendsOfFriends(r, me, followed) {
		if c, ok := byID[id]; ok && !blocks.Hides(id) && !followed[id] {
			candidates = append(candidates, c)
			picked[id] = true
		}
		if len(candidates) == whoToFollowCount {
			break
		}
	}
	// The directory lists krabs most followed first, so the walk usually
	// stops after a few krabs; followed krabs fill in only when there's
	// nobody else.
	var rest []store.Crab
	for _, c := range crabs {
		if len(candidates) == whoToFollowCount {
			break
		}
		switch {
		case c.ID == me || blocks.Hides(c.ID) || picked[c.ID]:
		case followed[c.ID]:
			if len(rest) < whoToFollowCount {
				rest = append(rest, c)
			}
		default:
			candidates = append(candidates, c)
		}
	}
	candidates = append(candidates, rest...)
	for _, c := range candidates[:min(whoToFollowCount, len(candidates))] {
		sb.WhoToFollow = append(sb.WhoToFollow, crabRow{Crab: c, Following: followed[c.ID]})
	}

	hidden := app.hidden(r)
	sb.Trending = trendingTags(recent, func(m store.Molt) bool { return hidden(m.OwnerID) || hidden(m.AuthorID) })
	return sb
}

// trendingTags ranks the crabtags in molts by how many different crabs used
// them, as Crabber does over the last week. Molts that skip reports true for
// aren't counted (nil counts them all).
func trendingTags(molts []store.Molt, skip func(store.Molt) bool) []trendingTag {
	users := map[string]map[string]bool{}
	for _, m := range molts {
		if skip != nil && skip(m) {
			continue
		}
		for _, tag := range m.Tags {
			if users[tag] == nil {
				users[tag] = map[string]bool{}
			}
			users[tag][m.AuthorID] = true
		}
	}
	tags := make([]trendingTag, 0, len(users))
	for name, by := range users {
		tags = append(tags, trendingTag{Name: name, Crabs: len(by)})
	}
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Crabs != tags[j].Crabs {
			return tags[i].Crabs > tags[j].Crabs
		}
		return tags[i].Name < tags[j].Name
	})
	return tags[:min(trendingCount, len(tags))]
}

// search matches crabs by name and the last week's molts by content.
func (app *App) search(r *http.Request, q string) ([]crabRow, []store.Molt) {
	crabs, _, recent := app.snapshot(r)
	needle := strings.ToLower(q)
	followed := app.following(r)

	var rows []crabRow
	for _, c := range crabs {
		if strings.Contains(strings.ToLower(c.UserName), needle) {
			rows = append(rows, crabRow{Crab: c, Following: followed[c.ID]})
			if len(rows) == searchResultLimit {
				break
			}
		}
	}
	// Molts show crab as krab, so search matches the way they read.
	shown := strings.ToLower(richtext.Krabify(q))
	var molts []store.Molt
	for _, m := range recent {
		if !m.Remolt && strings.Contains(strings.ToLower(richtext.Krabify(m.Content)), shown) {
			molts = append(molts, m)
			if len(molts) == searchResultLimit {
				break
			}
		}
	}
	return rows, molts
}

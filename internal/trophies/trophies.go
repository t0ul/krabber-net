// Package trophies is Krabber's trophy catalog, after Crabber's
// trophies.json, and the rules for earning the automatic ones. Awarding and
// storage live in the store; this package only answers "which trophies does
// this event earn?".
package trophies

import (
	"slices"
	"strings"
	"time"
)

// Trophy is one entry in the catalog. Manual trophies are only handed out by
// moderators (or crabctl); the rest are earned automatically.
type Trophy struct {
	ID          string
	Title       string
	Description string
	Gold        bool // the gold cup; silver otherwise
	Manual      bool
}

// All is the catalog in the order a trophy case shows it.
var All = []Trophy{
	{ID: "social-newbie", Title: "Social Newbie", Description: "Get your first follower"},
	{ID: "mingler", Title: "Mingler", Description: "Have 10 followers"},
	{ID: "life-of-the-party", Title: "Life of the Party", Description: "Have 100 followers"},
	{ID: "celebrity", Title: "Celebrity", Description: "Have 1,000 followers", Gold: true},
	{ID: "baby-krab", Title: "Baby Krab", Description: "Publish your first molt"},
	{ID: "loudmouth", Title: "Loudmouth", Description: "Post 1,000 molts", Gold: true},
	{ID: "please-stop", Title: "Please Stop", Description: "Post 10,000 molts", Gold: true},
	{ID: "dopamine-hit", Title: "Dopamine Hit", Description: "Get 10 likes on a molt"},
	{ID: "dopamine-addict", Title: "Dopamine Addict", Description: "Get 100 likes on a molt. Need... more...", Gold: true},
	{ID: "full-on-junkie", Title: "Full-On Junkie", Description: "Get 1,000 likes on a molt", Gold: true},
	{ID: "twenty-twenty", Title: "20/20", Description: "Have 20 times as many followers as krabs you follow, following at least 20"},
	{ID: "golden-ratio", Title: "The Golden Ratio", Description: "Have 100 times as many followers as krabs you follow, following at least 20", Gold: true},
	{ID: "captivated", Title: "I Captivated the Guy", Description: "Be followed by a verified krab", Gold: true},
	{ID: "back-krabber", Title: "Back-Krabber", Description: "Follow someone back only to have them unfollow you"},
	{ID: "i-want-it-that-way", Title: "I Want it That Way", Description: "Fill in your display name, bio, location or website, and a fun fact"},
	{ID: "pineapple-express", Title: "Pineapple Express", Description: "Find the hidden krabtag"},
	{ID: "mega-freakoid", Title: "Mega Freakoid", Description: "Find the hidden krabtag"},
	{ID: "cheezburger", Title: "i can haz cheezburger?", Description: "Find the hidden krabtag"},
	{ID: "f7u12", Title: "f7u12", Description: "Find the hidden krabtag"},
	{ID: "back-to-the-future", Title: "Back to the Future", Description: "Find the hidden krabtag"},
	{ID: "rogen", Title: "Rogen Out of Control", Description: "heh heh heh heh"},
	{ID: "one-year", Title: "One Year", Description: "Be a krab for a whole year", Gold: true},
	{ID: "unlimited-power", Title: "Unlimited Power!", Description: "Serve on Krabber's moderation team", Gold: true},
	{ID: "lab-rat", Title: "Lab Rat", Description: "Take part in Krabber's beta", Manual: true},
	{ID: "contributor", Title: "Krabber Contributor", Description: "Contribute code to Krabber", Gold: true, Manual: true},
	{ID: "pentester", Title: "Pentester", Description: "Find and report a security vulnerability", Gold: true, Manual: true},
	{ID: "universal-donor", Title: "Universal Donor", Description: "Donate to Krabber <3", Gold: true, Manual: true},
}

var byID = func() map[string]Trophy {
	m := make(map[string]Trophy, len(All))
	for _, t := range All {
		m[t.ID] = t
	}
	return m
}()

// Get returns the trophy with id.
func Get(id string) (Trophy, bool) {
	t, ok := byID[id]
	return t, ok
}

// Image is the trophy's picture, relative to /static/.
func (t Trophy) Image() string {
	switch {
	case t.ID == "one-year":
		return "img/trophies/one_year.png"
	case t.Gold:
		return "img/default_trophy.png"
	default:
		return "img/default_trophy_silver.png"
	}
}

// Manual lists the trophies only moderators hand out.
func Manual() []Trophy {
	var out []Trophy
	for _, t := range All {
		if t.Manual {
			out = append(out, t)
		}
	}
	return out
}

type milestone struct {
	at int
	id string
}

var (
	followerMilestones = []milestone{{1, "social-newbie"}, {10, "mingler"}, {100, "life-of-the-party"}, {1000, "celebrity"}}
	moltMilestones     = []milestone{{1, "baby-krab"}, {1000, "loudmouth"}, {10000, "please-stop"}}
	likeMilestones     = []milestone{{10, "dopamine-hit"}, {100, "dopamine-addict"}, {1000, "full-on-junkie"}}
	hiddenTags         = map[string]string{
		"420":                 "pineapple-express",
		"waaahhhh":            "mega-freakoid",
		"lolcat":              "cheezburger",
		"fffffffuuuuuuuuuuuu": "f7u12",
		"1985":                "back-to-the-future",
	}
)

// crossed returns the milestones a count reached by going from before to
// after. Only crossings earn a trophy, so the award (a conditional write) is
// tried once, not on every later like or follow.
func crossed(ms []milestone, before, after int) []string {
	var ids []string
	for _, m := range ms {
		if before < m.at && after >= m.at {
			ids = append(ids, m.id)
		}
	}
	return ids
}

// ForFollowers is what a new follower earns: follower milestones and the
// follow-ratio trophies.
func ForFollowers(followersBefore, followersAfter, following int) []string {
	ids := crossed(followerMilestones, followersBefore, followersAfter)
	if following >= 20 {
		ratio := func(n int) float64 { return float64(n) / float64(following) }
		if ratio(followersBefore) < 20 && ratio(followersAfter) >= 20 {
			ids = append(ids, "twenty-twenty")
		}
		if ratio(followersBefore) < 100 && ratio(followersAfter) >= 100 {
			ids = append(ids, "golden-ratio")
		}
	}
	return ids
}

// ForMolts is what posting a molt earns: molt-count milestones and hidden
// krabtags (tags are the molt's lowercase tags).
func ForMolts(countBefore, countAfter int, tags []string) []string {
	ids := crossed(moltMilestones, countBefore, countAfter)
	for _, tag := range tags {
		if id, ok := hiddenTags[tag]; ok && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// ForLikes is what the author earns when a molt's like count goes up.
func ForLikes(before, after int) []string { return crossed(likeMilestones, before, after) }

// Rogen reports whether liking this molt earns the liker "Rogen Out of Control".
func Rogen(content string, tags []string) bool {
	return strings.Contains(strings.ToLower(content), "seth rogen") || slices.Contains(tags, "sethrogen")
}

// CustomizedProfile reports whether a profile earns "I Want it That Way".
func CustomizedProfile(displayName, bio, location, website string, anyFunFact bool) bool {
	return displayName != "" && bio != "" && (location != "" || website != "") && anyFunFact
}

// Anniversary reports whether an account created at joined is in the week
// after its first anniversary, when the daily award show gives "One Year".
// The window lets a missed day catch up without retrying old accounts forever.
func Anniversary(joined, now time.Time) bool {
	year := joined.AddDate(1, 0, 0)
	return !now.Before(year) && now.Before(year.AddDate(0, 0, 7))
}

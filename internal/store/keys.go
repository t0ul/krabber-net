package store

import (
	"fmt"
	"strings"
	"time"
)

// Every key format in the table lives here. The table is single-table design:
// PK/SK on the base table plus sparse GSIs, each used by one access pattern.
//
//	Entity            PK                     SK                   Index keys
//	Crab              C#<email>              C#                   GSI2 ID#<id> / ID#<id>
//	Username marker   U#<username>           U#
//	Molt              M#<authorCrabID>       M#<moltID>           GSI3 M#<yyyy-mm-dd> / M#<moltID>
//	                                                              GSI5 M#<moltID> / M#<moltID>
//	                                                              GSI8 Q#fanout / <moltID> (while pending)
//	Remolt marker     RM#<crabID>            RM#<moltID>
//	Comment           MC#<moltID>            MC#<commentID>
//	Like              L#<crabID>             L#<moltID>           GSI7 L#<moltID> / L#<crabID>
//	Follow            F#<followerID>         F#<followeeID>       GSI6 F#<followeeID> / F#<followerID>
//	Trench entry      T#<crabID>             T#<moltID>
//	Token             CT#<sha256 hex>        CT#<scope>
//	Session           S#<sha256 hex>         S#
//	Rate-limit window RL#<action>#<key>      RL#<window unix>
//
// Molt, comment and other generated IDs are KSUIDs, so sorting by SK sorts by time.

const (
	gsiCrabByID     = "GSI2"
	gsiMoltsByDay   = "GSI3"
	gsiMoltByID     = "GSI5"
	gsiFollowers    = "GSI6"
	gsiLikesOnMolt  = "GSI7"
	gsiWorkQueue    = "GSI8"
	queueFanout     = "Q#fanout"
	trenchRetention = 90 * 24 * time.Hour
)

func crabPK(email string) string     { return "C#" + normalizeEmail(email) }
func crabSK() string                 { return "C#" }
func crabIDKey(id string) string     { return "ID#" + id }
func usernamePK(name string) string  { return "U#" + strings.ToLower(name) }
func usernameSK() string             { return "U#" }
func moltPK(authorID string) string  { return "M#" + authorID }
func moltSK(moltID string) string    { return "M#" + moltID }
func moltIDKey(moltID string) string { return "M#" + moltID }
func moltDayKey(t time.Time) string  { return "M#" + t.UTC().Format(time.DateOnly) }
func remoltMarkerPK(crabID string) string {
	return "RM#" + crabID
}
func remoltMarkerSK(moltID string) string      { return "RM#" + moltID }
func commentPK(moltID string) string           { return "MC#" + moltID }
func commentSK(commentID string) string        { return "MC#" + commentID }
func likePK(crabID string) string              { return "L#" + crabID }
func likeSK(moltID string) string              { return "L#" + moltID }
func likesOnKey(moltID string) string          { return "L#" + moltID }
func followPK(followerID string) string        { return "F#" + followerID }
func followSK(followeeID string) string        { return "F#" + followeeID }
func followersKey(followeeID string) string    { return "F#" + followeeID }
func trenchPK(crabID string) string            { return "T#" + crabID }
func trenchSK(moltID string) string            { return "T#" + moltID }
func tokenPK(hash string) string               { return "CT#" + hash }
func tokenSK(scope string) string              { return "CT#" + scope }
func sessionPK(hash string) string             { return "S#" + hash }
func sessionSK() string                        { return "S#" }
func rateLimitPK(action, key string) string    { return "RL#" + action + "#" + key }
func rateLimitSK(windowStart time.Time) string { return fmt.Sprintf("RL#%d", windowStart.Unix()) }

// NormalizeEmail lowercases and trims an email address so that one mailbox
// maps to exactly one account.
func NormalizeEmail(email string) string { return normalizeEmail(email) }

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

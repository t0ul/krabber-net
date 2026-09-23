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
//	Deleted crab      C#deleted#<id>         C#                   GSI2 as above; GSI8 Q#purge / <id> (until purged)
//	Username marker   U#<username>           U#
//	Molt              M#<authorCrabID>       M#<moltID>           GSI3 M#<yyyy-mm-dd> / M#<moltID>
//	                                                              GSI5 M#<moltID> / M#<moltID>
//	                                                              GSI8 Q#fanout / <moltID> (while pending)
//	Reply             M#<authorCrabID>       MR#<moltID>          GSI5 as for molts (no day or fan-out keys:
//	                                                              replies stay out of the Sea and trenches)
//	Reply pointer     RP#<parentMoltID>      RP#<replyMoltID>
//	Quote             a molt (keys as above) with quote_of set
//	Quote pointer     QP#<quotedMoltID>      QP#<quoteMoltID>
//	Crabtag pointer   TG#<lowercase tag>     TG#<moltID>
//	Remolt marker     RM#<crabID>            RM#<moltID>
//	Like              L#<crabID>             L#<moltID>           GSI7 L#<moltID> / L#<crabID>
//	Bookmark marker   BK#<crabID>            BK#<moltID>          (holds list_sk, the entry below)
//	Bookmark entry    BL#<crabID>            BL#<ksuid>           (sorted by when the crab bookmarked)
//	Follow            F#<followerID>         F#<followeeID>       GSI6 F#<followeeID> / F#<followerID>
//	Trench entry      T#<crabID>             T#<moltID>
//	Token             CT#<sha256 hex>        CT#<scope>
//	Session           S#<sha256 hex>         S#
//	Rate-limit window RL#<action>#<key>      RL#<window unix>
//	Notification      N#<recipientID>        N#<notificationID>
//	Notified-once     NO#<recipientID>       <type>#<actorID>#<moltID>
//	Unread counter    NC#<crabID>            NC#
//	Block             B#<crabID>             B#out#<otherID> (blocking) / B#in#<otherID> (blocked by)
//	Moderation log    ML#<yyyy-mm>           ML#<ksuid>
//	Report queue      R#<moltID>             R#                   GSI8 Q#report / <ksuid> (while open)
//	Report            R#<moltID>             R#by#<reporterID>
//
// Molt and other generated IDs are KSUIDs, so sorting by SK sorts by time.
// Replies use their own SK prefix so a crab's molts and replies are each one
// begins_with query on the same partition.

const (
	gsiCrabByID     = "GSI2"
	gsiMoltsByDay   = "GSI3"
	gsiMoltByID     = "GSI5"
	gsiFollowers    = "GSI6"
	gsiLikesOnMolt  = "GSI7"
	gsiWorkQueue    = "GSI8"
	queueFanout     = "Q#fanout"
	queuePurge      = "Q#purge"
	queueReports    = "Q#report"
	trenchRetention = 90 * 24 * time.Hour
)

func crabPK(email string) string     { return "C#" + normalizeEmail(email) }
func crabSK() string                 { return "C#" }
func deletedCrabPK(id string) string { return "C#deleted#" + id } // no "@", so never an email key
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
func replySK(moltID string) string             { return "MR#" + moltID }
func replyPointerPK(parentID string) string    { return "RP#" + parentID }
func replyPointerSK(replyID string) string     { return "RP#" + replyID }
func quotePointerPK(quotedID string) string    { return "QP#" + quotedID }
func quotePointerSK(quoteID string) string     { return "QP#" + quoteID }
func tagPK(tag string) string                  { return "TG#" + tag }
func tagSK(moltID string) string               { return "TG#" + moltID }
func likePK(crabID string) string              { return "L#" + crabID }
func likeSK(moltID string) string              { return "L#" + moltID }
func likesOnKey(moltID string) string          { return "L#" + moltID }
func bookmarkPK(crabID string) string          { return "BK#" + crabID }
func bookmarkSK(moltID string) string          { return "BK#" + moltID }
func bookmarkListPK(crabID string) string      { return "BL#" + crabID }
func bookmarkListSK(id string) string          { return "BL#" + id }
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
func notificationPK(crabID string) string      { return "N#" + crabID }
func notificationSK(id string) string          { return "N#" + id }
func notificationOncePK(crabID string) string  { return "NO#" + crabID }
func notificationOnceSK(kind, actorID, moltID string) string {
	return kind + "#" + actorID + "#" + moltID
}
func notificationCounterPK(crabID string) string { return "NC#" + crabID }
func notificationCounterSK() string              { return "NC#" }
func blockPK(crabID string) string               { return "B#" + crabID }
func blockOutSK(otherID string) string           { return "B#out#" + otherID }
func blockInSK(otherID string) string            { return "B#in#" + otherID }
func modLogPK(t time.Time) string                { return "ML#" + t.UTC().Format("2006-01") }
func modLogSK(id string) string                  { return "ML#" + id }
func reportPK(moltID string) string              { return "R#" + moltID }
func reportSummarySK() string                    { return "R#" }
func reportRowSK(reporterID string) string       { return "R#by#" + reporterID }

// NormalizeEmail lowercases and trims an email address so that one mailbox
// maps to exactly one account.
func NormalizeEmail(email string) string { return normalizeEmail(email) }

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

package rules

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/t0ul/krabber-net/engine/store"
)

// FanoutQueue hands new posts to the background fan-out worker, which
// writes them into every follower's home feed.
type FanoutQueue interface {
	Enqueue(m *store.Molt)
}

// Notifier hands notifications to the background writer.
type Notifier interface {
	Notify(n store.Notification)
}

// CardQueue asks the background worker for a link card.
type CardQueue interface {
	EnqueueCard(url string)
}

// Hides says whether the acting krab has blocked (or been blocked by) a
// krab, so nothing of theirs reaches the other. nil hides no one.
type Hides func(crabID string) bool

// Service is the write side every front end shares: what a post, a reply,
// a like or a follow sets in motion beyond the table write itself.
type Service struct {
	Store    *store.Store
	Log      *slog.Logger
	Fanout   FanoutQueue
	Notifier Notifier  // nil: no notifications
	Cards    CardQueue // nil: no link cards
	Awards   *Awards
}

// Deps are the collaborators a Service needs.
type Deps struct {
	Store    *store.Store
	Log      *slog.Logger
	Fanout   FanoutQueue
	Notifier Notifier
	Cards    CardQueue
}

// New builds the Service.
func New(d Deps) *Service {
	return &Service{
		Store:    d.Store,
		Log:      d.Log,
		Fanout:   d.Fanout,
		Notifier: d.Notifier,
		Cards:    d.Cards,
		Awards:   &Awards{Store: d.Store, Log: d.Log},
	}
}

// Draft is what a writer sends: the text, its form, the layout they asked
// for (the form's or their default when "" or unknown), and the NSFW label.
type Draft struct {
	Content string
	Form    string
	Layout  string
	NSFW    bool
}

func (d Draft) options(author *store.Crab) []store.MoltOption {
	return []store.MoltOption{store.WithNSFW(d.NSFW), store.WithForm(d.Form), store.WithLayout(LayoutFor(author, d.Form, d.Layout))}
}

// Post stores a new post by author and publishes it. The draft must already
// have passed CheckDraft and the front end's write limit.
func (s *Service) Post(ctx context.Context, author *store.Crab, d Draft, hides Hides) (*store.Molt, error) {
	m, err := s.Store.CreateMolt(ctx, author, d.Content, d.options(author)...)
	if err != nil {
		return nil, err
	}
	s.Publish(ctx, author, m, "", hides)
	return m, nil
}

// Publish sends a post author just wrote to the feeds and tells the krabs
// it mentions (except skipMention). Author's own home feed is written here
// so a refresh shows the post at once; follower fan-out stays in the
// background (and writes the author again). author's counts are from
// before the post.
func (s *Service) Publish(ctx context.Context, author *store.Crab, m *store.Molt, skipMention string, hides Hides) {
	if err := s.Store.AddToTrenches(ctx, m, []string{m.OwnerID}); err != nil {
		s.Log.Warn("write own trench", "err", err, "molt", m.ID)
	}
	s.Fanout.Enqueue(m)
	s.EnqueueCard(CardURL(m.Content))
	s.NotifyMentions(ctx, author, hides, m, skipMention)
	s.Awards.Molt(ctx, author, m)
}

// Reply stores author's reply to parent and tells parent's author and the
// krabs it mentions. Errors are the table's: ErrNotFound or ErrNotAllowed
// when the parent is gone or closed to author.
func (s *Service) Reply(ctx context.Context, author *store.Crab, parent *store.Molt, d Draft, hides Hides) (*store.Molt, error) {
	reply, err := s.Store.Reply(ctx, author, parent, d.Content, d.options(author)...)
	if err != nil {
		return nil, err
	}
	s.Notify(author, hides, parent.AuthorID, store.NotifyReply, reply.ID, d.Content)
	s.NotifyMentions(ctx, author, hides, reply, parent.AuthorID)
	s.EnqueueCard(CardURL(reply.Content))
	return reply, nil
}

// Quote stores author's quote of quoted, publishes it, and tells quoted's
// author.
func (s *Service) Quote(ctx context.Context, author *store.Crab, quoted *store.Molt, d Draft, hides Hides) (*store.Molt, error) {
	m, err := s.Store.Quote(ctx, author, quoted, d.Content, d.options(author)...)
	if err != nil {
		return nil, err
	}
	s.Publish(ctx, author, m, quoted.AuthorID, hides)
	s.Notify(author, hides, quoted.AuthorID, store.NotifyQuote, m.ID, d.Content)
	return m, nil
}

// Like toggles liker's like of m, and on a new like tells m's author and
// gives any trophy it earns. It reports whether m is liked now.
func (s *Service) Like(ctx context.Context, liker *store.Crab, m *store.Molt, hides Hides) (bool, error) {
	liked, err := s.Store.ToggleLike(ctx, liker, m)
	if err != nil {
		return false, err
	}
	if liked {
		s.Notify(liker, hides, m.AuthorID, store.NotifyLike, m.ID, m.Content)
		s.Awards.Like(ctx, liker, m)
	}
	return liked, nil
}

// Remolt shares m into by's followers' feeds (sharing a share shares the
// original) and tells m's author. Errors are the table's: ErrAlreadyExists
// when by already shared it, ErrNotAllowed when they may not.
func (s *Service) Remolt(ctx context.Context, by *store.Crab, m *store.Molt, hides Hides) (*store.Molt, error) {
	re, err := s.Store.Remolt(ctx, by, m)
	if err != nil {
		return nil, err
	}
	s.Fanout.Enqueue(re)
	s.Notify(by, hides, m.AuthorID, store.NotifyRemolt, m.ID, m.Content)
	return re, nil
}

// backfillMolts is how many of a newly followed krab's posts go straight
// into the follower's home feed (one batch write).
const backfillMolts = 10

// Follow makes follower follow followee, tells followee, gives any trophy
// it earns, and puts followee's latest posts in follower's home feed.
// Errors are the table's (ErrAlreadyExists, ErrNotAllowed, ErrBlocked…).
func (s *Service) Follow(ctx context.Context, follower, followee *store.Crab, hides Hides) error {
	if err := s.Store.Follow(ctx, follower, followee); err != nil {
		return err
	}
	s.Notify(follower, hides, followee.ID, store.NotifyFollow, "", "")
	s.Awards.Follow(ctx, follower, followee, true)
	s.backfill(ctx, follower, followee)
	return nil
}

// Unfollow undoes a follow and gives followee any trophy that earns.
func (s *Service) Unfollow(ctx context.Context, follower, followee *store.Crab) error {
	if err := s.Store.Unfollow(ctx, follower, followee); err != nil {
		return err
	}
	s.Awards.Follow(ctx, follower, followee, false)
	return nil
}

// backfill puts followee's latest posts in follower's home feed, which
// otherwise only gets what they post from now on. It's a bonus, so a
// failure is only logged.
func (s *Service) backfill(ctx context.Context, follower, followee *store.Crab) {
	molts, err := s.Store.MoltsByOwner(ctx, followee.ID, backfillMolts)
	if err == nil {
		err = s.Store.BackfillTrench(ctx, follower.ID, molts)
	}
	if err != nil {
		s.Log.Warn("trench backfill", "err", err, "followee", followee.ID)
	}
}

// Notify queues a notification from actor to recipientID, unless either
// has blocked the other.
func (s *Service) Notify(actor *store.Crab, hides Hides, recipientID, kind, moltID, text string) {
	if s.Notifier == nil || actor == nil || (hides != nil && hides(recipientID)) {
		return
	}
	s.Notifier.Notify(store.Notification{
		RecipientID: recipientID,
		Type:        kind,
		ActorID:     actor.ID,
		Actor:       actor.UserName,
		MoltID:      moltID,
		Snippet:     store.Snippet(text),
	})
}

// NotifyMentions tells the krabs mentioned in m, except skipID (who already
// gets a reply or quote notification for it) and the names in told
// (already mentioned before an edit).
func (s *Service) NotifyMentions(ctx context.Context, actor *store.Crab, hides Hides, m *store.Molt, skipID string, told ...string) {
	for _, name := range m.Mentions {
		if slices.Contains(told, name) {
			continue
		}
		c, err := s.Store.CrabByUsername(ctx, name)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.Log.Warn("mention lookup", "err", err, "name", name)
			}
			continue
		}
		if c.ID != skipID && c.CanSignIn() {
			s.Notify(actor, hides, c.ID, store.NotifyMention, m.ID, m.Content)
		}
	}
}

// EnqueueCard asks for the card of a post's first link, if it has one.
func (s *Service) EnqueueCard(u string) {
	if u != "" && s.Cards != nil {
		s.Cards.EnqueueCard(u)
	}
}

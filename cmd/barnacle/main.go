// Command barnacle is the @barnacle Krabber bot: a daily "Shell Prompt" — one
// light conversation starter for the reef to reply to, as a haiku (every
// krab's molts start as one): the title, the question, then a rhymed line
// asking for replies. Scheduled once a day around midday. The prompt rotates
// by day of year, so the list cycles.
package main

import (
	"context"
	"time"
	_ "time/tzdata"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// form is the writing form every Shell Prompt takes.
const form = "haiku"

// prompts are deliberately low-stakes and answerable in a molt. Each is the
// haiku's second and third lines: a question and the line that asks for
// replies, which rhymes with it.
var prompts = [][2]string{
	{"What's the best thing you ate this week?", "Reply in a molt; the reef wants a peek."},
	{"One song on repeat right now. Go.", "Reply in a molt and let the reef know."},
	{"What small win are you claiming today?", "Reply in a molt; the reef says hooray."},
	{"Beach or tidepool? Defend your answer.", "Reply in a molt, you sideways dancer."},
	{"What's a hill you'll die on? Low stakes only.", "Reply in a molt; no hill is lonely."},
	{"Last thing that made you laugh out loud?", "Reply in a molt and share it with the crowd."},
	{"What's the project you're excited about?", "Reply in a molt; let the reef find out."},
	{"Underrated snack. Name it.", "Reply in a molt and claim it."},
	{"What's the weather doing where you are?", "Reply in a molt, from near or from far."},
	{"Recommend one thing, anything.", "Reply in a molt; the reef is listening."},
	{"What did past-you get right?", "Reply in a molt and give them their light."},
	{"Coffee, tea, or seawater, straight?", "Reply in a molt; the reef can wait."},
	{"What's a skill you want to pick up this year?", "Reply in a molt; the reef wants to hear."},
	{"Best view you've had lately? Where?", "Reply in a molt and take us there."},
	{"What's something tiny that improved your day?", "Reply in a molt; small things get their say."},
	{"If you could molt one habit, which?", "Reply in a molt and scratch that itch."},
	{"What's your comfort rewatch?", "Reply in a molt; the reef keeps watch."},
	{"Early bird or night crab? Which one?", "Reply in a molt before the day is done."},
	{"What's a question you wish people asked?", "Reply in a molt; consider it asked."},
	{"Show the reef your desk, messy or not.", "Reply in a molt with whatever you've got."},
	{"What's the last photo you took?", "Reply in a molt and give us a look."},
	{"One thing you're grateful for today.", "Reply in a molt; it's good to say."},
	{"What are you putting off? No judgment here.", "Reply in a molt; the reef lends an ear."},
	{"Sweet or savory breakfast? Which is best?", "Reply in a molt and settle the rest."},
	{"What's a place you want to scuttle off to?", "Reply in a molt; the reef might come too."},
	{"Best advice you've ignored?", "Reply in a molt; the reef's on board."},
	{"What's playing in your head on loop?", "Reply in a molt and tell the group."},
	{"Your day in three emoji. No more.", "Reply in a molt; the reef keeps score."},
	{"What's a small luxury you'd recommend?", "Reply in a molt and tell a friend."},
	{"If today had a title, what would it be?", "Reply in a molt; tell the reef, tell the sea."},
}

func main() { botkit.Run("barnacle", form, compose) }

func compose(ctx context.Context) (string, error) {
	now := time.Now().In(nyLoc())
	return prompt(now.YearDay()), nil
}

// prompt is the day's Shell Prompt as a haiku.
func prompt(yearDay int) string {
	q := prompts[yearDay%len(prompts)]
	return botkit.Fit("🐚 Shell Prompt\n" + q[0] + "\n" + q[1])
}

func nyLoc() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}

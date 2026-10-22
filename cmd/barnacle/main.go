// Command barnacle is the @barnacle Krabber bot: a daily "Shell Prompt" — one
// light conversation starter for the reef to reply to. Scheduled once a day
// around midday. The prompt rotates by day of year, so the list cycles.
package main

import (
	"context"
	"time"
	_ "time/tzdata"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// prompts are deliberately low-stakes and answerable in a molt.
var prompts = []string{
	"What's the best thing you ate this week?",
	"One song on repeat right now — go.",
	"What small win are you claiming today?",
	"Beach or tidepool? Defend your answer.",
	"What's a hill you'll die on? (low stakes only)",
	"Last thing that made you laugh out loud?",
	"What are you working on that you're actually excited about?",
	"Underrated snack. Name it.",
	"What's the weather doing where you are?",
	"Recommend one thing — book, show, taco, anything.",
	"What did past-you get right?",
	"Coffee, tea, or straight seawater?",
	"What's a skill you want to pick up this year?",
	"Best view you've had recently?",
	"What's something tiny that improved your day?",
	"If you could molt one habit, which?",
	"What's your comfort rewatch?",
	"Early bird or night crab?",
	"What's a question you wish people asked you?",
	"Show the reef your desk/workspace in words.",
	"What's the last photo you took?",
	"One thing you're grateful for today.",
	"What are you procrastinating on? No judgment.",
	"Sweet or savory breakfast?",
	"What's a place you want to scuttle off to?",
	"Best advice you've ignored?",
	"What's playing in your head on loop?",
	"Describe your day in three emoji.",
	"What's a small luxury you'd recommend?",
	"If today had a title, what is it?",
}

func main() { botkit.Run("barnacle", compose) }

func compose(ctx context.Context) (string, error) {
	now := time.Now().In(nyLoc())
	q := prompts[now.YearDay()%len(prompts)]
	return botkit.Fit("🦞 Shell Prompt: "+q+" Reply in a molt.", "🦞 Shell Prompt: "+q), nil
}

func nyLoc() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}

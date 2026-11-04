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

// form is the writing form every Shell Prompt takes: a haiku, counted
// five-seven-five by the site's own counter.
const form = "haiku"

// prompts are deliberately low-stakes and answerable in a molt. Each is the
// haiku's middle line, seven syllables as the site counts them.
var prompts = []string{
	"The best thing you ate this week?",
	"One song on repeat right now?",
	"What small win is yours today?",
	"Beach or tidepool, and why?",
	"A hill you'll die on, low stakes.",
	"What last made you laugh out loud?",
	"What has you excited now?",
	"Underrated snack: name it.",
	"What's the weather where you are?",
	"Recommend one thing to us.",
	"What did your past self get right?",
	"Coffee, tea, or seawater?",
	"A skill to pick up this year?",
	"Best view you've had lately?",
	"What tiny thing helped today?",
	"Molt one habit: which would go?",
	"What's your comfort rewatch, friend?",
	"Early bird or night crab? Which?",
	"A question you wish was asked?",
	"Show the reef your desk in words.",
	"The last photo you took: what?",
	"One thing you're grateful for.",
	"What have you been putting off?",
	"Sweet or savory breakfast?",
	"Where would you scuttle off to?",
	"Best advice you've ignored? Why?",
	"What's on loop inside your head?",
	"Your day in three emoji.",
	"Name a small luxury, please.",
	"If today were titled, what?",
}

// fillers pad a line that comes up short.
var fillers = []string{"tell us", "crabs", "hey"}

func main() { botkit.Run("barnacle", form, compose) }

func compose(_ context.Context) (string, error) {
	now := time.Now().In(nyLoc())
	return prompt(now.YearDay()), nil
}

// prompt is the day's Shell Prompt as a haiku: the title, the question, the
// ask.
func prompt(yearDay int) string {
	return botkit.Haiku(fillers,
		[]string{"🐚 Shell Prompt for the reef"},
		[]string{prompts[yearDay%len(prompts)]},
		[]string{"reply in a molt"})
}

func nyLoc() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}

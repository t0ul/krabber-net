// Command scuttle is the @scuttle bot: once or twice a day it reads the
// Manhattan tide (NOAA) and sky (NWS) and posts one molt to krabber.net through
// the Krabber API, as a haiku: the tides, the sky, the sign-off. Both feeds
// are keyless US-gov APIs, so the only secret is the Krabber API key, read
// from SSM at runtime.
//
// On Lambda it runs on an EventBridge schedule. Run it locally against a dev
// server to see the molt it would post:
//
//	API_KEY=kb_... KRABBER_API_URL=http://localhost:5050/api/v1/molts go run ./cmd/scuttle
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/t0ul/krabber-net/internal/botkit"
)

// Defaults point at Central Park / The Battery, the usual Manhattan references
// for sky and tide. Every one can be overridden by an environment variable.
const (
	defaultLat     = "40.7829" // Central Park
	defaultLon     = "-73.9654"
	defaultStation = "8518750" // The Battery, NY
	defaultUA      = "krabber-scuttle (+https://krabber.net)"
	moltMax        = 280
	moltForm       = "haiku" // the writing form every Daily Scuttle takes
)

type scuttle struct {
	http    *http.Client
	apiURL  string
	apiKey  string
	ua      string
	lat     string
	lon     string
	station string
	loc     *time.Location // station local time, for the NOAA date and tide times
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := newScuttle(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// On Lambda, AWS sets AWS_LAMBDA_RUNTIME_API; otherwise run once and print.
	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		lambda.Start(func(ctx context.Context) error { return s.run(ctx) })
		return
	}
	if err := s.run(ctx); err != nil {
		log.Fatal(err)
	}
}

func newScuttle(ctx context.Context) (*scuttle, error) {
	s := &scuttle{
		http:    &http.Client{Timeout: 15 * time.Second},
		apiURL:  getenv("KRABBER_API_URL", "https://krabber.net/api/v1/molts"),
		ua:      getenv("USER_AGENT", defaultUA),
		lat:     getenv("NWS_LAT", defaultLat),
		lon:     getenv("NWS_LON", defaultLon),
		station: getenv("NOAA_STATION", defaultStation),
	}

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, fmt.Errorf("load timezone: %w", err)
	}
	s.loc = loc

	s.apiKey, err = apiKey(ctx)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// apiKey reads the Krabber API key from SSM (API_KEY_PARAM), or straight from
// API_KEY for local runs.
func apiKey(ctx context.Context) (string, error) {
	if k := os.Getenv("API_KEY"); k != "" {
		return k, nil
	}
	name := os.Getenv("API_KEY_PARAM")
	if name == "" {
		return "", fmt.Errorf("set API_KEY (local) or API_KEY_PARAM (SSM)")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("aws config: %w", err)
	}
	out, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("read %s from ssm: %w", name, err)
	}
	return aws.ToString(out.Parameter.Value), nil
}

// run fetches both feeds, composes one molt and posts it. The tide is the point
// of the bot, so a tide failure aborts; a weather failure just drops the sky.
func (s *scuttle) run(ctx context.Context) error {
	tides, err := s.tides(ctx)
	if err != nil {
		return fmt.Errorf("tides: %w", err)
	}
	wx, err := s.weather(ctx)
	if err != nil {
		log.Printf("weather unavailable, posting tide only: %v", err)
	}

	text := compose(tides, wx)
	if err := botkit.Check(text, moltForm); err != nil {
		return err
	}
	if os.Getenv("AWS_LAMBDA_RUNTIME_API") == "" {
		log.Printf("would post (%s, %d chars): %s", moltForm, utf8.RuneCountInString(text), text)
	}
	if err := s.post(ctx, text); err != nil {
		return fmt.Errorf("post molt: %w", err)
	}
	log.Printf("posted: %s", text)
	return nil
}

// ---------------------------------------------------------------------------
// NOAA tides
// ---------------------------------------------------------------------------

type tideEvent struct {
	when time.Time
	high bool
}

func (s *scuttle) tides(ctx context.Context) ([]tideEvent, error) {
	day := time.Now().In(s.loc).Format("20060102")
	u := fmt.Sprintf("https://api.tidesandcurrents.noaa.gov/api/prod/datagetter"+
		"?product=predictions&application=krabber-scuttle&begin_date=%s&end_date=%s"+
		"&datum=MLLW&station=%s&time_zone=lst_ldt&units=english&interval=hilo&format=json",
		day, day, s.station)

	var body struct {
		Predictions []struct {
			T    string `json:"t"`
			Type string `json:"type"`
		} `json:"predictions"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := s.getJSON(ctx, u, "application/json", &body); err != nil {
		return nil, err
	}
	if body.Error != nil {
		return nil, fmt.Errorf("noaa: %s", body.Error.Message)
	}
	if len(body.Predictions) == 0 {
		return nil, fmt.Errorf("no predictions for station %s", s.station)
	}
	var out []tideEvent
	for _, p := range body.Predictions {
		t, err := time.ParseInLocation("2006-01-02 15:04", p.T, s.loc)
		if err != nil {
			continue
		}
		out = append(out, tideEvent{when: t, high: strings.EqualFold(p.Type, "H")})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no parseable tide times")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// NWS weather
// ---------------------------------------------------------------------------

type weather struct {
	period    string // e.g. "Today", "Tonight"
	temp      int
	unit      string
	forecast  string // e.g. "Sunny"
	windSpeed string // e.g. "8 mph"
}

func (s *scuttle) weather(ctx context.Context) (*weather, error) {
	// points -> the gridpoint forecast URL for these coordinates.
	var point struct {
		Properties struct {
			Forecast string `json:"forecast"`
		} `json:"properties"`
	}
	pointURL := fmt.Sprintf("https://api.weather.gov/points/%s,%s", s.lat, s.lon)
	if err := s.getJSON(ctx, pointURL, "application/geo+json", &point); err != nil {
		return nil, err
	}
	if point.Properties.Forecast == "" {
		return nil, fmt.Errorf("no forecast url for %s,%s", s.lat, s.lon)
	}

	var fc struct {
		Properties struct {
			Periods []struct {
				Name            string `json:"name"`
				Temperature     int    `json:"temperature"`
				TemperatureUnit string `json:"temperatureUnit"`
				WindSpeed       string `json:"windSpeed"`
				ShortForecast   string `json:"shortForecast"`
			} `json:"periods"`
		} `json:"properties"`
	}
	if err := s.getJSON(ctx, point.Properties.Forecast, "application/geo+json", &fc); err != nil {
		return nil, err
	}
	if len(fc.Properties.Periods) == 0 {
		return nil, fmt.Errorf("no forecast periods")
	}
	p := fc.Properties.Periods[0]
	return &weather{
		period:    p.Name,
		temp:      p.Temperature,
		unit:      p.TemperatureUnit,
		forecast:  p.ShortForecast,
		windSpeed: p.WindSpeed,
	}, nil
}

// ---------------------------------------------------------------------------
// Compose and post
// ---------------------------------------------------------------------------

// fillers pad a line that comes up short.
var fillers = []string{"on the Battery", "over the harbor", "says the sky", "all day", "today", "now"}

// compose builds one molt as a haiku the site counts as five-seven-five:
// the tides, then the sky, then the sign-off. The tide line names the first
// two events (or the one there is), the sky line drops the wind, then the
// temperature, until it fits.
func compose(tides []tideEvent, wx *weather) string {
	event := func(i int) (label, at string) {
		label = "low"
		if tides[i].high {
			label = "high"
		}
		return label, clock(tides[i].when)
	}
	var tideLines []string
	if len(tides) >= 2 {
		l1, t1 := event(0)
		l2, t2 := event(1)
		tideLines = append(tideLines, fmt.Sprintf("%s tide %s, %s %s", l1, t1, l2, t2))
	}
	if len(tides) >= 1 {
		l1, t1 := event(0)
		tideLines = append(tideLines, fmt.Sprintf("%s tide at %s", l1, t1), fmt.Sprintf("%s tide %s", l1, t1))
	}
	sky := []string{"the sky keeps its own counsel"}
	if wx != nil {
		base := fmt.Sprintf("%s, %d%s", wx.forecast, wx.temp, wx.unit)
		sky = []string{base}
		if wx.windSpeed != "" {
			sky = []string{base + ", wind " + wx.windSpeed, base}
		}
		sky = append(sky, wx.forecast)
	}
	return botkit.Haiku(fillers, tideLines, sky, []string{"Daily Scuttle %nyc"})
}

// clock formats a time like "6:42a" or "12:58p".
func clock(t time.Time) string {
	s := strings.ToLower(t.Format("3:04pm"))
	return strings.TrimSuffix(s, "m") // "am"->"a", "pm"->"p"
}

func (s *scuttle) post(ctx context.Context, text string) error {
	payload, err := json.Marshal(map[string]string{"text": text, "form": moltForm})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.ua)

	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if res.StatusCode != http.StatusCreated {
		return fmt.Errorf("api returned %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *scuttle) getJSON(ctx context.Context, url, accept string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", s.ua) // NWS requires a descriptive User-Agent.

	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 2<<10))
		return fmt.Errorf("GET %s: %d: %s", url, res.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

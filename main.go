package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	baseURL    = "https://apibot.luffa.im/robot"
	receiveURL = baseURL + "/receive"
	sendURL    = baseURL + "/send"
)

var (
	secret     string
	openaiKey  string
	httpClient = &http.Client{Timeout: 20 * time.Second}

	lumaCalendars = []string{
		"https://lu.ma/BuildersBrew",
		"https://lu.ma/deeptechdecoded",
		"https://lu.ma/encode-club",
		"https://lu.ma/entrepreneurs-first",
		"https://lu.ma/granola",
		"https://lu.ma/kcltech",
		"https://lu.ma/londonlivecoding",
		"https://lu.ma/londonmaxxing-ai",
		"https://lu.ma/ldn_software_guild",
		"https://lu.ma/plugged",
		"https://lu.ma/ripplesocialclub",
		"https://lu.ma/tech-europe",
		"https://lu.ma/techgames",
		"https://lu.ma/entrepreneursnetwork",
		"https://lu.ma/thehackcollective",
		"https://lu.ma/london-loop",
		"https://lu.ma/theofflineclublondon",
		"https://lu.ma/mafia",
		"https://lu.ma/london",
	}
)

func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		os.Setenv(strings.TrimSpace(k), strings.TrimSpace(v))
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ============================================================================
// Models
// ============================================================================

type Event struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Location    string   `json:"location"`
	StartDate   string   `json:"start_date"`
	EndDate     string   `json:"end_date"`
	URL         string   `json:"url"`
	Source      string   `json:"source"`
	Calendar    string   `json:"calendar"`
	AISummary   string   `json:"ai_summary"`
	AITags      []string `json:"ai_tags"`
	AIVibe      string   `json:"ai_vibe"`
	// Application tracking
	AppDeadline string `json:"app_deadline,omitempty"` // YYYY-MM-DD
	AppURL      string `json:"app_url,omitempty"`
	AppStatus   string `json:"app_status,omitempty"` // open, closed, rolling
	PrizePool   string `json:"prize_pool,omitempty"`
	Travel      bool   `json:"travel_funded,omitempty"`
}

type Deadline struct {
	Name     string
	Date     string
	DaysLeft int
	EventID  string // link back to event if applicable
}

type User struct {
	UID         string
	DisplayName string
	Skills      []string
	Interests   []string // tags they care about
	Location    string
}

type Button struct {
	Name     string `json:"name"`
	Selector string `json:"selector"`
	IsHidden string `json:"isHidden"`
}

type MsgPayload struct {
	Text        string   `json:"text"`
	Button      []Button `json:"button,omitempty"`
	Confirm     []Button `json:"confirm,omitempty"`
	DismissType string   `json:"dismissType,omitempty"`
}

type Envelope struct {
	UID     string   `json:"uid"`
	Count   int      `json:"count"`
	Message []string `json:"message"`
	Type    int      `json:"type"`
}

type InnerMessage struct {
	UID  string `json:"uid"`
	Text string `json:"text"`
}

// ============================================================================
// State
// ============================================================================

type State struct {
	mu        sync.RWMutex
	events    map[string]*Event
	users     map[string]*User
	interests map[string]map[string]string // uid -> eventID -> status
	seenMsgs  map[string]bool
	deadlines []Deadline
	// Conversation context per user for multi-turn
	convCtx map[string]string // uid -> last command context
}

func NewState() *State {
	return &State{
		events:    make(map[string]*Event),
		users:     make(map[string]*User),
		interests: make(map[string]map[string]string),
		seenMsgs:  make(map[string]bool),
		convCtx:   make(map[string]string),
	}
}

func (s *State) ensureUser(uid string) *User {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.users[uid]; ok {
		return u
	}
	u := &User{UID: uid, DisplayName: uid}
	s.users[uid] = u
	return u
}

func (s *State) shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (s *State) findEventByShortID(sid string) *Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, evt := range s.events {
		if s.shortID(id) == sid {
			return evt
		}
	}
	return nil
}

func (s *State) findEventByName(query string) *Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query = strings.ToLower(query)
	for _, evt := range s.events {
		if strings.Contains(strings.ToLower(evt.Name), query) {
			return evt
		}
	}
	return nil
}

// ============================================================================
// LLM (OpenAI)
// ============================================================================

func llmChat(systemPrompt, userPrompt string) (string, error) {
	if openaiKey == "" {
		return "", fmt.Errorf("no OPENAI_API_KEY")
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_tokens":  500,
		"temperature": 0.7,
	})
	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+openaiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("openai %d: %s", resp.StatusCode, trunc(string(body), 200))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(body, &result)
	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("no choices")
}

// ============================================================================
// Luffa API
// ============================================================================

func sendDM(uid, text string, buttons []Button) {
	msg := MsgPayload{Text: text}
	if len(buttons) > 0 {
		msg.Button = buttons
		msg.DismissType = "dismiss"
	}
	msgJSON, _ := json.Marshal(msg)
	body := map[string]string{"secret": secret, "uid": uid, "msg": string(msgJSON)}
	if len(buttons) > 0 {
		body["type"] = "2"
	}
	bodyJSON, _ := json.Marshal(body)
	resp, err := httpClient.Post(sendURL, "application/json", bytes.NewReader(bodyJSON))
	if err != nil {
		log.Printf("[send] err: %v", err)
		return
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
}

func sendText(uid, text string)                  { sendDM(uid, text, nil) }
func sendButtons(uid, text string, b []Button)   { sendDM(uid, text, b) }

func sendConfirm(uid, text string, confirms []Button) {
	msg := MsgPayload{Text: text, Confirm: confirms, DismissType: "dismiss"}
	msgJSON, _ := json.Marshal(msg)
	body := map[string]string{"secret": secret, "uid": uid, "msg": string(msgJSON), "type": "2"}
	bodyJSON, _ := json.Marshal(body)
	resp, err := httpClient.Post(sendURL, "application/json", bytes.NewReader(bodyJSON))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
}

func pollMessages() []struct{ UID, Text string } {
	body, _ := json.Marshal(map[string]string{"secret": secret})
	resp, err := httpClient.Post(receiveURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var envelopes []Envelope
	if json.Unmarshal(data, &envelopes) != nil {
		return nil
	}
	var msgs []struct{ UID, Text string }
	for _, env := range envelopes {
		for _, raw := range env.Message {
			var inner InnerMessage
			if json.Unmarshal([]byte(raw), &inner) != nil {
				continue
			}
			uid := inner.UID
			if uid == "" {
				uid = env.UID
			}
			msgs = append(msgs, struct{ UID, Text string }{uid, strings.TrimSpace(inner.Text)})
		}
	}
	return msgs
}

// ============================================================================
// Luma Scraping
// ============================================================================

func scrapeLumaCalendar(calURL string) []*Event {
	var events []*Event
	resp, err := httpClient.Get(calURL)
	if err != nil {
		log.Printf("[scrape] %s: %v", calURL, err)
		return nil
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil
	}
	parts := strings.Split(calURL, "/")
	calName := parts[len(parts)-1]

	doc.Find("script#__NEXT_DATA__").Each(func(i int, sel *goquery.Selection) {
		var ndata map[string]any
		if json.Unmarshal([]byte(sel.Text()), &ndata) == nil {
			events = append(events, extractEventsFromJSON(ndata, calName)...)
		}
	})
	if len(events) == 0 {
		doc.Find("script[type='application/json']").Each(func(i int, sel *goquery.Selection) {
			var ndata map[string]any
			if json.Unmarshal([]byte(sel.Text()), &ndata) == nil {
				events = append(events, extractEventsFromJSON(ndata, calName)...)
			}
		})
	}
	log.Printf("[scrape] %s: %d events", calName, len(events))
	return events
}

func extractEventsFromJSON(data map[string]any, calName string) []*Event {
	var events []*Event
	var walk func(v any)
	walk = func(v any) {
		switch val := v.(type) {
		case map[string]any:
			if name, ok := val["name"].(string); ok {
				if _, has := val["start_at"]; has {
					apiID, _ := val["api_id"].(string)
					if apiID == "" {
						apiID, _ = val["id"].(string)
					}
					desc, _ := val["description"].(string)
					startAt, _ := val["start_at"].(string)
					endAt, _ := val["end_at"].(string)
					urlSlug, _ := val["url"].(string)
					loc := ""
					if geo, ok := val["geo_address_info"].(map[string]any); ok {
						if f, ok := geo["full_address"].(string); ok {
							loc = f
						} else if c, ok := geo["city"].(string); ok {
							loc = c
						}
					}
					evtURL := "https://lu.ma/" + urlSlug
					if urlSlug == "" && apiID != "" {
						evtURL = "https://lu.ma/" + apiID
					}
					eid := apiID
					if eid == "" {
						h := md5.Sum([]byte(name))
						eid = fmt.Sprintf("%x", h)[:12]
					}
					events = append(events, &Event{
						ID: eid, Name: name, Description: trunc(desc, 500),
						Location: loc, StartDate: fmtDate(startAt), EndDate: fmtDate(endAt),
						URL: evtURL, Source: "luma", Calendar: calName,
					})
				}
			}
			for _, c := range val {
				walk(c)
			}
		case []any:
			for _, c := range val {
				walk(c)
			}
		}
	}
	walk(data)
	return events
}

func fmtDate(iso string) string {
	if iso == "" {
		return "TBD"
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02"} {
		if t, err := time.Parse(layout, iso); err == nil {
			return t.Format("Jan 2, 2006")
		}
	}
	if len(iso) >= 10 {
		return iso[:10]
	}
	return iso
}

func scrapeAllCalendars(s *State) int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	sem := make(chan struct{}, 4)
	for _, calURL := range lumaCalendars {
		wg.Add(1)
		sem <- struct{}{}
		go func(url string) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, evt := range scrapeLumaCalendar(url) {
				mu.Lock()
				if _, exists := s.events[evt.ID]; !exists {
					s.events[evt.ID] = evt
					total++
				}
				mu.Unlock()
			}
		}(calURL)
	}
	wg.Wait()
	return total
}

// ============================================================================
// AI Enrichment
// ============================================================================

func enrichEvent(evt *Event) {
	prompt := fmt.Sprintf(`Event: %s | Date: %s | Location: %s | Description: %s
Return JSON only: {"summary":"2-3 sentences for students","tags":["#tag1","#tag2"],"vibe":"one-liner"}`,
		evt.Name, evt.StartDate, evt.Location, trunc(evt.Description, 300))

	resp, err := llmChat("Hackathon community bot. Concise, genuine, slightly cheeky British tone.", prompt)
	if err != nil {
		evt.AISummary = trunc(evt.Description, 200)
		if evt.AISummary == "" {
			evt.AISummary = evt.Name
		}
		evt.AITags = []string{"#tech"}
		evt.AIVibe = "Worth checking out!"
		return
	}
	cleaned := strings.TrimSpace(resp)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)
	var parsed struct {
		Summary string   `json:"summary"`
		Tags    []string `json:"tags"`
		Vibe    string   `json:"vibe"`
	}
	if json.Unmarshal([]byte(cleaned), &parsed) == nil {
		evt.AISummary = parsed.Summary
		evt.AITags = parsed.Tags
		evt.AIVibe = parsed.Vibe
	} else {
		evt.AISummary = trunc(resp, 200)
		evt.AITags = []string{"#tech"}
	}
}

// ============================================================================
// Deadlines
// ============================================================================

func computeDeadlines(deadlines []Deadline) []Deadline {
	now := time.Now()
	var result []Deadline
	for _, d := range deadlines {
		t, err := time.Parse("2006-01-02", d.Date)
		if err != nil {
			continue
		}
		days := int(math.Ceil(t.Sub(now).Hours() / 24))
		result = append(result, Deadline{Name: d.Name, Date: d.Date, DaysLeft: days, EventID: d.EventID})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].DaysLeft < result[j].DaysLeft })
	return result
}

func eventDeadlines(s *State) []Deadline {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var dls []Deadline
	for _, evt := range s.events {
		if evt.AppDeadline != "" {
			dls = append(dls, Deadline{Name: evt.Name + " application", Date: evt.AppDeadline, EventID: evt.ID})
		}
	}
	dls = append(dls, s.deadlines...)
	return computeDeadlines(dls)
}

// ============================================================================
// Seed Data
// ============================================================================

func seedData(s *State) {
	events := []*Event{
		// === DEMO EVENT WITH APPLICATION DEADLINE ===
		{
			ID: "encodeai26", Name: "Encode AI Hackathon 2026",
			Description: "Build agentic AI systems on the Luffa platform. $25,000 prize pool across multiple tracks including LuffaNator (Agentic Track), SuperBox (Mini App Track), and EndlessBuilder (On-Chain Track). 48-hour virtual hackathon with in-person demo day in London.",
			Location: "London / Virtual", StartDate: "Apr 5, 2026", EndDate: "Apr 7, 2026",
			URL: "https://www.encode.club/ai-hackathon", Source: "demo",
			AISummary: "Encode Club's AI hackathon with $25k in prizes. Build agentic systems on Luffa - bots, mini apps, and on-chain integrations. Virtual with London demo day. Perfect if you're into AI agents and Web3.",
			AITags: []string{"#AI", "#web3", "#luffa", "#25k-prizes", "#agents"},
			AIVibe: "The hackathon we're literally building this bot for",
			AppDeadline: "2026-03-28", AppURL: "https://www.encode.club/ai-hackathon",
			AppStatus: "open", PrizePool: "$25,000", Travel: false,
		},
		{
			ID: "hackprinceton26", Name: "HackPrinceton",
			Description: "Princeton University hackathon. Travel reimbursement available for accepted hackers.",
			Location: "Princeton, NJ, USA", StartDate: "Apr 17, 2026", EndDate: "Apr 19, 2026",
			URL: "https://www.hackprinceton.com/", Source: "discord",
			AISummary: "Princeton's flagship hackathon with travel reimbursement. Ivy League vibes, great networking, and they only ask 2 written answers on the app.",
			AITags: []string{"#ivy-league", "#travel-funded", "#usa", "#prestigious"},
			AIVibe: "Prestigious Ivy hack with free flights - apply and see what happens innit",
			AppDeadline: "2026-03-14", AppURL: "https://www.hackprinceton.com/",
			AppStatus: "closed", PrizePool: "TBD", Travel: true,
		},
		{
			ID: "cambattlecode26", Name: "Cambridge Battlecode",
			Description: "6-week turn-based strategy programming competition. Build Python bots for a Factorio/Mindustry-style game. £15,000 prize pool. Live finals at Cambridge Union. Teams of 1-4, separate beginner and veteran brackets.",
			Location: "Cambridge, UK", StartDate: "Mar 16, 2026", EndDate: "May 6, 2026",
			URL: "https://battlecode.cam/", Source: "discord",
			AISummary: "6-week programming competition building Python bots for a strategy game. £15k prize pool, live finals at the Cambridge Union Debating Chamber. Beginner bracket available so no excuses.",
			AITags: []string{"#competitive-programming", "#python", "#cambridge", "#15k-prizes"},
			AIVibe: "Big brain bot battles with serious prize money",
			AppStatus: "open",
		},
		{
			ID: "hackbelfast26", Name: "HackBelfast 2026",
			Description: "Hackathon at Queen's University Belfast. £5k prize pool.",
			Location: "Belfast, UK", StartDate: "Apr 25, 2026", EndDate: "Apr 26, 2026",
			URL: "https://www.hackbelfast.org/", Source: "discord",
			AISummary: "Hackathon at Queen's University Belfast with a £5k prize pool. Good weekend trip, Belfast is class.",
			AITags: []string{"#belfast", "#uk", "#5k-prizes", "#queens"},
			AIVibe: "Northern Irish hackathon vibes at a beautiful campus",
		},
		{
			ID: "kenthackit26", Name: "KentHackIt 2026",
			Description: "Hackathon at the University of Kent in Canterbury. Weekend event.",
			Location: "Canterbury, UK", StartDate: "Mar 21, 2026", EndDate: "Mar 22, 2026",
			URL: "https://www.kenthackit.co.uk/", Source: "discord",
			AISummary: "University hackathon in Canterbury. Quick weekend trip from London, chill vibes.",
			AITags: []string{"#university", "#kent", "#uk", "#weekend"},
			AIVibe: "Canterbury weekend hack, easy day trip from London",
		},
		{
			ID: "clawbio26", Name: "ClawBio Hackathon: Agentic Genomics",
			Description: "Building the bridge between humans and AI with OpenClaw, ClawBio and agentic AI for health data ownership.",
			Location: "Imperial College London", StartDate: "Mar 19, 2026", EndDate: "Mar 19, 2026",
			URL: "https://luma.com/kolhdoi9", Source: "discord",
			AISummary: "One-day bio hackathon at Imperial focused on agentic AI for genomics. Cutting-edge intersection of AI and health tech.",
			AITags: []string{"#biotech", "#AI", "#imperial", "#genomics", "#one-day"},
			AIVibe: "Cutting-edge bio x AI at Imperial",
		},
		{
			ID: "hackkosice26", Name: "Hack Kosice 2026",
			Description: "Hackathon in Kosice, Slovakia. European hackathon experience.",
			Location: "Kosice, Slovakia", StartDate: "Apr 18, 2026", EndDate: "Apr 19, 2026",
			URL: "https://hackkosice.com/2026/", Source: "discord",
			AISummary: "Hackathon in Slovakia, same weekend as StarkHacks and DragonHack. Pick your European adventure.",
			AITags: []string{"#slovakia", "#europe", "#travel", "#weekend"},
			AIVibe: "Eastern European hackathon adventure",
		},
		{
			ID: "dragonhack26", Name: "DragonHack",
			Description: "Major hackathon in Ljubljana, Slovenia. Known for great atmosphere.",
			Location: "Ljubljana, Slovenia", StartDate: "Apr 18, 2026", EndDate: "Apr 19, 2026",
			URL: "https://dragonhack.si/", Source: "discord",
			AISummary: "Slovenian hackathon in Ljubljana. Same weekend as Hack Kosice but reportedly has more aura. Ljubljana is gorgeous.",
			AITags: []string{"#slovenia", "#europe", "#aura", "#prestigious"},
			AIVibe: "The one with more aura - Ljubljana is gorgeous too",
		},
		{
			ID: "hackpompey26", Name: "Hack Pompey: The Century Hack",
			Description: "8-hour sprint hackathon celebrating Portsmouth reaching 100 years as a city.",
			Location: "Portsmouth, UK", StartDate: "Mar 28, 2026", EndDate: "Mar 28, 2026",
			URL: "https://www.eventbrite.co.uk/e/hack-pompey-the-century-hack-tickets-1983677444636", Source: "discord",
			AISummary: "8-hour sprint hackathon celebrating Portsmouth's centenary. Quick and fun, no all-nighter needed.",
			AITags: []string{"#portsmouth", "#sprint", "#uk", "#8-hours"},
			AIVibe: "Speed hack by the sea - 8 hours, no sleep needed",
		},
		{
			ID: "lauzhack26", Name: "LauzHack 2026",
			Description: "EPFL's flagship hackathon in Lausanne, Switzerland. Interest form open.",
			Location: "EPFL, Lausanne, Switzerland", StartDate: "TBD", EndDate: "TBD",
			URL: "https://lauzhack.com", Source: "discord",
			AISummary: "EPFL's flagship hackathon in Lausanne. Interest form currently open, proper applications coming later.",
			AITags: []string{"#switzerland", "#epfl", "#europe", "#prestigious"},
			AIVibe: "Swiss precision hackathon at a world-class uni",
		},
		{
			ID: "alpinevalley26", Name: "Alpine Valley Hacker House",
			Description: "6-week hacker house in Austria for aspiring founders. 0% equity taken, full support provided.",
			Location: "Austria", StartDate: "TBD", EndDate: "TBD",
			URL: "https://www.alpine-valley.com/", Source: "discord",
			AISummary: "6-week hacker house in the Austrian alps for aspiring founders. Zero equity, full support. Build your startup with mountain views.",
			AITags: []string{"#founders", "#austria", "#hacker-house", "#0-equity", "#6-weeks"},
			AIVibe: "Mountain views while you build your startup, can't beat it",
		},
		{
			ID: "hackmit26", Name: "HackMIT",
			Description: "MIT's annual hackathon. One of the most prestigious in the world.",
			Location: "Cambridge, MA, USA", StartDate: "Oct 2026", EndDate: "Oct 2026",
			URL: "https://hackmit.org", Source: "discord",
			AISummary: "MIT's legendary hackathon. Applications open in summer, one of the hardest to get into but absolutely worth it.",
			AITags: []string{"#MIT", "#usa", "#prestigious", "#top-tier"},
			AIVibe: "The holy grail of hackathons",
			AppDeadline: "2026-07-25", AppStatus: "upcoming",
		},
		{
			ID: "gsoc26", Name: "Google Summer of Code 2026",
			Description: "Open source contribution program by Google. Get paid to work on open source projects over summer.",
			Location: "Remote", StartDate: "May 2026", EndDate: "Aug 2026",
			URL: "https://summerofcode.withgoogle.com/", Source: "discord",
			AISummary: "Google's summer open source program. Get paid to contribute to major open source projects. Amazing for your CV and learning.",
			AITags: []string{"#google", "#open-source", "#remote", "#paid", "#summer"},
			AIVibe: "Get paid to write open source code all summer",
			AppDeadline: "2026-03-31", AppStatus: "open",
		},
		{
			ID: "svfellow26", Name: "Stanford Venture Fellowship",
			Description: "Fully funded trip to Stanford for exceptional applicants. Only 10 spots. May 28-June 3.",
			Location: "Stanford, CA, USA", StartDate: "May 28, 2026", EndDate: "Jun 3, 2026",
			URL: "https://www.svfellow.com/program", Source: "discord",
			AISummary: "One week fully funded at Stanford. Only 10 spots worldwide - ultra competitive but life-changing if you get in.",
			AITags: []string{"#stanford", "#fellowship", "#fully-funded", "#exclusive"},
			AIVibe: "10 spots, fully funded Stanford trip - shoot your shot",
			AppDeadline: "2026-04-04", AppURL: "https://www.svfellow.com/program",
			AppStatus: "open", Travel: true,
		},
		{
			ID: "chatgpt26", Name: "ChatGPT 26 (OpenAI Student Grant)",
			Description: "OpenAI celebrates 26 students shaping what's possible with AI. $10k grant, tech access, community. Must be 18-25, in college or recently graduated.",
			Location: "OpenAI HQ, San Francisco", StartDate: "TBD", EndDate: "TBD",
			URL: "https://chatgpt.com/chatgpt-26", Source: "discord",
			AISummary: "OpenAI picks 26 students for $10k grants and a trip to their HQ. Officially US/Canada only but worth a cheeky application anyway.",
			AITags: []string{"#openai", "#AI", "#grant", "#10k", "#san-francisco"},
			AIVibe: "Free trip to OpenAI HQ and $10k - worth a punt even if you're British",
			AppStatus: "open", Travel: true, PrizePool: "$10,000 grant",
		},
		{
			ID: "alpbach26", Name: "European Forum Alpbach Scholarship",
			Description: "Scholarship to attend the European Forum Alpbach in Austria. Lifechanging networking and intellectual experience.",
			Location: "Alpbach, Austria", StartDate: "Aug 2026", EndDate: "Sep 2026",
			URL: "https://www.alpbach.org/blog/scholarship-programme", Source: "discord",
			AISummary: "Scholarship to the European Forum Alpbach - a lifechanging intellectual and networking event in the Austrian alps.",
			AITags: []string{"#scholarship", "#austria", "#networking", "#intellectual"},
			AIVibe: "European intellectual elite meet in Austrian village - you should be there",
			AppDeadline: "2026-03-24", AppStatus: "closing-soon",
		},
	}

	deadlines := []Deadline{
		{Name: "BriCS Student Cluster Competition", Date: "2026-03-31"},
		{Name: "WiCyS Scholarships (opens Sep)", Date: "2026-09-01"},
		{Name: "RAEng Engineering Leaders Scholarship", Date: "2026-12-01"},
	}

	s.mu.Lock()
	for _, evt := range events {
		s.events[evt.ID] = evt
	}
	s.deadlines = deadlines
	s.mu.Unlock()
}

// ============================================================================
// Handlers
// ============================================================================

func handleHelp(uid string) {
	sendButtons(uid, `Welcome to HackAgent - your hackathon ops hub.

I track hackathons, fellowships, and opportunities from Luma calendars and community intel. Here's what I can do:`, []Button{
		{Name: "Browse Events", Selector: "/events", IsHidden: "0"},
		{Name: "Deadlines", Selector: "/deadlines", IsHidden: "0"},
		{Name: "Scrape Luma", Selector: "/scrape", IsHidden: "0"},
	})

	sendText(uid, `Commands:
/events - Browse upcoming events (with action buttons)
/deadlines - Application deadline tracker
/upcoming - Events in the next 2 weeks
/apply <name> - Get application info for an event
/scrape - Fetch latest from 19 Luma calendars
/skills python,react,ml - Set your skills
/status - Your profile & interests
/team - Find team matches for events you're attending
/calendars - List tracked Luma calendars
/adddeadline Name,2026-04-15 - Track a custom deadline

Or just ask me anything - I'll use AI to help!`)
}

func handleEvents(s *State, uid string, page int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.events) == 0 {
		sendText(uid, "No events yet! Send /scrape to fetch from Luma.")
		return
	}

	type kv struct {
		id  string
		evt *Event
	}
	var sorted []kv
	for id, evt := range s.events {
		sorted = append(sorted, kv{id, evt})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].evt.StartDate < sorted[j].evt.StartDate })

	start := page * 5
	if start >= len(sorted) {
		sendText(uid, "No more events!")
		return
	}
	end := start + 5
	if end > len(sorted) {
		end = len(sorted)
	}

	for _, se := range sorted[start:end] {
		evt := se.evt
		sid := s.shortID(se.id)

		// Build rich event card
		text := fmt.Sprintf("%s\n%s", evt.Name, strings.Repeat("-", min(len(evt.Name), 30)))
		text += fmt.Sprintf("\n%s | %s", evt.StartDate, evt.Location)

		if evt.PrizePool != "" {
			text += fmt.Sprintf("\nPrize: %s", evt.PrizePool)
		}
		if evt.Travel {
			text += "\nTravel: Funded"
		}
		if evt.AppDeadline != "" {
			dl := deadlineDaysLeft(evt.AppDeadline)
			urgency := ""
			if dl < 0 {
				urgency = " (CLOSED)"
			} else if dl <= 3 {
				urgency = fmt.Sprintf(" (%d days - URGENT!!)", dl)
			} else if dl <= 7 {
				urgency = fmt.Sprintf(" (%d days - SOON!)", dl)
			} else {
				urgency = fmt.Sprintf(" (%d days left)", dl)
			}
			text += fmt.Sprintf("\nApply by: %s%s", evt.AppDeadline, urgency)
		}

		text += fmt.Sprintf("\n\n%s", evt.AISummary)
		if evt.AIVibe != "" {
			text += fmt.Sprintf("\n\n\"%s\"", evt.AIVibe)
		}
		if len(evt.AITags) > 0 {
			text += "\n" + strings.Join(evt.AITags, " ")
		}
		text += "\n" + evt.URL

		// Count interest
		interested, going, needTeam := 0, 0, 0
		for _, ui := range s.interests {
			switch ui[evt.ID] {
			case "interested":
				interested++
			case "going":
				going++
			case "needteam":
				needTeam++
			}
		}
		if interested+going+needTeam > 0 {
			text += fmt.Sprintf("\n\n%d interested | %d going | %d need team", interested, going, needTeam)
		}

		buttons := []Button{
			{Name: "Interested", Selector: fmt.Sprintf("interested_%s", sid), IsHidden: "0"},
			{Name: "Going!", Selector: fmt.Sprintf("going_%s", sid), IsHidden: "0"},
			{Name: "Need Team", Selector: fmt.Sprintf("needteam_%s", sid), IsHidden: "0"},
		}
		if evt.AppURL != "" && evt.AppStatus != "closed" {
			buttons = append(buttons, Button{Name: "Apply", Selector: fmt.Sprintf("apply_%s", sid), IsHidden: "0"})
		}
		sendButtons(uid, text, buttons)
		time.Sleep(400 * time.Millisecond)
	}

	if end < len(sorted) {
		sendButtons(uid, fmt.Sprintf("Showing %d-%d of %d events", start+1, end, len(sorted)), []Button{
			{Name: "Next Page", Selector: "/events_next", IsHidden: "0"},
		})
	}
}

func handleUpcoming(s *State, uid string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	twoWeeks := now.AddDate(0, 0, 14)

	var upcoming []*Event
	for _, evt := range s.events {
		t := parseEventDate(evt.StartDate)
		if t != nil && t.After(now) && t.Before(twoWeeks) {
			upcoming = append(upcoming, evt)
		}
	}

	if len(upcoming) == 0 {
		sendText(uid, "Nothing in the next 2 weeks. Send /events to see all upcoming events.")
		return
	}

	sort.Slice(upcoming, func(i, j int) bool { return upcoming[i].StartDate < upcoming[j].StartDate })

	text := fmt.Sprintf("Events in the next 2 weeks (%d):\n\n", len(upcoming))
	for _, evt := range upcoming {
		text += fmt.Sprintf("- %s | %s | %s\n", evt.Name, evt.StartDate, evt.Location)
	}
	sendText(uid, text)
}

func handleApply(s *State, uid, query string) {
	evt := s.findEventByName(query)
	if evt == nil {
		sendText(uid, fmt.Sprintf("Can't find event matching '%s'. Try /events to browse.", query))
		return
	}

	text := fmt.Sprintf("Application Info: %s\n%s\n\n", evt.Name, strings.Repeat("-", 30))
	text += fmt.Sprintf("Event: %s - %s\n", evt.StartDate, evt.EndDate)
	text += fmt.Sprintf("Location: %s\n", evt.Location)

	if evt.AppStatus != "" {
		text += fmt.Sprintf("Status: %s\n", strings.ToUpper(evt.AppStatus))
	}
	if evt.AppDeadline != "" {
		dl := deadlineDaysLeft(evt.AppDeadline)
		text += fmt.Sprintf("Deadline: %s (%d days)\n", evt.AppDeadline, dl)
	}
	if evt.AppURL != "" {
		text += fmt.Sprintf("Apply: %s\n", evt.AppURL)
	}
	if evt.PrizePool != "" {
		text += fmt.Sprintf("Prizes: %s\n", evt.PrizePool)
	}
	if evt.Travel {
		text += "Travel: Funded!\n"
	}

	text += fmt.Sprintf("\n%s\n%s", evt.AISummary, evt.URL)
	sendText(uid, text)
}

func handleDeadlines(s *State, uid string) {
	dls := eventDeadlines(s)
	if len(dls) == 0 {
		sendText(uid, "No deadlines tracked.")
		return
	}

	text := "Application Deadlines\n" + strings.Repeat("=", 25) + "\n\n"

	// Split into active and passed
	var active, passed []Deadline
	for _, d := range dls {
		if d.DaysLeft >= 0 {
			active = append(active, d)
		} else {
			passed = append(passed, d)
		}
	}

	if len(active) > 0 {
		text += "UPCOMING:\n"
		for _, d := range active {
			urgency := ""
			if d.DaysLeft <= 1 {
				urgency = " TOMORROW!!"
			} else if d.DaysLeft <= 3 {
				urgency = " URGENT!!"
			} else if d.DaysLeft <= 7 {
				urgency = " SOON!"
			}
			text += fmt.Sprintf("  %s\n  %s | %d days left%s\n\n", d.Name, d.Date, d.DaysLeft, urgency)
		}
	}

	if len(passed) > 0 {
		text += "PASSED:\n"
		for _, d := range passed {
			text += fmt.Sprintf("  %s (%s)\n", d.Name, d.Date)
		}
	}

	sendText(uid, text)
}

func handleScrape(s *State, uid string) {
	sendText(uid, fmt.Sprintf("Scraping %d Luma calendars...", len(lumaCalendars)))
	s.mu.Lock()
	newCount := scrapeAllCalendars(s)
	total := len(s.events)
	s.mu.Unlock()

	if newCount == 0 {
		sendText(uid, fmt.Sprintf("No new events found. Tracking %d total.", total))
	} else {
		sendText(uid, fmt.Sprintf("Found %d new events! %d total. Send /events to browse.", newCount, total))
		if openaiKey != "" {
			go func() {
				s.mu.RLock()
				var toEnrich []*Event
				for _, evt := range s.events {
					if evt.AISummary == "" {
						toEnrich = append(toEnrich, evt)
					}
				}
				s.mu.RUnlock()
				for i, evt := range toEnrich {
					if i >= 10 {
						break
					}
					enrichEvent(evt)
					time.Sleep(500 * time.Millisecond)
				}
				if len(toEnrich) > 0 {
					sendText(uid, fmt.Sprintf("AI-enriched %d events with summaries & vibes.", min(len(toEnrich), 10)))
				}
			}()
		}
	}
}

func handleAddDeadline(s *State, uid, args string) {
	parts := strings.SplitN(args, ",", 2)
	if len(parts) != 2 {
		sendText(uid, "Usage:\n/adddeadline Name,2026-04-15\n/adddeadline Name,2026-04-15 14:30")
		return
	}
	name := strings.TrimSpace(parts[0])
	dateStr := strings.TrimSpace(parts[1])

	// Validate - support date or datetime
	valid := false
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if _, err := time.Parse(layout, dateStr); err == nil {
			valid = true
			break
		}
	}
	if !valid {
		sendText(uid, "Invalid format. Use YYYY-MM-DD or YYYY-MM-DD HH:MM")
		return
	}

	s.mu.Lock()
	s.deadlines = append(s.deadlines, Deadline{Name: name, Date: dateStr})
	s.mu.Unlock()

	dl := deadlineDaysLeft(dateStr)
	sendText(uid, fmt.Sprintf("Tracking: %s\nDeadline: %s (%d days)\nYou'll get alerts at 1 week, 3 days, 24h, 12h, 6h, 3h, 1h, and 30min.", name, dateStr, dl))
}

func handleTestReminder(s *State, uid string) {
	// Add a deadline 10 seconds from now — will trigger immediately on next reminder check
	testTime := time.Now().Add(10 * time.Second)
	testTimeStr := testTime.Format("2006-01-02 15:04")
	s.mu.Lock()
	s.deadlines = append(s.deadlines, Deadline{Name: "TEST DEADLINE (demo)", Date: testTimeStr})
	s.mu.Unlock()
	sendText(uid, fmt.Sprintf("Test deadline set for %s!\nAlert incoming in seconds...", testTimeStr))
}

func handleTestReminderSoon(s *State, uid string) {
	// 1 hour + 1 min from now to trigger the "1 HOUR" threshold
	testTime := time.Now().Add(61 * time.Minute)
	testTimeStr := testTime.Format("2006-01-02 15:04")
	s.mu.Lock()
	s.deadlines = append(s.deadlines, Deadline{Name: "DEMO HACKATHON APPLICATION", Date: testTimeStr})
	s.mu.Unlock()
	sendText(uid, fmt.Sprintf("Demo deadline set for %s (1 hour from now).\nYou'll get the '1 HOUR' alert on the next check cycle (~1 min).", testTimeStr))
}

func handleCalendars(uid string) {
	text := fmt.Sprintf("Tracking %d Luma Calendars:\n\n", len(lumaCalendars))
	for _, cal := range lumaCalendars {
		parts := strings.Split(cal, "/")
		text += fmt.Sprintf("- %s\n", parts[len(parts)-1])
	}
	sendText(uid, text)
}

func handleInterest(s *State, uid, action, sid string) {
	evt := s.findEventByShortID(sid)
	if evt == nil {
		sendText(uid, "Event not found.")
		return
	}
	s.ensureUser(uid)
	s.mu.Lock()
	if s.interests[uid] == nil {
		s.interests[uid] = make(map[string]string)
	}
	s.interests[uid][evt.ID] = action
	counts := map[string]int{}
	for _, ui := range s.interests {
		if st := ui[evt.ID]; st != "" {
			counts[st]++
		}
	}
	s.mu.Unlock()

	m := map[string]string{"interested": "interested in", "going": "going to", "needteam": "looking for a team at"}
	msg := fmt.Sprintf("You're %s %s!", m[action], evt.Name)
	if counts["going"]+counts["interested"]+counts["needteam"] > 1 {
		msg += fmt.Sprintf("\n%d interested | %d going | %d need team", counts["interested"], counts["going"], counts["needteam"])
	}
	sendText(uid, msg)
}

func handleApplyButton(s *State, uid, sid string) {
	evt := s.findEventByShortID(sid)
	if evt == nil {
		sendText(uid, "Event not found.")
		return
	}
	if evt.AppURL != "" {
		sendText(uid, fmt.Sprintf("Apply for %s:\n%s\n\nDeadline: %s", evt.Name, evt.AppURL, evt.AppDeadline))
	} else {
		sendText(uid, fmt.Sprintf("Apply at: %s", evt.URL))
	}
}

func handleSkills(s *State, uid, text string) {
	u := s.ensureUser(uid)
	var cleaned []string
	for _, sk := range strings.Split(text, ",") {
		if sk = strings.TrimSpace(sk); sk != "" {
			cleaned = append(cleaned, sk)
		}
	}
	s.mu.Lock()
	u.Skills = cleaned
	s.mu.Unlock()
	sendText(uid, fmt.Sprintf("Skills set: %s\n\nThese help with team matching. Mark 'Need Team' on events to find teammates!", strings.Join(cleaned, ", ")))
}

func handleStatus(s *State, uid string) {
	u := s.ensureUser(uid)
	s.mu.RLock()
	defer s.mu.RUnlock()

	skills := "Not set (use /skills to add)"
	if len(u.Skills) > 0 {
		skills = strings.Join(u.Skills, ", ")
	}

	text := fmt.Sprintf("Your HackAgent Profile\n%s\n\nSkills: %s\n\nEvents:\n", strings.Repeat("=", 25), skills)

	ui := s.interests[uid]
	if len(ui) == 0 {
		text += "None yet - browse with /events!"
	} else {
		for eid, status := range ui {
			name := eid
			if evt := s.events[eid]; evt != nil {
				name = evt.Name
			}
			icon := map[string]string{"interested": "~", "going": "+", "needteam": "?"}
			text += fmt.Sprintf("  [%s] %s: %s\n", icon[status], name, status)
		}
	}
	sendText(uid, text)
}

func handleTeam(s *State, uid string) {
	s.ensureUser(uid)
	s.mu.RLock()
	defer s.mu.RUnlock()

	ui := s.interests[uid]
	var needTeam []string
	for eid, st := range ui {
		if st == "needteam" {
			needTeam = append(needTeam, eid)
		}
	}
	if len(needTeam) == 0 {
		sendText(uid, "Mark 'Need Team' on an event first! Browse with /events")
		return
	}

	userSkills := map[string]bool{}
	if u := s.users[uid]; u != nil {
		for _, sk := range u.Skills {
			userSkills[sk] = true
		}
	}

	for _, eid := range needTeam {
		evtName := eid
		if evt := s.events[eid]; evt != nil {
			evtName = evt.Name
		}
		var lines []string
		for oUID, oi := range s.interests {
			if oUID == uid || oi[eid] != "needteam" {
				continue
			}
			ou := s.users[oUID]
			if ou == nil {
				continue
			}
			comp := 0
			for _, sk := range ou.Skills {
				if !userSkills[sk] {
					comp++
				}
			}
			sk := "no skills set"
			if len(ou.Skills) > 0 {
				sk = strings.Join(ou.Skills, ", ")
			}
			lines = append(lines, fmt.Sprintf("- %s: %s (complementarity: %d)", ou.DisplayName, sk, comp))
		}
		if len(lines) > 0 {
			sendText(uid, fmt.Sprintf("Team matches for %s:\n\n%s", evtName, strings.Join(lines, "\n")))
		} else {
			sendText(uid, fmt.Sprintf("No team matches yet for %s. Share the bot to get more people in!", evtName))
		}
	}
}

func handleAIChat(s *State, uid, text string) {
	if openaiKey == "" {
		// Fallback: try to match intent without AI
		lo := strings.ToLower(text)
		if strings.Contains(lo, "deadline") || strings.Contains(lo, "apply") || strings.Contains(lo, "application") {
			handleDeadlines(s, uid)
			return
		}
		if strings.Contains(lo, "event") || strings.Contains(lo, "hackathon") || strings.Contains(lo, "hack") {
			handleEvents(s, uid, 0)
			return
		}
		sendText(uid, "I didn't understand that. Try /help to see what I can do!")
		return
	}

	s.mu.RLock()
	var evtCtx string
	i := 0
	for _, evt := range s.events {
		if i >= 20 {
			break
		}
		sum := evt.AISummary
		if sum == "" {
			sum = trunc(evt.Description, 80)
		}
		line := fmt.Sprintf("- %s (%s, %s): %s", evt.Name, evt.StartDate, evt.Location, sum)
		if evt.AppDeadline != "" {
			line += fmt.Sprintf(" [Apply by %s, status: %s]", evt.AppDeadline, evt.AppStatus)
		}
		if evt.PrizePool != "" {
			line += fmt.Sprintf(" [Prize: %s]", evt.PrizePool)
		}
		evtCtx += line + "\n"
		i++
	}
	var dlCtx string
	for _, d := range eventDeadlines(s) {
		dlCtx += fmt.Sprintf("- %s: %s (%d days)\n", d.Name, d.Date, d.DaysLeft)
	}
	s.mu.RUnlock()

	resp, err := llmChat(
		`You are HackAgent, a hackathon & tech community bot on Luffa messenger built for the Encode AI Hackathon. You help students discover hackathons, fellowships, and tech opportunities.

Your personality: helpful, slightly cheeky British tone, encouraging. You know about all the events and deadlines listed below. Give specific, actionable advice. Keep responses to 3-4 sentences max. If someone asks about events, reference specific ones from your knowledge base.

If they ask something unrelated to hackathons/tech/events, briefly answer but steer them back.`,
		fmt.Sprintf("Events I know about:\n%s\nUpcoming deadlines:\n%s\nUser message: %s", evtCtx, dlCtx, text),
	)
	if err != nil {
		log.Printf("[ai] %v", err)
		sendText(uid, "AI hiccup. Try /help for commands.")
		return
	}
	sendText(uid, resp)
}

// ============================================================================
// Router
// ============================================================================

var btnRe = regexp.MustCompile(`^(interested|going|needteam)_(.+)$`)
var applyRe = regexp.MustCompile(`^apply_(.+)$`)

func route(s *State, uid, text string) {
	if text == "" {
		return
	}
	s.ensureUser(uid)
	lo := strings.ToLower(strings.TrimSpace(text))
	cmd := strings.TrimPrefix(lo, "/")

	log.Printf("[route] uid=%s cmd=%q", uid, cmd)

	// Button actions
	if m := btnRe.FindStringSubmatch(lo); m != nil {
		handleInterest(s, uid, m[1], m[2])
		return
	}
	if m := applyRe.FindStringSubmatch(lo); m != nil {
		handleApplyButton(s, uid, m[1])
		return
	}

	switch {
	case cmd == "help" || cmd == "h" || cmd == "start":
		handleHelp(uid)
	case cmd == "events" || cmd == "e":
		handleEvents(s, uid, 0)
	case cmd == "events_next":
		// Simple pagination via conversation context
		s.mu.Lock()
		page := 1
		if ctx := s.convCtx[uid]; strings.HasPrefix(ctx, "events_page_") {
			fmt.Sscanf(ctx, "events_page_%d", &page)
			page++
		}
		s.convCtx[uid] = fmt.Sprintf("events_page_%d", page)
		s.mu.Unlock()
		handleEvents(s, uid, page)
	case cmd == "scrape":
		go handleScrape(s, uid)
	case cmd == "deadlines" || cmd == "dl" || cmd == "d":
		handleDeadlines(s, uid)
	case cmd == "upcoming" || cmd == "soon" || cmd == "u":
		handleUpcoming(s, uid)
	case strings.HasPrefix(cmd, "apply "):
		handleApply(s, uid, strings.TrimPrefix(cmd, "apply "))
	case strings.HasPrefix(cmd, "adddeadline "):
		handleAddDeadline(s, uid, strings.TrimSpace(text[strings.Index(lo, "adddeadline")+12:]))
	case cmd == "testreminder" || cmd == "tr":
		handleTestReminder(s, uid)
	case cmd == "calendars" || cmd == "cal":
		handleCalendars(uid)
	case strings.HasPrefix(cmd, "skills "):
		handleSkills(s, uid, strings.TrimSpace(text[strings.Index(lo, "skills")+7:]))
	case cmd == "status" || cmd == "s":
		handleStatus(s, uid)
	case cmd == "team" || cmd == "t":
		handleTeam(s, uid)
	default:
		// Reset pagination on non-events command
		s.mu.Lock()
		delete(s.convCtx, uid)
		s.mu.Unlock()
		handleAIChat(s, uid, text)
	}
}

// ============================================================================
// Helpers
// ============================================================================

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func deadlineDaysLeft(dateStr string) int {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return -1
	}
	return int(math.Ceil(t.Sub(time.Now()).Hours() / 24))
}

func parseEventDate(dateStr string) *time.Time {
	for _, layout := range []string{"Jan 2, 2006", "2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, dateStr); err == nil {
			return &t
		}
	}
	return nil
}

// ============================================================================
// Deadline Reminder Loop
// ============================================================================

func reminderLoop(ctx context.Context, s *State) {
	ticker := time.NewTicker(10 * time.Second) // Check frequently for demo responsiveness
	defer ticker.Stop()

	sentReminders := make(map[string]bool) // "deadline_name:threshold" -> sent

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()

			s.mu.RLock()
			users := make([]string, 0, len(s.users))
			for uid := range s.users {
				users = append(users, uid)
			}

			// Collect all deadlines including event app deadlines with time support
			type dlCheck struct {
				name    string
				dateStr string
			}
			var checks []dlCheck
			for _, d := range s.deadlines {
				checks = append(checks, dlCheck{d.Name, d.Date})
			}
			for _, evt := range s.events {
				if evt.AppDeadline != "" {
					checks = append(checks, dlCheck{evt.Name + " application", evt.AppDeadline})
				}
			}
			s.mu.RUnlock()

			for _, dl := range checks {
				// Parse deadline - support both date-only and datetime
				var deadline time.Time
				var err error
				for _, layout := range []string{
					"2006-01-02 15:04",
					"2006-01-02T15:04:05Z07:00",
					"2006-01-02T15:04",
					"2006-01-02",
				} {
					deadline, err = time.Parse(layout, dl.dateStr)
					if err == nil {
						// If date-only, set to end of day (23:59)
						if layout == "2006-01-02" {
							deadline = deadline.Add(23*time.Hour + 59*time.Minute)
						}
						break
					}
				}
				if err != nil {
					continue
				}

				hoursLeft := deadline.Sub(now).Hours()

				// Reminder thresholds
				thresholds := []struct {
					hours float64
					label string
				}{
					{168, "1 week"},
					{72, "3 days"},
					{24, "24 hours"},
					{12, "12 hours"},
					{6, "6 hours"},
					{3, "3 hours"},
					{1, "1 HOUR"},
					{0.5, "30 MINUTES"},
				}

				for _, th := range thresholds {
					key := fmt.Sprintf("%s:%s", dl.name, th.label)
					if sentReminders[key] {
						continue
					}
					// Fire when we're at or below the threshold (and deadline hasn't passed)
					// Each threshold only fires once thanks to sentReminders
					if hoursLeft <= th.hours && hoursLeft > -1 {
						sentReminders[key] = true
						urgency := ""
						if hoursLeft <= 0 {
							urgency = " EXPIRED"
						} else if th.hours <= 1 {
							urgency = " FINAL WARNING"
						} else if th.hours <= 6 {
							urgency = " URGENT"
						}

						label := th.label
						if hoursLeft <= 0.017 { // ~1 minute
							label = "NOW"
						}

						msg := fmt.Sprintf("DEADLINE ALERT%s\n\n%s closes in %s!\n\nDeadline: %s\n\nDon't miss it!",
							urgency, dl.name, label, dl.dateStr)

						for _, uid := range users {
							sendText(uid, msg)
						}
						log.Printf("[reminder] Sent %s reminder for %s (%.1fh left)", th.label, dl.name, hoursLeft)
					}
				}
			}
		}
	}
}

// ============================================================================
// Main
// ============================================================================

func main() {
	loadEnv(".env")
	secret = envOr("LUFFA_BOT_SECRET", "f39a0f4b700b46a0a0e79b3a9d251b50")
	openaiKey = os.Getenv("OPENAI_API_KEY")

	log.Println("=== HackAgent v0.2.0 ===")
	log.Printf("Bot secret: %s...", secret[:8])
	if openaiKey != "" {
		log.Printf("OpenAI: configured (%s...)", openaiKey[:12])
	} else {
		log.Println("OpenAI: NOT configured (AI features disabled)")
	}

	state := NewState()
	seedData(state)

	state.mu.RLock()
	evtCount := len(state.events)
	dlCount := len(state.deadlines)
	// Count event deadlines too
	evtDLCount := 0
	for _, evt := range state.events {
		if evt.AppDeadline != "" {
			evtDLCount++
		}
	}
	state.mu.RUnlock()

	log.Printf("Events: %d | Deadlines: %d (+ %d from events) | Calendars: %d",
		evtCount, dlCount, evtDLCount, len(lumaCalendars))
	log.Println("Polling for messages...")

	go reminderLoop(context.Background(), state)

	for {
		msgs := pollMessages()
		for _, msg := range msgs {
			if msg.Text == "" {
				continue
			}
			state.mu.Lock()
			h := md5.Sum([]byte(msg.UID + ":" + msg.Text))
			id := fmt.Sprintf("%x", h)
			if state.seenMsgs[id] {
				state.mu.Unlock()
				continue
			}
			state.seenMsgs[id] = true
			state.mu.Unlock()

			log.Printf("[msg] %s: %s", msg.UID, msg.Text)
			route(state, msg.UID, msg.Text)
		}
		time.Sleep(1 * time.Second)
	}
}

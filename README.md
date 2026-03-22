# HackAgent

**An agentic hackathon operations hub built on the Luffa messaging platform.**

Built for the [Encode AI Hackathon 2026](https://www.encode.club/ai-hackathon) — LuffaNator (Agentic Track).

## What is HackAgent?

HackAgent is an AI-powered bot that lives inside [Luffa](https://luffa.im), the Web3 messaging platform. It automates hackathon discovery, deadline tracking, team matching, and event coordination — all through conversational commands and interactive buttons.

Instead of juggling Discord servers, Luma calendars, and spreadsheets to track hackathon opportunities, HackAgent brings everything into one place: your Luffa DMs.

## Features

### Event Discovery & Scraping
- Scrapes **19 Luma calendars** (BuildersBrew, Encode Club, KCL Tech, London Loop, etc.) for upcoming events
- AI-enriched event summaries with vibe checks, tags, and smart descriptions
- Interactive button cards for each event (Interested / Going / Need Team / Apply)
- Pagination for browsing large event lists

### Application Deadline Tracker
- Tracks application deadlines for hackathons, fellowships, and competitions
- Multi-threshold reminder system: alerts at **1 week, 3 days, 24h, 12h, 6h, 3h, 1 hour, and 30 minutes**
- Supports both date (`2026-04-15`) and datetime (`2026-04-15 14:30`) deadlines
- Pre-loaded with real opportunities: Stanford Ventures Fellowship, Google Summer of Code, HackMIT, Cambridge Battlecode, and more

### AI-Powered Chat
- Natural language Q&A about events, deadlines, and hackathon advice
- Context-aware: knows about all tracked events and deadlines
- Powered by OpenAI GPT-4o with a cheeky British personality

### Team Matching
- Set your skills (`/skills python,react,ml`)
- Mark events as "Need Team" to enter the matching pool
- Complementarity-based matching: pairs people with *different* skills for maximum coverage

### Community Intel
- Pre-loaded with curated events from the hackathon community
- Sources: Luma calendars, Discord community intelligence, manual curation
- Covers UK, Europe, and US hackathons, fellowships, and opportunities

## Commands

| Command | Description |
|---------|-------------|
| `/help` | Show all commands with quick-action buttons |
| `/events` | Browse events with interactive action buttons |
| `/deadlines` | View all upcoming deadlines with urgency indicators |
| `/upcoming` | Events in the next 2 weeks |
| `/scrape` | Fetch latest events from 19 Luma calendars |
| `/apply <name>` | Get application info for a specific event |
| `/skills a,b,c` | Set your skills for team matching |
| `/team` | Find team matches for events you're attending |
| `/status` | View your profile and event interests |
| `/calendars` | List all tracked Luma calendars |
| `/adddeadline Name,YYYY-MM-DD` | Add a custom deadline |
| `/testreminder` | Create a test deadline (2 min) to demo reminders |

Or just type naturally — the AI will help!

## Architecture

```
User (Luffa App)
      |
      | Encrypted DMs
      v
Luffa Robot API (apibot.luffa.im)
      |
      | Poll /robot/receive (1s interval)
      v
+----------------------------------+
|        HackAgent (Go)            |
|                                  |
|  [Poller] Message loop           |
|  [Router] Command + AI routing   |
|  [Scraper] 19 Luma calendars     |
|  [Enricher] OpenAI GPT-4o        |
|  [Reminder] Minute-level checks  |
|  [Matcher] Skill complementarity |
+----------------------------------+
      |
      | OpenAI API
      v
  GPT-4o (enrichment + chat)
```

- **Single Go binary** — no Docker, no database, no dependencies at runtime
- **Concurrent Luma scraping** — 4 calendars scraped in parallel
- **In-memory state** — fast, simple, sufficient for hackathon demo
- **Minute-level reminder loop** — checks deadlines every 60 seconds for hour-precision alerts

## Tech Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.25 |
| Bot API | Luffa Robot API (HTTP polling) |
| AI | OpenAI GPT-4o |
| Scraping | goquery (HTML parsing) |
| State | In-memory (sync.RWMutex) |

## Setup

### Prerequisites
- Go 1.21+
- A Luffa account and bot (create at [robot.luffa.im](https://robot.luffa.im))
- OpenAI API key (optional, for AI features)

### Run

```bash
# Clone
git clone https://github.com/keanucz/hackagent.git
cd hackagent

# Configure
cat > .env << EOF
LUFFA_BOT_SECRET=your_bot_secret_key
OPENAI_API_KEY=sk-your-openai-key
EOF

# Build & run
go build -o hackagent .
./hackagent
```

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `LUFFA_BOT_SECRET` | Yes | Bot secret from robot.luffa.im |
| `OPENAI_API_KEY` | No | Enables AI chat and event enrichment |

## Why Luffa?

Luffa is a Web3-native encrypted messaging platform with first-class bot support. HackAgent demonstrates how agentic systems can:

1. **Automate tasks** — Event scraping, AI enrichment, deadline reminders
2. **Coordinate users** — Team matching, interest tracking, community building
3. **Interact on Luffa** — Bot messages with interactive buttons, natural conversation

## Built by

**Keanu** — solo build for the Encode AI Hackathon 2026, LuffaNator track.

## License

MIT

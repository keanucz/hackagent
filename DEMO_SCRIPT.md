# Demo Script (90 seconds)

## Screen: Phone with Luffa open, DM with HackAgent

### 1. Intro (10s)
"This is HackAgent — an AI-powered hackathon bot built on Luffa for the Encode AI Hackathon. It discovers events, tracks deadlines, matches teams, and answers questions — all inside Luffa DMs."

### 2. Help command (10s)
Send: `help`
"It responds with interactive buttons and a full command list. Let's browse some events."

### 3. Events with buttons (15s)
Tap: "Browse Events" button (or send `/events`)
"Each event comes as a rich card with AI-generated summaries, vibe checks, deadline info, and action buttons. I can mark myself as Interested, Going, or Need Team."

Tap: "Going!" on an event

### 4. Deadline tracker (15s)
Send: `deadlines`
"The deadline tracker shows all upcoming applications sorted by urgency. Stanford Ventures Fellowship, Google Summer of Code, HackMIT — with days-left counters and urgency flags."

### 5. Live reminder demo (15s)
Send: `/testreminder`
"I just created a test deadline 2 minutes from now. The bot checks every minute and sends alerts at 1 week, 3 days, 24h, 12h, 6h, 3h, 1 hour, and 30 minutes before deadlines."

Wait for the reminder to pop in.

### 6. AI chat (15s)
Send: "What hackathons should I go to in April?"
"The AI knows about all tracked events and gives personalised recommendations. It's powered by GPT-4o with context about every event and deadline."

### 7. Luma scraping (10s)
Send: `/scrape`
"HackAgent scrapes 19 Luma calendars concurrently — from Encode Club to KCL Tech to London Loop. New events get AI-enriched automatically."

### 8. Closing (10s)
"Single Go binary. No database needed. Polls Luffa every second, checks deadlines every minute, and scrapes Luma on demand. Built solo for the Encode AI Hackathon LuffaNator track."

---
name: browser-based-website-extraction
description: "FALLBACK for WebFetch failures: Use when WebFetch returns 403, 401, empty content, or anti-bot blocks. Also use when a page requires JS rendering, interaction (clicking, navigating tabs, filling search filters), or when you need to extract structured content from complex live websites. This skill drives a real Chrome browser via the rodney CLI to bypass bot detection and render dynamic content."
---

# Web Extract

## Overview

Load a webpage using rodney and extract specific information, driven by a stated goal. Uses DOM queries as the primary extraction method, with screenshots only as a last resort.

**Core principle:** DOM-first extraction with goal-driven adaptive depth.

**Announce at start:** "I'm using the web-extract skill to fetch information from [URL]."

## Phase 1: Setup

### Start Rodney

Create a new session with `rodney newsession '<url>'`. The command prints a session ID; capture it and pass `--session <id>` on all subsequent commands.

```bash
SESSION=$(rodney newsession '<url>')
```

If `newsession` fails due to stealth issues, retry without stealth:
```bash
SESSION=$(rodney newsession '<url>' --no-stealth)
```
Note: "Stealth mode unavailable, proceeding without it."

If the caller passes an existing session ID, skip `newsession` and use `--session <id>` directly.

### Access Checks

After the page loads, check for login walls, CAPTCHAs, or access-denied content:

```bash
rodney title --session $SESSION
rodney js 'document.body.innerText.substring(0, 500)' --session $SESSION
```

Look for indicators like "sign in", "access denied", "403", "captcha", "verify you are human".

**If blocked:**
- If the page is critical to the goal: pause and ask the user to manually intervene in the browser window. Continue once they confirm.
- Otherwise: bail with a clear description of the blocker (e.g. "chatgpt.com requires login"). The calling agent decides whether to retry.

## Phase 2: Extract

**Always try DOM queries before anything else.** Never screenshot just to "see what's there."

### Extraction hierarchy (use in order):

**1. Structured DOM queries (preferred)**

```bash
# Page basics
rodney title --session $SESSION
rodney url --session $SESSION

# Element text
rodney text '<selector>' --session $SESSION

# JavaScript for complex extraction
rodney js '<expression>' --session $SESSION

# Attributes
rodney attr '<selector>' '<name>' --session $SESSION

# Count elements
rodney count '<selector>' --session $SESSION

# Raw HTML when structure matters
rodney html '<selector>' --session $SESSION
```

A good starting point for an unfamiliar page:
```bash
rodney title --session $SESSION
rodney js 'document.body.innerText.substring(0, 3000)' --session $SESSION
```

**2. Accessibility tree (when selectors aren't obvious)**

```bash
rodney ax-tree --depth 4 --session $SESSION
```

Useful for understanding page structure without visual inspection.

**3. Screenshot (last resort only)**

Only use when DOM + accessibility tree genuinely can't answer the question:
- Canvas-rendered content
- Charts, graphs, or diagrams
- Images containing text
- Complex visual layouts where spatial relationships matter

```bash
rodney screenshot /tmp/page.png --session $SESSION
```

When used, note it in the final output: "Note: used a screenshot to interpret [reason]."

## Phase 3: Evaluate

Compare extracted content against the caller's stated goal:

- **Goal met** -- Extracted content fully answers the question. Go to Phase 5 (Report).
- **Goal partially met** -- Some information but not enough. Identify what's missing. Go to Phase 4 (Pursue).
- **Goal not met** -- Page didn't have useful content. Go to Phase 4 (Pursue) or bail if no leads exist.

**Calibrate depth to the request:**
- Caller asked for a summary and you have one: stop, don't over-pursue
- Caller asked for detailed information and you only have a headline: keep going

## Phase 4: Pursue

When the goal isn't fully met, follow leads to gather more information.

### Identify leads

```bash
# Extract relevant links
rodney js 'JSON.stringify(Array.from(document.querySelectorAll("a[href]")).map(a => ({text: a.innerText.trim(), href: a.href})).filter(a => a.text))' --session $SESSION
```

Rank by relevance to the goal. Pursue the best lead first.

### Follow leads

**Direct links:**
```bash
rodney open '<url>' --session $SESSION
rodney waitstable --session $SESSION
```

**Interactive elements (tabs, "show more", expandable sections):**
```bash
rodney click '<selector>' --session $SESSION
rodney waitstable --session $SESSION
```

**Search boxes and filters:**
```bash
rodney input '<selector>' '<query>' --session $SESSION
rodney click '<submit-button-selector>' --session $SESSION
# or
rodney submit '<form-selector>' --session $SESSION
rodney waitstable --session $SESSION
```

**Filter dropdowns:**
```bash
rodney select '<selector>' '<value>' --session $SESSION
rodney waitstable --session $SESSION
```

After following a lead, loop back to **Phase 2 (Extract)**.

### Depth limits

- **Maximum 5 pages** deep from the starting URL (unless caller requests exhaustive exploration)
- **Maximum 3 minutes** total wall time before reporting what was gathered
- If a lead is irrelevant: `rodney back --session $SESSION` and try the next one

### Don't pursue

- Links to unrelated domains (unless the goal requires it)
- Login-gated content (bail with description per access check policy)
- Infinite scroll / pagination beyond the first few pages (unless explicitly asked)

### Stealth retry

If page interactions are failing (clicks not registering, inputs not working, elements unresponsive), this may be caused by stealth mode breaking page functionality:

1. `rodney endsession $SESSION`
2. `SESSION=$(rodney newsession '<url>' --no-stealth)`
3. Continue from Phase 2

## Phase 5: Report

### Return results

- Format output to match what was asked (plain text, bullet points, key-value pairs, tables, etc.)
- **Include provenance** -- which URL(s) the information came from
- **Note limitations** -- what's missing and why (e.g. "pricing page required login", "chart data was in an image")
- **Note screenshots** -- if any were used, briefly explain why DOM extraction wasn't sufficient

### Cleanup

Always end the session when done:
```bash
rodney endsession $SESSION
```

If the caller may want follow-up queries, leave the session running instead and report the session ID:
```
Browser left running. To reuse: --session <id>
```

## Flow Summary

```
Setup -> Extract -> Evaluate
                    |-- Goal met -> Report
                    +-- Goal not met -> Pursue -> Extract -> Evaluate (loop)
                                                             +-- Exhausted -> Report
```

## Rodney Command Quick Reference

| Category | Commands |
|----------|----------|
| Sessions | `newsession [url]`, `endsession [id]`, `sessions` |
| Navigation | `open <url>`, `back`, `forward`, `reload` |
| Extraction | `js <expr>`, `text <sel>`, `html [sel]`, `attr <sel> <name>`, `title`, `url` |
| Interaction | `click <sel>`, `input <sel> <text>`, `clear <sel>`, `select <sel> <val>`, `submit <sel>`, `hover <sel>` |
| Waiting | `wait <sel>`, `waitstable`, `waitload`, `waitidle`, `sleep <N>` |
| Checks | `exists <sel>`, `visible <sel>`, `count <sel>`, `assert <expr> [expected]` |
| Accessibility | `ax-tree [--depth N]`, `ax-find [--name N] [--role R]`, `ax-node <sel>` |
| Screenshots | `screenshot [file]`, `screenshot-el <sel> [file]` |

**All commands must end with `--session <id>`** to target the correct session.

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Screenshotting to "see what's there" | Use `rodney title` + `rodney js 'document.body.innerText.substring(0, 3000)'` first |
| Polling with `rodney exists` in a loop (triggers permission prompts) | Use `rodney waitstable` or `sleep N` instead |
| Forgetting `--session` suffix | Every rodney command needs `--session <id>` for session targeting |
| Over-pursuing when the goal is already met | Re-read the caller's goal before following another link |
| Not cleaning up sessions | Always `rodney endsession $SESSION` when done |

## Red Flags

**Never:**
- Screenshot as a first step on any page
- Follow more than 5 pages deep without explicit request
- Attempt to log in or enter credentials without caller direction
- Leave a session running after finishing (unless caller wants follow-up)

**Always:**
- Try at least one DOM query before screenshotting
- Include source URLs in the output
- Note when screenshots were used and why
- Use `rodney newsession '<url>'` to start unless caller provides an existing session ID

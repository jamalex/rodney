---
name: web-extract
description: Use when you need to load a webpage and extract specific information from it — articles, data, search results, or any structured content from live websites
---

# Web Extract

## Overview

Load a webpage using rodney and extract specific information, driven by a stated goal. Uses DOM queries as the primary extraction method, with screenshots only as a last resort.

**Core principle:** DOM-first extraction with goal-driven adaptive depth.

**Announce at start:** "I'm using the web-extract skill to fetch information from [URL]."

## Phase 1: Setup

### Session Isolation

Every invocation creates a fresh session to avoid conflicts with other agents:

```bash
export RODNEY_HOME=$(mktemp -d /tmp/rodney-session-XXXXXX)
```

If the caller passes an existing `RODNEY_HOME`, reuse that session instead of creating a new one.

### Start Rodney

Determine flags from caller's intent:

| Caller says | Flags |
|-------------|-------|
| Nothing / default | `--stealth` |
| Wants to watch or interact | `--show --stealth` |
| Explicitly no stealth | (no flags) |

```bash
RODNEY_HOME=$RODNEY_HOME rodney start [flags]
```

If `start --stealth` fails, retry without `--stealth` and note: "Stealth mode unavailable, proceeding without it."

### Navigate

```bash
RODNEY_HOME=$RODNEY_HOME rodney open '<url>'
RODNEY_HOME=$RODNEY_HOME rodney waitstable
```

### Access Checks

After the page loads, check for login walls, CAPTCHAs, or access-denied content:

```bash
RODNEY_HOME=$RODNEY_HOME rodney title
RODNEY_HOME=$RODNEY_HOME rodney js 'document.body.innerText.substring(0, 500)'
```

Look for indicators like "sign in", "access denied", "403", "captcha", "verify you are human".

**If blocked:**
- In `--show` mode and the page is critical to the goal: pause and ask the user to manually intervene in the visible browser window. Continue once they confirm.
- Otherwise: bail with a clear description of the blocker (e.g. "chatgpt.com requires login — retry with --show to log in manually"). The calling agent decides whether to retry.

## Phase 2: Extract

**Always try DOM queries before anything else.** Never screenshot just to "see what's there."

### Extraction hierarchy (use in order):

**1. Structured DOM queries (preferred)**

```bash
# Page basics
RODNEY_HOME=$RODNEY_HOME rodney title
RODNEY_HOME=$RODNEY_HOME rodney url

# Element text
RODNEY_HOME=$RODNEY_HOME rodney text '<selector>'

# JavaScript for complex extraction
RODNEY_HOME=$RODNEY_HOME rodney js '<expression>'

# Attributes
RODNEY_HOME=$RODNEY_HOME rodney attr '<selector>' '<name>'

# Count elements
RODNEY_HOME=$RODNEY_HOME rodney count '<selector>'

# Raw HTML when structure matters
RODNEY_HOME=$RODNEY_HOME rodney html '<selector>'
```

A good starting point for an unfamiliar page:
```bash
RODNEY_HOME=$RODNEY_HOME rodney title
RODNEY_HOME=$RODNEY_HOME rodney js 'document.body.innerText.substring(0, 3000)'
```

**2. Accessibility tree (when selectors aren't obvious)**

```bash
RODNEY_HOME=$RODNEY_HOME rodney ax-tree --depth 4
```

Useful for understanding page structure without visual inspection.

**3. Screenshot (last resort only)**

Only use when DOM + accessibility tree genuinely can't answer the question:
- Canvas-rendered content
- Charts, graphs, or diagrams
- Images containing text
- Complex visual layouts where spatial relationships matter

```bash
RODNEY_HOME=$RODNEY_HOME rodney screenshot /tmp/page.png
```

When used, note it in the final output: "Note: used a screenshot to interpret [reason]."

## Phase 3: Evaluate

Compare extracted content against the caller's stated goal:

- **Goal met** — Extracted content fully answers the question. Go to Phase 5 (Report).
- **Goal partially met** — Some information but not enough. Identify what's missing. Go to Phase 4 (Pursue).
- **Goal not met** — Page didn't have useful content. Go to Phase 4 (Pursue) or bail if no leads exist.

**Calibrate depth to the request:**
- Caller asked for a summary and you have one → stop, don't over-pursue
- Caller asked for detailed information and you only have a headline → keep going

## Phase 4: Pursue

When the goal isn't fully met, follow leads to gather more information.

### Identify leads

```bash
# Extract relevant links
RODNEY_HOME=$RODNEY_HOME rodney js 'JSON.stringify(Array.from(document.querySelectorAll("a[href]")).map(a => ({text: a.innerText.trim(), href: a.href})).filter(a => a.text))'
```

Rank by relevance to the goal. Pursue the best lead first.

### Follow leads

**Direct links:**
```bash
RODNEY_HOME=$RODNEY_HOME rodney open '<url>'
RODNEY_HOME=$RODNEY_HOME rodney waitstable
```

**Interactive elements (tabs, "show more", expandable sections):**
```bash
RODNEY_HOME=$RODNEY_HOME rodney click '<selector>'
RODNEY_HOME=$RODNEY_HOME rodney waitstable
```

**Search boxes and filters:**
```bash
RODNEY_HOME=$RODNEY_HOME rodney input '<selector>' '<query>'
RODNEY_HOME=$RODNEY_HOME rodney click '<submit-button-selector>'
# or
RODNEY_HOME=$RODNEY_HOME rodney submit '<form-selector>'
RODNEY_HOME=$RODNEY_HOME rodney waitstable
```

**Filter dropdowns:**
```bash
RODNEY_HOME=$RODNEY_HOME rodney select '<selector>' '<value>'
RODNEY_HOME=$RODNEY_HOME rodney waitstable
```

After following a lead, loop back to **Phase 2 (Extract)**.

### Depth limits

- **Maximum 5 pages** deep from the starting URL (unless caller requests exhaustive exploration)
- **Maximum 3 minutes** total wall time before reporting what was gathered
- If a lead is irrelevant: `RODNEY_HOME=$RODNEY_HOME rodney back` and try the next one

### Don't pursue

- Links to unrelated domains (unless the goal requires it)
- Login-gated content (bail with description per access check policy)
- Infinite scroll / pagination beyond the first few pages (unless explicitly asked)

### Stealth retry

If page interactions are failing (clicks not registering, inputs not working, elements unresponsive), this may be caused by stealth mode breaking page functionality:

1. `RODNEY_HOME=$RODNEY_HOME rodney stop`
2. `RODNEY_HOME=$RODNEY_HOME rodney start` (without `--stealth`)
3. Re-navigate to the last working URL
4. Continue from Phase 2

## Phase 5: Report

### Return results

- Format output to match what was asked (plain text, bullet points, key-value pairs, tables, etc.)
- **Include provenance** — which URL(s) the information came from
- **Note limitations** — what's missing and why (e.g. "pricing page required login", "chart data was in an image")
- **Note screenshots** — if any were used, briefly explain why DOM extraction wasn't sufficient

### Cleanup

**`--show` mode:** Leave rodney running. Report the `RODNEY_HOME` value so the caller can reuse the session for follow-ups:
```
Browser left running. To reuse: RODNEY_HOME=<path>
```

**Headless mode:** Stop rodney and clean up:
```bash
RODNEY_HOME=$RODNEY_HOME rodney stop
rm -rf "$RODNEY_HOME"
```

## Flow Summary

```
Setup → Extract → Evaluate
                    ├─ Goal met → Report
                    └─ Goal not met → Pursue → Extract → Evaluate (loop)
                                                          └─ Exhausted → Report
```

## Rodney Command Quick Reference

| Category | Commands |
|----------|----------|
| Lifecycle | `start [--show] [--stealth]`, `stop`, `status` |
| Navigation | `open <url>`, `back`, `forward`, `reload` |
| Extraction | `js <expr>`, `text <sel>`, `html [sel]`, `attr <sel> <name>`, `title`, `url` |
| Interaction | `click <sel>`, `input <sel> <text>`, `clear <sel>`, `select <sel> <val>`, `submit <sel>`, `hover <sel>` |
| Waiting | `wait <sel>`, `waitstable`, `waitload`, `waitidle`, `sleep <N>` |
| Checks | `exists <sel>`, `visible <sel>`, `count <sel>`, `assert <expr> [expected]` |
| Accessibility | `ax-tree [--depth N]`, `ax-find [--name N] [--role R]`, `ax-node <sel>` |
| Screenshots | `screenshot [file]`, `screenshot-el <sel> [file]` |
| Tabs | `pages`, `page <idx>`, `newpage [url]`, `closepage` |

**All commands must be prefixed with `RODNEY_HOME=$RODNEY_HOME`** to maintain session isolation.

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Screenshotting to "see what's there" | Use `rodney title` + `rodney js 'document.body.innerText.substring(0, 3000)'` first |
| Polling with `rodney exists` in a loop (triggers permission prompts) | Use `rodney waitstable` or `sleep N` instead |
| Forgetting `RODNEY_HOME` prefix | Every rodney command needs it for session isolation |
| Over-pursuing when the goal is already met | Re-read the caller's goal before following another link |
| Not cleaning up headless sessions | Always `rodney stop` + `rm -rf` in headless mode |

## Red Flags

**Never:**
- Screenshot as a first step on any page
- Follow more than 5 pages deep without explicit request
- Attempt to log in or enter credentials without caller direction
- Leave a headless session running after finishing

**Always:**
- Try at least one DOM query before screenshotting
- Include source URLs in the output
- Note when screenshots were used and why
- Create a fresh RODNEY_HOME unless caller provides one

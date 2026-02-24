# web-extract Skill Design

## Overview

A Claude Code skill that uses rodney to load webpages and extract specific information, using DOM queries as the primary extraction method and screenshots only as a last resort. Goal-driven with adaptive depth — follows links and interactions as needed until the caller's goal is satisfied.

**Location:** `/exports/projectpool/dev/rodney/.claude/skills/web-extract/SKILL.md`

## Phases

### 1. Setup

**Session isolation:** Each invocation creates a fresh `RODNEY_HOME` temp directory. Parallel agents never collide. If the caller passes an existing `RODNEY_HOME`, that session is reused instead.

**Start rodney:**
- Default flags: `--stealth` (no `--show`)
- If user wants to watch/interact: `--show --stealth`
- If user explicitly says no stealth: omit `--stealth`
- If `start --stealth` fails, retry without `--stealth` and note the fallback.

**Navigate:** `rodney open <url>`, then `rodney waitstable`.

**Access checks:** If the page shows a login wall, CAPTCHA, or access-denied content:
- In `--show` mode for a page critical to the goal: pause and ask the user to manually intervene in the visible browser, then continue once they confirm.
- Otherwise: bail with a clear description of the blocker. The caller can retry with `--show` if the page is important enough.

### 2. Extract

DOM-first extraction hierarchy:

1. **Structured DOM queries** — `rodney js`, `rodney text`, `rodney html`, `rodney attr`, `rodney count`. Always try at least one DOM query before anything else.
2. **Accessibility tree** — `rodney ax-tree` for semantic page structure when selectors aren't obvious.
3. **Screenshot (last resort)** — Only for canvas-rendered content, visual layouts, charts/images, or when DOM queries genuinely can't locate the content. When used, note it: "Note: used a screenshot to interpret [reason]."

**Key principle:** Never screenshot just to "see what's there." A `rodney title` + `rodney js 'document.body.innerText.substring(0, 2000)'` is almost always a better starting point.

### 3. Evaluate

Compare extracted content against the caller's stated goal:

- **Goal met** — Extracted content fully answers the question. Proceed to Report.
- **Goal partially met** — Some information gathered but not enough. Identify what's missing, proceed to Pursue.
- **Goal not met** — Page didn't have useful content. Proceed to Pursue or bail if no leads exist.

Don't over-pursue (caller asked for a summary and you have one — stop). Don't under-pursue (caller asked for detailed information and you only have a headline — keep going).

### 4. Pursue

When the goal isn't fully met, follow leads:

**Identify leads** — Use `rodney js` to extract links, buttons, or interactive elements relevant to the goal. Rank by likely relevance.

**Follow the best lead:**
- `rodney open <url>` for direct links
- `rodney click <selector>` for interactive elements (tabs, "show more", expandable sections)
- `rodney input` + `rodney submit` / `rodney click` for search boxes
- `rodney select` for filter dropdowns, `rodney click` for checkboxes
- Then `rodney waitstable` and loop back to Extract.

**Depth limits:**
- Maximum 5 pages deep unless caller requests exhaustive exploration
- Maximum 3 minutes wall time before reporting what was gathered
- If a lead is irrelevant, `rodney back` and try the next one

**Don't pursue:**
- Links to unrelated domains unless the goal requires it
- Login-gated content (bail with description per auth policy)
- Infinite scroll / pagination beyond first few pages unless explicitly asked

**Stealth retry:** If page interactions are failing (clicks not registering, inputs not working, elements unresponsive), stop rodney, restart without `--stealth`, re-navigate to the last working URL, and continue.

### 5. Report

**Return results:**
- Structured format relevant to what was asked (plain text, bullets, key-value pairs, etc.)
- Include provenance — which URL(s) the information came from
- Note limitations — what's missing and why (login required, data in unparseable image, etc.)
- Note screenshots used — briefly explain why DOM extraction wasn't sufficient

**Cleanup:**
- `--show` mode: leave rodney running, report the `RODNEY_HOME` value so the caller can reuse the session
- Headless mode: `rodney stop` and clean up the temp dir

## Flow

```
Setup → Extract → Evaluate
                    ├─ Goal met → Report
                    └─ Goal not met → Pursue → Extract → Evaluate (loop)
                                                          └─ Exhausted → Report
```

Cross-cutting concerns:
- DOM-first, screenshots as last resort (Extract)
- Stealth retry on interaction failures (Setup, Pursue)
- User unblock in --show mode (any phase)
- Session isolation via RODNEY_HOME (Setup)
- Leave running in --show, stop in headless (Report)

## Rodney Command Reference

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
| Tabs | `pages`, `page <index>`, `newpage [url]`, `closepage` |

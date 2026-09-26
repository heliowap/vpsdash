---
name: vpsdash
description: A private tailnet command center with the clarity of a field maintenance ledger.
colors:
  action: "#174c51"
  action-hover: "#0f383c"
  on-action: "#ffffff"
  healthy: "#296451"
  healthy-paper: "#e0ebe4"
  attention: "#ad421f"
  failure-paper: "#f4e4dc"
  paper: "#f5f3ee"
  raised-paper: "#fffdfa"
  rail-paper: "#eeede8"
  selected-paper: "#dce8e4"
  editor-paper: "#ecefe9"
  preview-paper: "#e9f0eb"
  ink: "#242b2a"
  muted-ink: "#59625f"
  rule: "#d7dcd6"
typography:
  brand:
    fontFamily: "IBM Plex Sans, sans-serif"
    fontSize: "22px"
    fontWeight: 700
    letterSpacing: "-.025em"
  headline:
    fontFamily: "IBM Plex Sans, sans-serif"
    fontSize: "clamp(28px, 3.5vw, 42px)"
    fontWeight: 600
    lineHeight: 1.12
    letterSpacing: "-.035em"
  section-title:
    fontFamily: "IBM Plex Sans, sans-serif"
    fontSize: "20px"
    fontWeight: 600
    lineHeight: 1.2
    letterSpacing: "-.02em"
  body:
    fontFamily: "IBM Plex Sans, sans-serif"
    fontSize: "15px"
    fontWeight: 400
    lineHeight: 1.55
  label:
    fontFamily: "IBM Plex Sans, sans-serif"
    fontSize: "11px"
    fontWeight: 700
    letterSpacing: ".07em"
  button:
    fontFamily: "IBM Plex Sans, sans-serif"
    fontSize: "13px"
    fontWeight: 600
  measure:
    fontFamily: "IBM Plex Mono, monospace"
    fontSize: "16px"
    fontWeight: 500
  timestamp:
    fontFamily: "IBM Plex Mono, monospace"
    fontSize: "12px"
    fontWeight: 500
  activity-number:
    fontFamily: "IBM Plex Mono, monospace"
    fontSize: "30px"
    fontWeight: 500
    letterSpacing: "-.04em"
rounded:
  stamp: "4px"
  control: "6px"
  panel: "8px"
  sheet: "10px"
  login: "14px"
spacing:
  tight: "8px"
  regular: "16px"
  mobile-gutter: "20px"
  desktop-gutter: "clamp(28px, 5vw, 76px)"
components:
  button-primary:
    backgroundColor: "{colors.action}"
    textColor: "{colors.on-action}"
    typography: "{typography.button}"
    rounded: "{rounded.control}"
    padding: "9px 15px"
    height: "40px"
  button-primary-hover:
    backgroundColor: "{colors.action-hover}"
  button-secondary:
    backgroundColor: "{colors.raised-paper}"
    textColor: "{colors.ink}"
    typography: "{typography.button}"
    rounded: "{rounded.control}"
    padding: "9px 15px"
    height: "40px"
  text-field:
    backgroundColor: "{colors.raised-paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "0 11px"
    height: "40px"
  navigation-active:
    backgroundColor: "{colors.selected-paper}"
    textColor: "{colors.action}"
    rounded: "{rounded.panel}"
    padding: "12px 14px"
  stamp-standard:
    backgroundColor: "{colors.healthy-paper}"
    textColor: "{colors.healthy}"
    rounded: "{rounded.stamp}"
    padding: "5px 8px"
  stamp-failure:
    backgroundColor: "{colors.failure-paper}"
    textColor: "{colors.attention}"
    rounded: "{rounded.stamp}"
    padding: "5px 8px"
  ledger-row:
    textColor: "{colors.ink}"
    padding: "17px 0"
  inline-editor:
    backgroundColor: "{colors.editor-paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.panel}"
    padding: "18px 20px 20px"
  repository-sheet:
    backgroundColor: "{colors.raised-paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sheet}"
    padding: "0 18px 4px"
  operation-preview:
    backgroundColor: "{colors.preview-paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.panel}"
    padding: "15px"
---

# Design System: vpsdash

## Overview

**Creative North Star: "Field Maintenance Ledger"**

Operate mode. The interface feels like a field ledger carried between hosts: warm paper, dark ink, precise rules, small state stamps, and type that makes measurements easy to compare. It serves one operator checking infrastructure from a phone, often during an incident. The current condition and its time of observation lead; everything else follows in quiet, ordered rows.

Hosts, projects, runners, queue items, and sessions share a name, state, context, and observation rhythm. Actions reveal beside the item they affect. Desktop keeps the same dense grammar while giving it a navigation rail and wider measures. Bounded sheets appear where a repository or bulk operation needs several controls; ordinary records remain ruled rows.

**Key Characteristics:**

- Warm, flat surfaces with thin dividers and very little elevation.
- Plain-language condition and explicit freshness before supporting metrics.
- Teal for operation, green for confirmed health, rust for failures or risky unknowns.
- Local IBM Plex Sans for reading and IBM Plex Mono for times, measures, and code.
- Inline previews that show the target and proposed change before submission.

## Colors

The palette is paper and ink with restrained operational color. The frontmatter values are normative; the roles below explain their use.

### Primary

- **Deep Teal** (`action`): active navigation, primary actions, links on hover, and sparklines. The darker `action-hover` is its button hover state; `on-action` is button text.

### Secondary

- **Observed Green** (`healthy`): online dots and standard non-failure stamps, including waiting or silent states. `healthy-paper` is the stamp tint. Written labels distinguish these states.

### Tertiary

- **Incident Rust** (`attention`): failed rows, stale readings, errors, and explicit warnings before an unknown backend value is overwritten. `failure-paper` supplies a pale stamp background.

### Neutral

- **Ledger Paper** (`paper`): main canvas. **Raised Paper** (`raised-paper`): controls, repository sheets, and the login sheet. **Rail Paper** (`rail-paper`): desktop navigation.
- **Selected Paper** (`selected-paper`): active rail item. **Editor Paper** (`editor-paper`): inline project editor. **Preview Paper** (`preview-paper`): operation review area.
- **Ink** (`ink`): titles and values. **Muted Ink** (`muted-ink`): supporting context. **Rule** (`rule`): row and section dividers.

**The State Has Words Rule.** Every dot, tint, and stamp is paired with a readable state or warning; color never carries the diagnosis alone.

## Typography

**Display and body font:** IBM Plex Sans, bundled locally in regular, medium, semibold, and bold weights. **Measurement font:** IBM Plex Mono, bundled locally in medium weight.

The Sans face keeps Portuguese operational copy calm and compact. Mono is reserved for times, metrics, and code; tabular numerals keep adjacent measures aligned. Backend choices remain in Sans controls. Uppercase tracking is a small labeling device, not a treatment for whole paragraphs.

### Hierarchy

- **Brand:** product mark in the desktop rail and login view.
- **Headline:** the condition sentence and page titles, with a fixed phone size for legibility.
- **Section title:** ruled ledger section headings.
- **Body:** condition descriptions and page introductions.
- **Label:** observation status, counts, measure captions, and rail footer.
- **Button:** compact control text.
- **Measure, timestamp, activity number:** host readings, last observation, and quiet aggregate counts.

**The Measurement Type Rule.** Put IBM Plex Mono on data that benefits from alignment or exact reading; leave explanations in IBM Plex Sans.

## Layout

The desktop shell uses a sticky left rail (230px) and a content column capped at 1180px, with a fluid horizontal gutter (`clamp(28px, 5vw, 76px)`). At 1080px and below the rail narrows to icons (74px). At 720px and below the shell becomes one column: a compact top bar (62px), horizontal gutters (20px), and a fixed four-destination bottom navigation (69px plus the safe-area inset). At 390px and below the gutter tightens to 16px. The desktop top bar is 82px high.

The overview starts with observed state, observation time, a condition sentence, and any incident list. Host rows follow; activity counts come after hosts. The 390px healthy-state capture shows the first host row before the bottom navigation. When incidents exist, their list may occupy that space because triage takes precedence. On desktop, host measures sit beside the host name; on a phone they wrap beneath it, with the sparkline below the measures. Project, runner, queue, and session records keep the same ruled-row rhythm across widths.

Sections are separated by rules and generous vertical space, while rows use compact internal padding. Candidate search precedes the candidate list, and long discovery lists are revealed on demand. Repository controls use bounded sheets; bulk controls and preset review stack into one column on a phone. The fixed bottom bar keeps all four destinations reachable while content has extra bottom padding to clear it.

**The Incident First Rule.** Show the condition and its last observation before metrics or controls. A failed subject, its context, and its age stay together in the incident list.

## Elevation & Depth

The ledger is flat by default. Thin rules and subtle changes in paper tone separate records and control regions. The login sheet alone has a soft ambient shadow (`0 14px 35px rgba(33, 49, 43, .08)`); the fixed mobile navigation has a slight upward shadow (`0 -8px 30px rgba(38, 49, 43, .06)`) so it reads above scrolling content. Field focus uses a teal-tinted ring. A status dot's small halo signals observation, not a raised surface.

**The Ruled Surface Rule.** Use borders and tonal changes to organize in-flow content; reserve shadows for a surface that actually floats.

## Shapes

The shape language is practical and gently rounded: tiny stamps, ordinary form controls, slightly softer inline editors and navigation, larger repository sheets, and the single login sheet. Status indicators are circular; list dividers stay straight and full width. Nothing in the ledger needs a pill or decorative frame to imply importance.

## Components

### Buttons and fields

Primary buttons use Deep Teal with white text; secondary buttons sit on Raised Paper with a visible stroke. Both are compact and have a clear hover change. Small buttons keep the same treatment for row actions. Native text fields and selects share height, border, and focus ring; checkboxes use the action color. Disabled controls fade, while nearby copy explains unavailable integrations or missing workflow switches.

### Navigation

Desktop has a full rail, then an icon rail at medium widths. The phone uses a fixed bottom bar with icon and short label for each destination. The active item receives a tinted background and stronger teal text; desktop hover has a quieter tint. Refresh and sign-out remain icon buttons with accessible names in the top bar.

### Ledger rows and state stamps

Rows present the subject first, then host or repository context, an observation age where available, and a short state. Host measures use aligned Mono values and small uppercase captions; missing history says it is still collecting. Project rows open an inline editor for monitoring and health criteria. Healthy, failed, unknown, and silent states have explicit labels. Runners, queued jobs, and sessions keep the same type and divider rhythm without becoming a card grid.

### Repository sheets and operation previews

Each repository sheet groups the agent and CI backend rows. An individual switch reveals current and proposed values before confirmation, then reports the GitHub result in place. Bulk and preset operations use a pale preview panel listing each target and change, with Confirm and Cancel side by side where space allows. If GitHub did not confirm a current value, the preview says so and uses an explicit unknown-value confirmation label; a verified switch shows integration unavailable while its GitHub read is down.

### Login and transient states

The login sheet is the only softly lifted card. Loading uses muted ledger lines and plain status text. Empty sections state what has not been observed or configured; errors and service warnings remain short, visible notes within the reading flow.

## Do's and Don'ts

### Do:

- Do lead the phone overview with condition and observation time, then show incidents before healthy measurements.
- Do give unknown, missing, and stale readings their own text; a dash means unavailable data, not zero.
- Do keep actions and results adjacent to the project or repository they affect.
- Do preview repository, variable, current value, and proposed value before a backend change.
- Do keep rule-separated rows as the default presentation for infrastructure records.

### Don't:

- Don't turn ordinary host, project, runner, or session records into a wall of summary cards.
- Don't let a color dot or wash stand in for a written state or reason.
- Don't treat a GitHub value that was not read as a known default in a confirmation preview.
- Don't use IBM Plex Mono for explanatory paragraphs or uppercase labels for long copy.

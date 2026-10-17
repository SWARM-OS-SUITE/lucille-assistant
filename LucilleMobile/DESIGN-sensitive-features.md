# Lucille Mobile: design for the sensitive features (needs your sign-off)

Written 2026-10-16 while you slept. **Nothing here is built.** You asked for Lucille to read your contacts, email and
Workspace, watch your screen (with an offline model, except at certain times), keep a private memory, and reach Facebook.
Those touch your accounts and your privacy, so this is the plan and the decisions only you can make.

## Rules that apply to all of it
1. **Off until you turn each one on**, one switch per feature, plus one master "pause everything" switch that is always visible.
2. **No passwords, ever.** Sign-in is OAuth; the app only holds a revocable token, kept in the Android Keystore.
3. **Local first.** Reading happens on the phone (or your PC) with the offline model. Nothing leaves the device unless you ask a question that needs the cloud model, and then only the text you can see.
4. **Nothing stored unless you say so** ("remember that..."). Watching produces a short in-memory summary that is thrown away, not a recording.
5. **A visible indicator while anything is watching**, and a log you can read of what she looked at and when.
6. **Never in git, never in swarm-memory.** swarm-memory syncs to GitHub; private data gets its own encrypted store (below).

## 1. Watching the screen
What Android allows (this is the part that decides the design):
- **Screen capture (MediaProjection):** works, but Android makes you approve it each session and shows a system "recording" indicator. No silent always-on capture. A foreground service of type mediaProjection is required.
- **Accessibility service** (reads what's on screen without capturing it): can run all the time, **but Google Play restricts it to tools that help people with disabilities, and an assistant that reads your screen is very likely to be rejected.** Fine for a private APK you install yourself, risky for a product you plan to sell on Play.
- So on the phone, honestly: "**Watch with me**" sessions you start (with the Android indicator), not an invisible always-on watcher. On your PC (SWARM OS / Debian) it can be closer to always-on, through the screen-share portal, with the same indicator and rules.

How "except certain times" works: quiet hours (a schedule you set), an **app blocklist** (banking, password manager, messages, health, anything you add; when one is in front she pauses and the log says "paused"), and the same server-side refusal list Lucille Sight already uses (login, payment, mail, health, tax pages).
Offline model: on-device text recognition (ML Kit, free, runs offline) turns the screen into text, then a small local model (Qwen3 0.6B already runs on your phones in the Companion app) summarizes. Bigger models fit on your 1 TB card, but phone RAM and heat are the limit, not storage.
**Decide:** phone sessions only, PC first, or both? (My recommendation: PC first, phone "Watch with me" second.)

## 2. Email, contacts, Workspace
- **Contacts:** Android's own Contacts permission, asked at runtime, read when you ask ("what's Sam's number"), not copied anywhere.
- **Gmail / Calendar / Drive:** Google sign-in (OAuth) with the smallest read-only scopes. For just you (and up to 100 test users) Google lets this run in "testing" mode (sign-ins there expire after about 7 days and need redoing). **If you publish this on Play to other people, Google requires app verification, and Gmail read access is a restricted scope that can require a paid annual third-party security assessment.** That is a real cost and delay; check the current rules before promising Gmail to buyers.
- Your PC already has a Google Workspace service (`GoogleWorkspaceService`, OAuth refresh credentials in a private folder). Mobile can ask the PC's Lucille for it in PC mode instead of holding its own Google token.
**Decide:** personal use first (cheap, fast) or build for sale from day one (verification needed)?

## 3. Facebook
Meta gives apps **no way to read your personal feed, messages or friends** through an API. The realistic options: (a) you download your data from Facebook ("Download your information") and Lucille reads that file locally; (b) the screen watcher reads it while you scroll. I would do (a) only, and not promise live Facebook.
**Decide:** is an export-file reader enough?

## 4. Private memory drive
- A separate encrypted store, **not** swarm-memory. On the phone: a key in the hardware-backed Keystore (optionally needing your fingerprint) encrypts an app-private database. On the PC: the existing LUCILLE-style `slick` encrypted partition.
- Syncing between devices: encrypted blobs only, phone to PC over Tailscale (same path as memory sync), never GitHub.
- Honest tradeoff: **lose the key (new phone without a backup) and the data is gone.** Backup = an export protected by a passphrase you choose.
**Decide:** phone-only, or phone and PC synced?

## Suggested order once you decide
1. Private memory drive (everything else stores into it).
2. Contacts on demand (smallest, no accounts).
3. Google read-only (personal use).
4. PC screen watching with quiet hours and the blocklist.
5. Phone "Watch with me" sessions.
6. Facebook export reader.

Already built and waiting for a phone test (no sign-off needed): reminders with action buttons, wellness settings, PC mode, sudo approval, the gaming rig panel.

## Decision 2026-10-16 (user): Gmail scope
Lucille only picks up emails about **shipping, development, and the sales projects/sites the user has set up to sell on**.
Nothing else in the inbox is read, summarised or remembered. How to hold that line when it is built:
- Read-only, and only messages matching an allow-list the user edits in the app (sender domains / labels / keywords for
  carriers and orders, dev services such as GitHub and hosting, and each sales site's notifications). Anything not matched is
  never fetched past its headers, and never sent to a cloud model.
- The allow-list is the user's to set; start empty, show what it matches before turning it on.
- Facebook and Workspace stay as designed above (still waiting on the remaining decisions); this decision covers Gmail only.

## Refinement 2026-10-16 (user): Gmail is visible to Lucille, minus the noise
Update to the scope above: Lucille CAN see the email, but not **bills, statements or anything the user isn't interested in**.
So the filter is two-sided: an allow-list (shipping, development, the user's sales sites) AND a block-list that always wins
(bills, statements, receipts-for-utilities, marketing, newsletters). The user edits both lists in the app; anything blocked is
never opened past its headers. Same read-only, off-until-enabled rules as everything else here.

## Idea 2026-10-16 (user): package tracker with a map
Shipping emails already pass the Gmail filter, so tracking numbers can be picked out of them (carrier + number), shown as a
list of parcels with a status bar (ordered -> shipped -> out for delivery -> delivered) and a map pin for the last known
location. Needs, before any code: (a) Gmail connected (user's OAuth step), (b) a tracking data source: either each carrier's
own API (keys per carrier) or one aggregator (e.g. 17TRACK / AfterShip, free tiers, one key), (c) a map: OpenStreetMap tiles
via Leaflet (no key). A first slice that needs none of (a)/(b): add a tracking number by hand and watch it on the map.
Open question for the user: which carriers matter (USPS/UPS/FedEx/DHL/Amazon)?

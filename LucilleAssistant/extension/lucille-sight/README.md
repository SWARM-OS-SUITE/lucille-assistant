# Lucille Sight

Lets Lucille see the web page you are on and help: she tells you what it means, and **highlights what you are looking for**.
On a DuckDuckGo or Google results page she reads the results, outlines the ones worth opening (green star = what she'd open
first, amber = worth a look, faded = ads / spam / off-topic) and says why on a badge. On any other page she answers your
question and highlights the exact passages on the page. On a Steam store page she adds the game's reviews and price and gives
you 👍 / 👎 / ♡ buttons.

## Privacy, in one place
- She reads a page **only when you press her toolbar button or Alt+L**. A search you start from her own panel is read once, when it loads. Nothing is read in the background.
- She never looks at banking, payment, mail, health, tax, login, password or checkout pages, or anything on your own network. This is enforced by the Lucille server, not only here.
- Long digit runs (card / account / SSN shapes) and anything that looks like a key are removed before anything leaves the page.
- Nothing is stored: not the address, not the text, not her answer.
- Tick **"Keep it on this PC"** and the answer comes from the offline models only; otherwise the page text goes to Lucille's cloud model like a chat message would.
- Highlights are only drawn for quotes that really are on the page (the server throws away any quote it cannot find word for word).

## Install
Needs Lucille Assistant 1.11+ running (`http://127.0.0.1:8403`).

**Chrome / Chromium / Brave / Vivaldi:** open `chrome://extensions`, switch on *Developer mode*, *Load unpacked*, choose this `lucille-sight` folder.
**Firefox:** open `about:debugging#/runtime/this-firefox`, *Load Temporary Add-on*, choose `manifest.json` here. (Temporary add-ons are removed when Firefox closes; to keep it permanently use Firefox Developer Edition/ESR with `xpinstall.signatures.required=false`, or sign it at addons.mozilla.org as *unlisted*.)

**Pair once:** on the PC, open Lucille's Brief tab and press *Pair a browser or phone* (or run `lucille-assistant pair`). Press the extension's button, type the 6-digit code. The code lasts 10 minutes and works once.

## SWARM OS
SWARM OS opens Firefox (then Chromium) inside firejail. Load the extension there the same way, and point it at a Lucille Assistant that is running on SWARM OS (or `http://100.82.1.19:8403` over Tailscale when the Debian PC is on). SWARM OS needs its own Lucille Assistant 1.11+ service for this (build-list item G5).

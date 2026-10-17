# C · Lucille Assistant

Project C of the SWARM OS suite. Both versions are built on their own partition,
`/mnt/lucillebuild` (partition C), and live there as two repositories:

- `LucilleAssistant/`: the Go service on :8403 plus the phone-first web app.
- `LucilleMobile/`: the native Android phone app.

This repository is a published snapshot of those two repositories, taken
2026-10-17 (LucilleAssistant d80965e, LucilleMobile 6e5b856). Development
happens on partition C.

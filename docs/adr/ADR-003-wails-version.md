# ADR-003: Wails v2 for the desktop shell

- Status: **Proposed** (decision taken; platform validation still open)
- Date: 2026-10-17
- Milestone: M0

## Context

The desktop shell must be thin: all behaviour lives in a headless Go core so it
can be tested without a window. We need a Go-native shell with a web front end
and a small binary.

## Decision

Use **Wails v2** (stable). The shell binds exactly one Go object (the Facade)
and is built under a `desktop` build tag so that `go build ./...` and the CLI
never need WebView libraries or CGO.

## Alternatives

| Option | Why not (for now) |
| --- | --- |
| Wails v3 | Still in beta; API churn is a risk for a deliverable that must stay buildable. Revisit after v1. |
| Electron / Tauri | Different language or much larger runtime; the point of the project is a Go core. |
| Browser-only UI served by the CLI | Kept anyway as the **second front end** (`httpapi`), which is also what makes end-to-end tests possible. |

## Consequences

- Linux builds need the WebKitGTK development packages; Windows needs WebView2;
  macOS uses WKWebView.
- The UI talks to the core through a `Transport` abstraction with two
  implementations (HTTP and Wails bindings).

## Gate (open)

Build a minimal Wails v2 app on all three platforms and confirm it can reach the
embedded HTTP API. This needs a desktop toolchain and was **not** possible in the
authoring sandbox (no WebKitGTK headers, no display); run it on a developer
machine / CI matrix before accepting.

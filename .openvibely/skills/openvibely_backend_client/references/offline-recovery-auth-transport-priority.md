# Remote Recovery Priority And Configured Login Redaction

Use these checks when changing offline/startup/status presentation or configured credential preflight in the TUI/CLI.

- A state can retain `authRequired` from an earlier reachable request and later receive a transport failure. For an explicitly remote configured server, prioritize correction of `-server` or `OPENVIBELY_SERVER_URL` in the interactive hint and `/status` recovery actions, while retaining `/login` guidance in the same output. Do not suggest starting a local backend for that remote state; retain local setup/start guidance for local endpoints.
- Do not interpolate `Client.BaseURL()` directly into configured-login errors, including non-transport authentication failures. Use `tui.ServerURLDisplay` so userinfo, query values, fragments, and terminal controls cannot leak, while retaining a useful host/path context.
- Cover configured-login failures with real HTTP responses using mixed-case `HTTP` schemes and configured URLs containing userinfo, query secrets, and fragments. Assert every sentinel is absent and the normalized safe endpoint remains present.
- Test diagnostic terminal safety against raw `/status` output before `stripANSI`: assert injected ANSI/control/newline sentinels are absent, but allow renderer-owned ANSI styling. Continue checking the stripped text for secret redaction and readable bounded diagnostics.

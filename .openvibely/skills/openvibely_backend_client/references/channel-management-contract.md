# Channel Management Contract

Use this reference when implementing or auditing the OpenVibely TUI's channel setup, test, disconnect, remove, help, completion, or selector workflows.

## Supported-Set Completeness

- Derive the current channel inventory from backend route registration, channel handlers, and the Channels template before trusting a client-side fixed registry or remembered list. Reconcile every `data-channel-type` integration card and its add/edit controls with client parsing, selectors, help, completions, documentation, dispatch, and tests.
- Do not treat "all supported integrations" coverage as established by iterating only the client's own allowlist. Compare that allowlist against an independently derived backend/template inventory, and add a regression fixture containing every current backend channel type so unknown cards cannot be silently dropped.
- The current backend channel set includes GitHub, Slack, Telegram, Discord, X, and Email. Webhooks and outbound targets are separate channel-adjacent resource families and should not be mistaken for ordinary integration cards.

## X Channel

- Recognize the backend's `data-channel-type="x"` card and expose X in structured list/show output, add/edit selectors, test/remove actions, help, completions, documentation, and all-supported-type tests.
- Configure or edit X with `POST /channels/x/configure`. The form uses `x_consumer_key`, `x_consumer_secret`, `x_access_token`, `x_access_token_secret`, `x_poll_interval_seconds`, and `x_send_responses`.
- All four OAuth 1.0a credentials are required for initial setup. Omitted credential values preserve existing settings on edit. Treat all four as secrets and never expose them in terminal output, command history, diagnostics, or machine-readable list/show results.
- Validate the X poll interval as `15..300` seconds and `x_send_responses` as a boolean before mutation. Test with `POST /channels/x/test`; remove with `POST /channels/x/remove` behind the normal destructive confirmation or headless force gate.
- X authorized mention authors are project-scoped subordinate resources under `/channels/x/authorized-users`. If the requested channel workflow includes authorization management, inventory and implement those add/delete routes separately rather than assuming configuration alone provides a usable inbound channel.

## Test Feedback

- Slack, Telegram, Discord, X, and Email connection-test routes can return HTTP 200 for both success and failure. Parse the returned HTML semantic class: `text-success` means success and `text-error` means failure.
- Treat a malformed or unrecognized HTTP-200 test fragment as failure rather than assuming success from status alone.
- Do not surface raw test-response text in terminal errors because backend/service messages may contain sensitive or unsafe content. Preserve authentication and transport classifications, but return a safe generic channel-test failure for semantic or malformed-result failures.
- GitHub has no channel test route.

## Disconnect And Remove

- Determine safety from the backend service method's actual state changes, not from the action name, route name, or web copy. Inspect handler and service implementation through the settings writes.
- Explicit `disconnect` is supported only for GitHub and Slack and posts to `/channels/github/disconnect` or `/channels/slack/disconnect`, but both routes currently clear credentials or connection state. GitHub disconnect clears the PAT in PAT mode; Slack disconnect clears OAuth/manual bot tokens and connection metadata. Treat these operations as destructive and require interactive confirmation or headless `--force`.
- Destructive `remove` posts to `/channels/<type>/remove` for GitHub, Telegram, Discord, X, and Email and deletes stored integration configuration.
- Preserve the established TUI routing exception for Slack removal: `remove slack` maps to `/channels/slack/disconnect`. Because that backend route clears tokens and metadata, both `remove slack` and explicit `disconnect slack` need the same destructive safety gate even though their user-facing labels differ.
- Resolve and validate the supported canonical channel before confirmation. Invalid or ambiguous references must not prompt or request, and the captured canonical target must be the one mutated after confirmation.

## Configuration Validation

- Share context-free validation between headless parsing and final interactive submission. Reject malformed enums, booleans, ports, URLs, addresses, PEM values, and add-only missing requirements before requests.
- Treat transition-sensitive edit validation separately because it requires authoritative current state. Fetch the channel once, compare an explicitly supplied GitHub auth mode, Slack bot-token mode, or Email provider with the authoritative current value, validate target credentials or hosts only for a genuine change, then reuse that same snapshot for the read-modify-write mutation. Do not fetch again between validation and form merge.
- Explicitly restating the authoritative current mode/provider is not a transition. It must preserve omitted credentials and custom hosts just like any other partial edit; do not require the user to resupply protected fields merely because the enum option appeared in the command.
- For genuine transitions, require newly supplied target-mode fields rather than accepting preserved values from an incompatible mode/provider: GitHub PAT requires `--pat`; GitHub App requires `--app-id`, `--app-slug`, and `--private-key`; Slack OAuth-to-manual requires `--bot-token`; Email preset-to-custom requires `--imap-host` and `--smtp-host`.
- Preserve non-secret authoritative transition metadata through an explicit safe-field allowlist. Do not classify `slack_bot_token_mode` as secret merely because its name contains `token`; conversely, never expose client secrets, app tokens, bot tokens, passwords, PATs, or private keys through editable settings, output, JSON, or diagnostics.
- GitHub PAT mode requires a PAT. GitHub App mode requires app ID, app slug, and private key. Slack requires client ID, client secret, and app token; manual bot-token mode additionally requires a bot token. X requires all four OAuth 1.0a credentials for initial setup and a poll interval from 15 through 300 seconds.
- Supported Email providers are `gmail`, `outlook`, `yahoo`, `fastmail`, `icloud`, and `custom`. Switching to custom Email requires explicit compatible IMAP and SMTP hosts rather than silently reusing a previous provider preset; ports must remain valid when supplied.
- Terminal flows must reproduce meaningful browser input constraints that handlers do not enforce. Validate GitHub API endpoints as usable URLs and Email addresses as actual mailbox addresses before mutation instead of relying on web-only `type="url"` or `type="email"` controls.
- Normalize enum-like values consistently and keep accepted providers/modes synchronized across validation, help, completions, examples, wizard prompts, and serialization.

## Interactive Secret Input

- Do not use a single-line `textinput.Model` for values whose valid representation is multiline, such as a GitHub App PEM private key. Bubbles text input replaces pasted newlines with spaces and corrupts ordinary PEM.
- Use a masked multiline-capable input, or explicitly support and document a validated escaped-newline representation without ever echoing it. Add a realistic generated PEM regression that exercises actual paste/input handling and backend-compatible decoding; one-line placeholder strings do not prove the workflow works.
- Clear secret-bearing model/form state after submission, cancellation, authentication loss, project transition, and cleanup, and assert secrets never enter transcript, history, errors, or JSON.

## Nested Webhook Composition

- When channel-adjacent resources are nested under `/channels`, compose the command registry instead of replacing ordinary integration actions. Preserve the full channel action set while adding nested actions, action usages, selector paths, completion rules, examples, and documentation.
- CLI argument validation may run before the command's runtime dispatcher. The outer channel validator must recognize `webhooks` and delegate the remaining arguments to the webhook validator; runtime-only delegation is insufficient and causes valid nested commands to fail before dispatch.
- Keep nested `/channels webhooks` as the canonical selector command. Deprecated `/webhooks` and `/inbound-webhooks` aliases may enter through separate commands, but picker dispatch and pending-command state should canonicalize to `channels webhooks ...` so all paths share one implementation.
- Retain both structured `ListChannels` behavior and any legacy `GetChannels` compatibility contract. On shared channel/webhook pages, legacy text output must include only ordinary `data-channel-type` cards and exclude webhook controls, cards, pagination state, modal content, hidden data, scripts, and mixed empty-state prose.
- After merging a branch that changed the same command family, run focused tests through the real CLI preflight and interactive selector paths, not only direct dispatcher tests. Cover canonical nested commands, deprecated aliases, option-like webhook names, malformed nested options before requests, help/completion composition, and canonical selector state.

## Regression Matrix

- Cover HTTP-200 success, `text-error`, malformed fragments, authentication, transport, and secret-safe diagnostics for channel tests.
- Assert exact method, route, `project_id`, and destructive gate for test, disconnect, and remove on every supported integration, including GitHub PAT disconnect and Slack's shared disconnect/removal route.
- Cover effective-state conditional validation for both add and edit in interactive and headless modes, including GitHub PAT/App, Slack OAuth/manual, Email preset/custom transitions, malformed URL/email values, realistic multiline PEM input, successful setup for every independently inventoried backend integration, all four X credentials and poll bounds, and every supported Email provider.
- For headless transition regressions, cover unchanged explicit App/manual/custom values, successful genuine transitions with complete target fields, and rejected genuine transitions with incomplete target fields. Assert static malformed inputs make zero requests, transition failures make exactly one scoped authoritative GET and no POST, successful edits reuse that snapshot, and omitted secrets remain preserved without appearing in output.
- Include omitted-reference selector opening, action-specific filtering, selection, cancellation with zero side effects, and headless usage errors.
- Verify help and completion expose every current backend integration, action, provider, advanced Email option, X option, and conditional mode requirement.
- Include a backend-shaped list fixture with all current `data-channel-type` cards and assert each expected ordinary integration appears exactly once while separate webhooks/outbound-target resources remain correctly classified.
- If `main` advances during a long fix pass, recheck ancestry immediately before delivery, integrate the new tip, rerun the inventory against the new backend/template snapshot, and rerun the full post-merge quality gate.

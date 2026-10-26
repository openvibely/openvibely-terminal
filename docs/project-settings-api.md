# Project settings JSON API contract

The terminal client consumes a backend JSON resource at `/api/projects/{id}/settings`. The existing browser routes remain available to the web application; the terminal does not request or submit their HTML.

## Read settings

`GET /api/projects/{id}/settings` returns one JSON object:

```json
{
  "id": "abc123",
  "name": "My Project",
  "description": "Project notes",
  "repository_source": "github",
  "repository_path": "/managed/checkouts/abc123",
  "github_url": "https://github.com/acme/project",
  "default_agent_id": "agent-config-id",
  "default_agent_name": "Builder",
  "max_workers": 4,
  "local_repository_paths_enabled": true,
  "available_agents": [
    { "id": "agent-config-id", "name": "Builder" }
  ]
}
```

`default_agent_name` is the display name for the selected default agent (or may be omitted when `default_agent_id` is empty); when omitted for a selected agent, the terminal resolves the name from `available_agents`. `max_workers` is an integer or `null` when the project inherits the global limit. The backend persists explicit `0` separately from `null`: zero means no project-specific worker cap, while null clears the project override. Both values permit work up to the global worker limit. `available_agents` may be empty. Repository paths and URLs may be empty when they do not apply to the selected source. `local_repository_paths_enabled` is server/environment metadata and must be computed by the backend.

## Update settings

`PUT /api/projects/{id}/settings` accepts the full editable settings object. The project ID is taken from the URL; read-only fields are omitted:

```json
{
  "name": "My Project",
  "description": "Project notes",
  "repository_source": "github",
  "repository_path": "/managed/checkouts/abc123",
  "github_url": "https://github.com/acme/project",
  "default_agent_id": "agent-config-id",
  "max_workers": 4
}
```

A `null` `max_workers` means inherit the global limit; `0` means no project worker limit. Return a JSON error with an appropriate non-2xx status for invalid settings, missing projects, and authorization failures. On success, return any 2xx status. The terminal then performs a separate GET and displays that authoritative persisted resource.

The backend implementation must use the same project settings parsing, validation, authorization, repository replacement/reclone safeguards, local-path restrictions, and persistence path as the existing browser update route. In particular, it must not create a second, weaker settings mutation path. The API response's repository path must reflect the persisted/managed checkout after an update.

## Backend integration delivery

The backend is a separate repository from this terminal task worktree, so its source changes are delivered as the apply-ready patch [`backend-project-settings-api.patch`](backend-project-settings-api.patch). The patch is based on backend commit `d2640272bc0cd5510a8af8848f539e4f71b60196`; apply it from that backend revision with `git apply --3way --unidiff-zero /path/to/backend-project-settings-api.patch`. It includes the handlers, route registration, shared browser/API settings validation and persistence path, actual-router regressions for successful reads and updates, project scoping, missing projects, and invalid updates, plus generated Swagger documentation. Nullable `max_workers` remains documented and tested with distinct persisted values for `0` and `null`. The backend implementation was built and tested in an isolated worktree; the existing backend checkout was left unchanged.

# Automations

Automations are built visually in **Automations** (web) and run in the desktop app whenever files in a sync folder change. The visual editor and the JSON format are the same document: anything you build can be exported, and any valid JSON opens in the editor.

## Concepts

- **Triggers** start a run: `file.created`, `file.modified`, `file.renamed`, `file.deleted`, `folder.changed`, `schedule.interval`, `sync.completed`, `sync.failed`, `file.size_threshold`, `file.extension_matched`.
- **Conditions** branch on YES/NO: name contains/matches, extension, size, folder path, file age, created/modified date, any variable ("metadata matches"), and AND/OR groups that can nest.
- **Actions**: upload, skip, move to a destination folder, rename the destination file, create a folder, tag, start another sync task, send a notification.

A file that passes the sync folder's filters is uploaded unless an automation skips it. Actions never change local files. Modified files replace their previous upload unless an automation chooses a new destination.

When several automations act on one file, lower priority runs first and higher priority wins conflicting decisions; at equal priority, *skip* beats *upload*.

## Document format

```json
{
  "schema": "vaultdock.automation/v1",
  "name": "Medal clips by game and month",
  "enabled": true,
  "priority": 0,
  "nodes": [
    { "id": "trigger", "type": "trigger", "kind": "file.created" },
    { "id": "is-video", "type": "condition", "kind": "extension.in", "params": { "extensions": ["mp4", "mkv"] } },
    { "id": "folder", "type": "action", "kind": "dest.set_folder",
      "params": { "folder": "{{file.parent_name|default:\"Unsorted\"}}/{{file.created|date:\"YYYY-MM\"}}" } },
    { "id": "skip", "type": "action", "kind": "sync.skip", "params": { "reason": "Not a video" } }
  ],
  "edges": [
    { "from": "trigger", "to": "is-video" },
    { "from": "is-video", "to": "folder", "port": "true" },
    { "from": "is-video", "to": "skip", "port": "false" }
  ]
}
```

Rules enforced by the validator (errors are reported with JSON paths such as `nodes[1].params.bytes`):

- `schema` must be `vaultdock.automation/v1`; unknown fields are rejected.
- Node ids are unique (`[A-Za-z0-9_-]{1,64}`); kinds and parameters must exist; required parameters must be present and well-typed.
- Condition edges use `port: "true"` or `"false"`; others use `"next"` (default).
- No cycles, no edges into triggers, at least one trigger and one action.
- Steps must fit the trigger: file actions can't follow sync or schedule triggers; template variables must exist for the trigger.
- Warnings (non-blocking): unreachable steps, unconnected conditions, routes that both upload and skip, multiple destinations on one route, start-task loops.

Sizes may be numbers of bytes or strings like `"50 MB"`. Globs support `*`, `**`, `?` and `[abc]`.

## Variables and templates

Open **Variable reference** in the web app for the full table (name, type, description, example, where it applies) with copy buttons. Common ones: `file.name`, `file.stem`, `file.ext`, `file.size`, `file.size_mb`, `file.rel_path`, `file.rel_dir`, `file.parent_name`, `file.top_folder`, `file.created`, `file.modified`, `file.age_days`, `file.mime`, `sync.config`, `sync.destination`, `sync.files_failed`, `device.name`, `now`.

Filters: `date:"YYYY-MM-DD HH:mm"`, `default:"x"`, `lower`, `upper`, `slug`, `trim`, `truncate:N`, `replace:"a","b"`.

## Medal clips

Medal saves clips in one folder per game. Point a sync folder at Medal's `Clips` folder (the desktop preset fills this in when it finds it), then use the **Medal clips by game and month** template: it uploads only finished videos, skips temporary/partial/tiny files, files them under `<game>/<year-month>`, and tags them by game. Clips directly in the root (no game folder) go to `Unsorted` rather than guessing. Nothing in the sync engine is Medal-specific.

## Generating automations with an AI assistant

1. In the web app open **Variable reference → AI assistant guide** and click **Copy AI guide**. It contains the format, every trigger/condition/action with parameters, the variables, filters and a complete example.
2. Paste it into your assistant with a description, e.g. *"Upload only .mkv files over 100 MB from my OBS folder into /Recordings/<year>, tag them obs, and notify me if a sync fails."*
3. Paste the JSON into **Import JSON**, click **Validate**, fix anything reported, and **Open in editor**. Use **Test** to dry-run sample files before enabling it.

The JSON Schema (draft 2020-12) is available from the same dialog and from `GET /api/v1/automations/reference`.

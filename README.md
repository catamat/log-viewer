# Log-Viewer
[![License](https://img.shields.io/github/license/mashape/apistatus.svg)](https://github.com/catamat/log-viewer/blob/master/LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/catamat/log-viewer)](https://goreportcard.com/report/github.com/catamat/log-viewer)
[![Go Reference](https://pkg.go.dev/badge/github.com/catamat/log-viewer.svg)](https://pkg.go.dev/github.com/catamat/log-viewer)
[![Version](https://img.shields.io/github/tag/catamat/log-viewer.svg?color=blue&label=version)](https://github.com/catamat/log-viewer/releases)

Log-Viewer is a web-based viewer for JSON logs.

It provides a lightweight local UI to inspect line-delimited JSON log files from one or more folders, with quick filtering, dynamic columns, and file selection directly in the browser.

## Features

- Browse multiple configured log folders
- Open a single log file or aggregate all supported files in a folder
- Support arbitrary JSON records without a fixed schema
- Generate columns dynamically from the keys found in the loaded records
- Filter by free text and per-column values
- Show source file and line number for each parsed record
- Surface parsing or file-read warnings without stopping the view

## Configuration

Create `config.json` starting from `config-blank.json` and set the folders you want to browse:

```json
{
    "serverPort": 3333,
    "logFolders": [
        {
            "name": "Local Test",
            "path": "/absolute/path/to/logs"
        },
        {
            "name": "My App",
            "path": "/absolute/path/to/my/app/logs/folder"
        }
    ]
}
```

## Run

```bash
go run .
```

The app starts on `127.0.0.1:<serverPort>` and serves the UI at `/log-viewer`.

## Expected Log Format

Each non-empty line should contain a valid JSON value.

- JSON objects are shown as dynamic columns based on their keys
- Non-object values such as arrays, strings, numbers, or booleans are shown in a `value` column
- Nested objects and arrays are rendered as compact JSON inside the relevant cell
- The viewer adds `source file` and `line number` metadata to every parsed record

Supported file extensions:

- `.log`
- `.json`
- `.jsonl`
- `.ndjson`

Notes:

- Logs are still read line by line, so pretty-printed multi-line JSON records are not supported
- When a timestamp-like field is present, the viewer tries to sort by time using common keys such as `time`, `timestamp`, `ts`, and similar variants

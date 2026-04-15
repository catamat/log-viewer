package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/catamat/config"
	"github.com/goccy/go-json"
	"github.com/gofiber/fiber/v2"
)

type logFolder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type configJSON struct {
	ServerPort int         `json:"serverPort"`
	LogFolders []logFolder `json:"logFolders"`
}

type parsedLogRecord struct {
	Fields      map[string]any
	SearchText  string
	SortTime    time.Time
	HasSortTime bool
	Order       int
}

var (
	supportedLogExtensions = []string{".log", ".json", ".jsonl", ".ndjson"}
	knownTimeKeys          = []string{"time", "timestamp", "ts", "datetime", "date", "createdat", "loggedat", "eventtime"}
	preferredColumnOrder   = map[string]int{
		"time":        0,
		"timestamp":   1,
		"ts":          2,
		"datetime":    3,
		"date":        4,
		"createdat":   5,
		"loggedat":    6,
		"eventtime":   7,
		"level":       20,
		"severity":    21,
		"loglevel":    22,
		"lvl":         23,
		"message":     40,
		"msg":         41,
		"event":       42,
		"description": 43,
		"caller":      60,
		"logger":      61,
		"service":     62,
		"component":   63,
		"sourcefile":  1000,
		"linenumber":  1001,
	}
	keyNormalizer = strings.NewReplacer(" ", "", "_", "", "-", "", "@", "", ".", "", "/", "")
)

func main() {
	cfgJSON := configJSON{}
	if err := config.FromJSON(&cfgJSON, "config.json"); err != nil {
		panic(fmt.Sprintf("failed to read config.json: %v", err))
	}

	logFoldersByName := make(map[string]string, len(cfgJSON.LogFolders))
	for _, lf := range cfgJSON.LogFolders {
		name := strings.TrimSpace(lf.Name)
		path := strings.TrimSpace(lf.Path)
		if name == "" || path == "" {
			continue
		}
		logFoldersByName[name] = path
	}

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ReadTimeout:           10 * time.Second,
		IdleTimeout:           30 * time.Second,
		JSONEncoder:           json.Marshal,
		JSONDecoder:           json.Unmarshal,
	})

	app.Get("/log-viewer", func(c *fiber.Ctx) error {
		return c.Type("html").SendString(indexHTML)
	})

	app.Get("/log-viewer/api/folders", func(c *fiber.Ctx) error {
		folders := make([]fiber.Map, 0, len(cfgJSON.LogFolders))
		for _, lf := range cfgJSON.LogFolders {
			if strings.TrimSpace(lf.Name) == "" || strings.TrimSpace(lf.Path) == "" {
				continue
			}
			folders = append(folders, fiber.Map{
				"name": lf.Name,
			})
		}

		return c.JSON(fiber.Map{
			"ok":      true,
			"folders": folders,
		})
	})

	app.Get("/log-viewer/api/files", func(c *fiber.Ctx) error {
		folderName := strings.TrimSpace(c.Query("folder"))
		if folderName == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"ok":    false,
				"error": "folder not specified",
			})
		}

		dir, ok := logFoldersByName[folderName]
		if !ok {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"ok":    false,
				"error": "invalid folder",
			})
		}

		files, errs := listLogFiles(dir)

		return c.JSON(fiber.Map{
			"ok":     true,
			"files":  files,
			"errors": errs,
		})
	})

	app.Get("/log-viewer/api/logs", func(c *fiber.Ctx) error {
		folderName := strings.TrimSpace(c.Query("folder"))
		if folderName == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"ok":    false,
				"error": "folder not specified",
			})
		}

		logDir, ok := logFoldersByName[folderName]
		if !ok {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"ok":    false,
				"error": "invalid folder",
			})
		}

		file := strings.TrimSpace(c.Query("file"))
		q := strings.TrimSpace(strings.ToLower(c.Query("q")))
		limit := c.QueryInt("limit", 5000)

		var (
			records []parsedLogRecord
			errs    []string
		)

		if file == "" {
			records, errs = readLogsFromDir(logDir)
		} else {
			if filepath.Base(file) != file {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"ok":    false,
					"error": "invalid file name",
				})
			}
			if !isSupportedLogFile(file) {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"ok":    false,
					"error": "unsupported file extension",
				})
			}

			fullPath := filepath.Join(logDir, file)
			records, errs, _ = readLogFile(fullPath, 0)
			sortRecords(records)
		}

		filtered := make([]parsedLogRecord, 0, len(records))
		for _, r := range records {
			if q != "" && !strings.Contains(r.SearchText, q) {
				continue
			}
			filtered = append(filtered, r)
		}

		if limit > 0 && len(filtered) > limit {
			filtered = filtered[:limit]
		}

		responseRecords := make([]map[string]any, 0, len(filtered))
		for _, r := range filtered {
			responseRecords = append(responseRecords, r.Fields)
		}

		return c.JSON(fiber.Map{
			"ok":      true,
			"count":   len(responseRecords),
			"errors":  errs,
			"columns": collectColumns(filtered),
			"records": responseRecords,
		})
	})

	addr := fmt.Sprintf("http://localhost:%d/log-viewer", cfgJSON.ServerPort)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", cfgJSON.ServerPort)

	go func() {
		time.Sleep(400 * time.Millisecond)
		if err := openBrowser(addr); err != nil {
			log.Printf("failed to open the browser automatically: %v", err)
		}
	}()

	log.Println("Log Viewer started at", addr)
	log.Fatal(app.Listen(listenAddr))
}

func isSupportedLogFile(name string) bool {
	lowerName := strings.ToLower(strings.TrimSpace(name))
	for _, ext := range supportedLogExtensions {
		if strings.HasSuffix(lowerName, ext) {
			return true
		}
	}
	return false
}

func listLogFiles(dir string) ([]string, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{fmt.Sprintf("failed to read folder %s: %v", dir, err)}
	}

	files := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isSupportedLogFile(e.Name()) {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	return files, nil
}

func readLogsFromDir(dir string) ([]parsedLogRecord, []string) {
	var (
		all         []parsedLogRecord
		parseErrors []string
		order       int
	)

	files, errs := listLogFiles(dir)
	if errs != nil {
		return nil, errs
	}

	for _, name := range files {
		filePath := filepath.Join(dir, name)
		recs, ferrs, nextOrder := readLogFile(filePath, order)
		all = append(all, recs...)
		parseErrors = append(parseErrors, ferrs...)
		order = nextOrder
	}

	sortRecords(all)

	return all, parseErrors
}

func readLogFile(filePath string, startOrder int) ([]parsedLogRecord, []string, int) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, []string{fmt.Sprintf("failed to open file %s: %v", filePath, err)}, startOrder
	}
	defer f.Close()

	var (
		out   []parsedLogRecord
		errs  []string
		order = startOrder
	)

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			errs = append(errs, fmt.Sprintf("%s:%d invalid JSON: %v", filePath, lineNo, err))
			continue
		}

		fields := toRecordFields(raw)
		fields["source file"] = filepath.Base(filePath)
		fields["line number"] = lineNo

		sortTime, hasSortTime := detectSortTime(fields)
		out = append(out, parsedLogRecord{
			Fields:      fields,
			SearchText:  buildSearchText(fields),
			SortTime:    sortTime,
			HasSortTime: hasSortTime,
			Order:       order,
		})
		order++
	}

	if err := scanner.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("scanner read error %s: %v", filePath, err))
	}

	return out, errs, order
}

func toRecordFields(raw any) map[string]any {
	if obj, ok := raw.(map[string]any); ok {
		return obj
	}
	return map[string]any{
		"value": raw,
	}
}

func sortRecords(records []parsedLogRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		ri := records[i]
		rj := records[j]

		switch {
		case ri.HasSortTime && rj.HasSortTime:
			if ri.SortTime.Equal(rj.SortTime) {
				return ri.Order > rj.Order
			}
			return ri.SortTime.After(rj.SortTime)
		case ri.HasSortTime != rj.HasSortTime:
			return ri.HasSortTime
		default:
			return ri.Order > rj.Order
		}
	})
}

func buildSearchText(fields map[string]any) string {
	b, err := json.Marshal(fields)
	if err != nil {
		return strings.ToLower(fmt.Sprint(fields))
	}
	return strings.ToLower(string(b))
}

func detectSortTime(fields map[string]any) (time.Time, bool) {
	value, ok := findFirstValueByKnownKeys(fields, knownTimeKeys)
	if !ok {
		return time.Time{}, false
	}
	return parseAnyTime(value)
}

func findFirstValueByKnownKeys(node any, knownKeys []string) (any, bool) {
	obj, ok := node.(map[string]any)
	if !ok {
		return nil, false
	}

	for _, wanted := range knownKeys {
		for key, value := range obj {
			if normalizeKeyName(key) == wanted {
				return value, true
			}
		}
	}

	for _, wanted := range knownKeys {
		for _, value := range obj {
			if found, ok := findValueByNormalizedKey(value, wanted); ok {
				return found, true
			}
		}
	}

	return nil, false
}

func findValueByNormalizedKey(node any, wanted string) (any, bool) {
	switch current := node.(type) {
	case map[string]any:
		for key, value := range current {
			if normalizeKeyName(key) == wanted {
				return value, true
			}
		}
		for _, value := range current {
			if found, ok := findValueByNormalizedKey(value, wanted); ok {
				return found, true
			}
		}
	case []any:
		for _, value := range current {
			if found, ok := findValueByNormalizedKey(value, wanted); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func parseAnyTime(v any) (time.Time, bool) {
	switch value := v.(type) {
	case nil:
		return time.Time{}, false
	case string:
		return parseTimeString(strings.TrimSpace(value))
	case float64:
		return parseUnixNumber(value)
	case int:
		return unixTimeFromInt(int64(value)), true
	case int64:
		return unixTimeFromInt(value), true
	case json.Number:
		if i, err := value.Int64(); err == nil {
			return unixTimeFromInt(i), true
		}
		if f, err := value.Float64(); err == nil {
			return parseUnixNumber(f)
		}
	}
	return time.Time{}, false
}

func parseTimeString(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}

	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return unixTimeFromInt(i), true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return parseUnixNumber(f)
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

func parseUnixNumber(v float64) (time.Time, bool) {
	if v == 0 {
		return time.Unix(0, 0).UTC(), true
	}

	if v == float64(int64(v)) {
		return unixTimeFromInt(int64(v)), true
	}

	seconds := int64(v)
	nanos := int64((v - float64(seconds)) * float64(time.Second))
	return time.Unix(seconds, nanos).UTC(), true
}

func unixTimeFromInt(v int64) time.Time {
	abs := v
	if abs < 0 {
		abs = -abs
	}

	switch {
	case abs >= 1_000_000_000_000_000_000:
		return time.Unix(0, v).UTC()
	case abs >= 1_000_000_000_000_000:
		return time.UnixMicro(v).UTC()
	case abs >= 1_000_000_000_000:
		return time.UnixMilli(v).UTC()
	default:
		return time.Unix(v, 0).UTC()
	}
}

func stringifyValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		if value == float64(int64(value)) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		b, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(b)
	}
}

func collectColumns(records []parsedLogRecord) []string {
	seen := make(map[string]struct{})
	columns := make([]string, 0)

	for _, record := range records {
		for key := range record.Fields {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			columns = append(columns, key)
		}
	}

	sort.Slice(columns, func(i, j int) bool {
		pi := columnPriority(columns[i])
		pj := columnPriority(columns[j])
		if pi == pj {
			return strings.ToLower(columns[i]) < strings.ToLower(columns[j])
		}
		return pi < pj
	})

	return columns
}

func columnPriority(name string) int {
	if priority, ok := preferredColumnOrder[normalizeKeyName(name)]; ok {
		return priority
	}
	return 500
}

func normalizeKeyName(name string) string {
	return keyNormalizer.Replace(strings.ToLower(strings.TrimSpace(name)))
}

func openBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	return cmd.Start()
}

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width,initial-scale=1" />
  <title>Log Viewer</title>
  <link rel="preconnect" href="https://fonts.googleapis.com" />
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin />
  <link href="https://fonts.googleapis.com/css2?family=Roboto:wght@400;500;700&display=swap" rel="stylesheet" />
  <style>
    html, body { height: 100%; margin: 0; }
    body {
      font-family: 'Roboto', sans-serif;
      background: #f7f7f9;
      color: #222;
      display: flex;
      flex-direction: column;
      height: 100vh;
      overflow: hidden;
      box-sizing: border-box;
    }

    .appbar {
      display: flex;
      align-items: center;
      min-height: 48px;
      padding: 0 16px;
      background: #d7d8dc;
    }

    .appbar-title {
      margin: 0;
      color: #111;
      font-size: 24px;
      font-weight: 500;
      letter-spacing: 0.04em;
    }

    .page-content {
      flex: 1;
      min-height: 0;
      display: flex;
      flex-direction: column;
      overflow: hidden;
      padding: 16px;
      box-sizing: border-box;
    }

    .toolbar {
      display: flex; gap: 8px; flex-wrap: wrap; align-items: center;
      background: white; padding: 12px; border: 1px solid #ddd; border-radius: 8px;
      margin-bottom: 12px;
    }

    input, select, button {
      padding: 8px 10px; border: 1px solid #ccc; border-radius: 6px; font-size: 14px;
      box-sizing: border-box;
    }

    button { cursor: pointer; background: #fff; }

    .meta { margin: 8px 0 12px; color: #555; font-size: 14px; }

    .table-wrap{
      flex: 1;
      min-height: 0;
      overflow: auto;
      background: white;
      border: 1px solid #ddd;
      border-radius: 8px;
    }

    table {
      border-collapse: collapse;
      width: 100%;
      min-width: 900px;
      table-layout: fixed;
    }

    th, td {
      border-bottom: 1px solid #eee;
      padding: 8px;
      text-align: left;
      vertical-align: top;
      font-size: 13px;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    th {
      position: sticky;
      background: #fafafa;
      z-index: 2;
      border-bottom: 1px solid #ddd;
      white-space: nowrap;
    }

    thead tr.header-row th {
      top: 0;
      z-index: 3;
    }

    thead tr.filter-row th {
      top: 37px;
      z-index: 2;
      background: #fdfdfd;
      padding: 6px;
    }

    .filter-input {
      width: 100%;
      padding: 6px 8px;
      font-size: 12px;
      border: 1px solid #d6d6d6;
      border-radius: 6px;
      background: #fff;
    }

    tbody tr {
      content-visibility: auto;
      contain-intrinsic-size: auto 38px;
    }

    tbody td {
      background: #fff;
    }

    tbody tr:hover > td {
      background-color: #f7faff !important;
    }

    .pill { padding: 2px 8px; border-radius: 999px; font-size: 12px; display: inline-block; }
    .level-info { background: #e8f3ff; color: #1155aa; }
    .level-error { background: #ffe8e8; color: #b00020; }
    .level-warn { background: #fff4d6; color: #946200; }
    .level-debug { background: #eceff3; color: #44505c; }
    .level-other { background: #edf2f7; color: #475569; }
    .small { color: #666; font-size: 12px; }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }

    .errbox {
      margin-top: 12px; background: #fff3cd; color: #6b5500; border: 1px solid #f1d58a;
      padding: 10px; border-radius: 8px; display: none;
    }

    th.resizable {
      position: sticky;
    }

    th.resizable .th-content {
      position: relative;
      padding-right: 10px;
    }

    th.resizable .resizer {
      position: absolute;
      top: 0;
      right: -2px;
      width: 4px;
      height: 100%;
      cursor: col-resize;
      user-select: none;
      z-index: 5;
      background: #dddddd;
    }

    th.resizable .resizer:hover,
    th.resizing .resizer {
      background: rgba(0,0,0,0.08);
    }
  </style>
</head>
<body>
  <header class="appbar">
    <div class="appbar-title">Log Viewer</div>
  </header>

  <main class="page-content">
    <div class="toolbar">
      <label>Folder:
        <select id="folder" style="width:220px">
          <option value="">Select</option>
        </select>
      </label>

      <label>File:
        <select id="file" style="width:220px">
          <option value="">All</option>
        </select>
      </label>

      <label>Search:
        <input id="search" type="text" placeholder="Search any key or value..." style="width:360px" />
      </label>

      <label>Limit:
        <input id="limit" type="number" value="5000" min="1" step="1" style="width:100px" />
      </label>

      <button id="reloadBtn">Reload</button>
      <button id="clearBtn">Clear filters</button>
    </div>

    <div class="meta" id="meta">Ready.</div>

    <div class="table-wrap">
      <table id="logTable">
        <colgroup id="colgroup"></colgroup>
        <thead id="thead"></thead>
        <tbody id="tbody"></tbody>
      </table>
    </div>

    <div class="errbox" id="errbox"></div>
  </main>

  <script>
    const COLUMN_PRIORITY = {
      time: 0,
      timestamp: 1,
      ts: 2,
      datetime: 3,
      date: 4,
      createdat: 5,
      loggedat: 6,
      eventtime: 7,
      level: 20,
      severity: 21,
      loglevel: 22,
      lvl: 23,
      message: 40,
      msg: 41,
      event: 42,
      description: 43,
      caller: 60,
      logger: 61,
      service: 62,
      component: 63,
      sourcefile: 1000,
      linenumber: 1001
    };

    const tbody = document.getElementById('tbody');
    const meta = document.getElementById('meta');
    const errbox = document.getElementById('errbox');
    const folderSelect = document.getElementById('folder');
    const fileSelect = document.getElementById('file');
    const table = document.getElementById('logTable');
    const thead = document.getElementById('thead');
    const colgroup = document.getElementById('colgroup');
    const tableWrap = document.querySelector('.table-wrap');

    let filteredRecords = [];
    let allRecords = [];
    let allColumns = [];
    let columnFilterState = {};
    let columnWidths = {};

    async function loadFolders() {
      const currentValue = folderSelect.value;

      try {
        const res = await fetch('/log-viewer/api/folders');
        const data = await res.json();

        folderSelect.innerHTML = '<option value="">Select</option>';

        if (Array.isArray(data.folders)) {
          for (const f of data.folders) {
            const opt = document.createElement('option');
            opt.value = f.name;
            opt.textContent = f.name;
            folderSelect.appendChild(opt);
          }
        }

        const exists = Array.from(folderSelect.options).some(o => o.value === currentValue);
        folderSelect.value = exists ? currentValue : '';
      } catch (e) {
        folderSelect.innerHTML = '<option value="">Select</option>';
      }
    }

    async function loadFiles() {
      const currentValue = fileSelect.value;
      const folder = folderSelect.value;

      errbox.style.display = 'none';
      errbox.textContent = '';

      fileSelect.innerHTML = '<option value="">All</option>';

      if (!folder) {
        return;
      }

      try {
        const params = new URLSearchParams({ folder });
        const res = await fetch('/log-viewer/api/files?' + params.toString());
        const data = await res.json();

        if (!data.ok) {
          throw new Error(data.error || 'Invalid API response');
        }

        if (Array.isArray(data.files)) {
          for (const f of data.files) {
            const opt = document.createElement('option');
            opt.value = f;
            opt.textContent = f;
            fileSelect.appendChild(opt);
          }
        }

        const exists = Array.from(fileSelect.options).some(o => o.value === currentValue);
        fileSelect.value = exists ? currentValue : '';

        if (Array.isArray(data.errors) && data.errors.length > 0) {
          errbox.style.display = 'block';
          errbox.innerHTML = '<b>Folder read warnings:</b><br>' +
            data.errors.map(e => escapeHtml(e)).join('<br>');
        }
      } catch (e) {
        fileSelect.innerHTML = '<option value="">All</option>';
      }
    }

    async function loadLogs() {
      const folder = document.getElementById('folder').value;
      const file = document.getElementById('file').value;
      const q = document.getElementById('search').value.trim();
      const limit = document.getElementById('limit').value || '5000';

      const params = new URLSearchParams({ limit });
      if (folder) params.set('folder', folder);
      if (file) params.set('file', file);
      if (q) params.set('q', q);

      meta.textContent = 'Loading...';
      errbox.style.display = 'none';
      errbox.textContent = '';
      tbody.innerHTML = '';

      if (!folder) {
        allRecords = [];
        filteredRecords = [];
        allColumns = [];
        rebuildTable();
        renderAllRows();
        meta.textContent = 'Select a folder';
        tableWrap.scrollTop = 0;
        return;
      }

      try {
        const res = await fetch('/log-viewer/api/logs?' + params.toString());
        const data = await res.json();

        if (!data.ok) {
          throw new Error(data.error || 'Invalid API response');
        }

        allRecords = Array.isArray(data.records) ? data.records : [];
        allColumns = Array.isArray(data.columns) ? data.columns : buildColumnsFromRecords(allRecords);
        rebuildTable();
        tableWrap.scrollTop = 0;
        applyColumnFilters();

        if (Array.isArray(data.errors) && data.errors.length > 0) {
          errbox.style.display = 'block';
          errbox.innerHTML = '<b>Parsing/read warnings:</b><br>' +
            data.errors.slice(0, 20).map(e => escapeHtml(e)).join('<br>') +
            (data.errors.length > 20 ? '<br>... ' + (data.errors.length - 20) + ' more warnings' : '');
        }
      } catch (e) {
        meta.textContent = 'Loading error';
        errbox.style.display = 'block';
        errbox.textContent = String(e);
        allRecords = [];
        filteredRecords = [];
        allColumns = [];
        rebuildTable();
        renderAllRows();
        tableWrap.scrollTop = 0;
      }
    }

    function buildColumnsFromRecords(records) {
      const seen = new Set();

      records.forEach(record => {
        if (!record || typeof record !== 'object') {
          return;
        }
        Object.keys(record).forEach(key => seen.add(key));
      });

      return Array.from(seen).sort(compareColumns);
    }

    function compareColumns(a, b) {
      const pa = columnPriority(a);
      const pb = columnPriority(b);
      if (pa === pb) {
        return a.localeCompare(b);
      }
      return pa - pb;
    }

    function columnPriority(name) {
      const normalized = normalizeKeyName(name);
      if (Object.prototype.hasOwnProperty.call(COLUMN_PRIORITY, normalized)) {
        return COLUMN_PRIORITY[normalized];
      }
      return 500;
    }

    function normalizeKeyName(name) {
      return String(name || '')
        .trim()
        .toLowerCase()
        .replace(/[-\s_.@/]+/g, '');
    }

    function rebuildTable() {
      colgroup.innerHTML = '';
      thead.innerHTML = '';

      if (!allColumns.length) {
        return;
      }

      const colParts = [];
      const headerParts = [];
      const filterParts = [];

      for (const col of allColumns) {
        const width = columnWidths[col] || guessColumnWidth(col);
        colParts.push('<col style="width:' + width + 'px">');
        headerParts.push(
          '<th class="resizable" data-col="' + escapeHtml(col) + '"><div class="th-content">' +
          escapeHtml(col) +
          '<div class="resizer"></div></div></th>'
        );

        const currentValue = columnFilterState[col] || '';
        const extraClass = isMonospaceColumn(col, null) ? ' mono' : '';
        filterParts.push(
          '<th><input class="filter-input' + extraClass + '" data-col="' + escapeHtml(col) +
          '" type="text" placeholder="Filter" value="' + escapeHtml(currentValue) + '"></th>'
        );
      }

      colgroup.innerHTML = colParts.join('');
      thead.innerHTML =
        '<tr class="header-row">' + headerParts.join('') + '</tr>' +
        '<tr class="filter-row">' + filterParts.join('') + '</tr>';

      bindColumnFilterInputs();
      initColumnResize();
    }

    function bindColumnFilterInputs() {
      document.querySelectorAll('.filter-input').forEach(input => {
        let localTimer = null;
        input.addEventListener('input', () => {
          columnFilterState[input.dataset.col] = input.value;
          clearTimeout(localTimer);
          localTimer = setTimeout(applyColumnFilters, 120);
        });
      });
    }

    function applyColumnFilters() {
      const activeFilters = {};

      Object.entries(columnFilterState).forEach(([key, value]) => {
        const needle = String(value || '').trim().toLowerCase();
        if (needle) {
          activeFilters[key] = needle;
        }
      });

      filteredRecords = allRecords.filter(record => {
        for (const [key, needle] of Object.entries(activeFilters)) {
          const haystack = formatDisplayValue(key, record ? record[key] : '').toLowerCase();
          if (!haystack.includes(needle)) {
            return false;
          }
        }
        return true;
      });

      meta.textContent = 'Records: ' + filteredRecords.length + ' / ' + allRecords.length;
      renderAllRows();
    }

    function renderAllRows() {
      const colspan = Math.max(allColumns.length, 1);

      if (!filteredRecords.length) {
        tbody.innerHTML = '<tr><td colspan="' + colspan + '" class="small">No records found</td></tr>';
        return;
      }

      const parts = [];
      for (let i = 0; i < filteredRecords.length; i++) {
        const record = filteredRecords[i];
        const row = [];

        for (let j = 0; j < allColumns.length; j++) {
          const key = allColumns[j];
          const rawValue = record ? record[key] : '';
          const displayValue = formatDisplayValue(key, rawValue);
          const cellClasses = [];
          if (isMonospaceColumn(key, rawValue)) {
            cellClasses.push('mono');
          }

          let cellContent = escapeHtml(displayValue);
          if (displayValue && isLevelColumn(key)) {
            cellContent = '<span class="pill ' + levelPillClass(displayValue) + '">' + escapeHtml(displayValue) + '</span>';
          }

          const classAttr = cellClasses.length ? ' class="' + cellClasses.join(' ') + '"' : '';
          row.push(
            '<td' + classAttr + ' title="' + escapeHtml(displayValue) + '">' + cellContent + '</td>'
          );
        }

        parts.push('<tr>' + row.join('') + '</tr>');
      }

      tbody.innerHTML = parts.join('');
    }

    function formatDisplayValue(key, value) {
      if (value === null || value === undefined) {
        return '';
      }

      if (typeof value === 'string') {
        if (isTimeColumn(key)) {
          return formatLocalDateTime(value);
        }
        return value;
      }

      if (typeof value === 'number' || typeof value === 'boolean') {
        return String(value);
      }

      try {
        return JSON.stringify(value);
      } catch (e) {
        return String(value);
      }
    }

    function isTimeColumn(key) {
      const normalized = normalizeKeyName(key);
      return normalized === 'time' ||
        normalized === 'timestamp' ||
        normalized === 'ts' ||
        normalized === 'datetime' ||
        normalized === 'date' ||
        normalized === 'createdat' ||
        normalized === 'loggedat' ||
        normalized === 'eventtime';
    }

    function isLevelColumn(key) {
      const normalized = normalizeKeyName(key);
      return normalized === 'level' ||
        normalized === 'severity' ||
        normalized === 'loglevel' ||
        normalized === 'lvl';
    }

    function isMonospaceColumn(key, value) {
      const normalized = normalizeKeyName(key);

      if (isTimeColumn(key) || normalized === 'sourcefile' || normalized === 'linenumber') {
        return true;
      }
      if (normalized.includes('id') || normalized.includes('uuid') || normalized.includes('trace') || normalized.includes('caller') || normalized.includes('path') || normalized.includes('file')) {
        return true;
      }
      return value && typeof value === 'object';
    }

    function guessColumnWidth(key) {
      const normalized = normalizeKeyName(key);

      if (isTimeColumn(key)) return 190;
      if (isLevelColumn(key)) return 120;
      if (normalized === 'message' || normalized === 'msg' || normalized === 'description' || normalized === 'event' || normalized === 'stack' || normalized === 'error') return 420;
      if (normalized === 'sourcefile') return 170;
      if (normalized === 'linenumber') return 90;
      if (normalized.includes('id') || normalized.includes('caller') || normalized.includes('logger') || normalized.includes('service') || normalized.includes('component')) return 220;
      return 240;
    }

    function levelPillClass(value) {
      const normalized = String(value || '').trim().toLowerCase();

      if (normalized.includes('error') || normalized.includes('fatal') || normalized.includes('panic')) {
        return 'level-error';
      }
      if (normalized.includes('warn')) {
        return 'level-warn';
      }
      if (normalized.includes('debug') || normalized.includes('trace')) {
        return 'level-debug';
      }
      if (normalized.includes('info') || normalized.includes('notice')) {
        return 'level-info';
      }
      return 'level-other';
    }

    function formatLocalDateTime(rawValue) {
      if (!rawValue) return '';

      if (/^-?\d+(\.\d+)?$/.test(rawValue)) {
        const numericValue = Number(rawValue);
        if (!Number.isNaN(numericValue)) {
          return formatNumericTimestamp(numericValue) || rawValue;
        }
      }

      const d = new Date(rawValue);
      if (isNaN(d.getTime())) return rawValue;

      return formatDateObject(d);
    }

    function formatNumericTimestamp(numericValue) {
      if (!Number.isFinite(numericValue)) {
        return '';
      }

      let millis = numericValue;
      const abs = Math.abs(numericValue);

      if (abs >= 1e18) {
        millis = numericValue / 1e6;
      } else if (abs >= 1e15) {
        millis = numericValue / 1e3;
      } else if (abs >= 1e12) {
        millis = numericValue;
      } else {
        millis = numericValue * 1e3;
      }

      const d = new Date(millis);
      if (isNaN(d.getTime())) {
        return '';
      }

      return formatDateObject(d);
    }

    function formatDateObject(d) {
      const dd = String(d.getDate()).padStart(2, '0');
      const mm = String(d.getMonth() + 1).padStart(2, '0');
      const yyyy = d.getFullYear();
      const hh = String(d.getHours()).padStart(2, '0');
      const mi = String(d.getMinutes()).padStart(2, '0');
      const ss = String(d.getSeconds()).padStart(2, '0');

      return dd + '/' + mm + '/' + yyyy + ' ' + hh + ':' + mi + ':' + ss;
    }

    function escapeHtml(s) {
      return String(s)
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#039;');
    }

    function initColumnResize() {
      const headers = table.querySelectorAll('thead tr.header-row th.resizable');
      const cols = colgroup.querySelectorAll('col');

      headers.forEach((th, index) => {
        const resizer = th.querySelector('.resizer');
        if (!resizer || !cols[index]) return;

        let startX = 0;
        let startWidth = 0;
        const colKey = th.dataset.col || '';

        const onMouseMove = (e) => {
          const newWidth = Math.max(80, startWidth + (e.clientX - startX));
          cols[index].style.width = newWidth + 'px';
          columnWidths[colKey] = newWidth;
          th.classList.add('resizing');
        };

        const onMouseUp = () => {
          th.classList.remove('resizing');
          document.removeEventListener('mousemove', onMouseMove);
          document.removeEventListener('mouseup', onMouseUp);
          document.body.style.cursor = '';
          document.body.style.userSelect = '';
        };

        resizer.addEventListener('mousedown', (e) => {
          e.preventDefault();
          e.stopPropagation();
          startX = e.clientX;
          startWidth = th.getBoundingClientRect().width;
          document.body.style.cursor = 'col-resize';
          document.body.style.userSelect = 'none';
          document.addEventListener('mousemove', onMouseMove);
          document.addEventListener('mouseup', onMouseUp);
        });
      });
    }

    document.getElementById('folder').addEventListener('change', async () => {
      document.getElementById('file').value = '';
      columnFilterState = {};
      await loadFiles();
      await loadLogs();
    });

    document.getElementById('reloadBtn').addEventListener('click', async () => {
      await loadFiles();
      await loadLogs();
    });

    document.getElementById('clearBtn').addEventListener('click', async () => {
      document.getElementById('search').value = '';
      document.getElementById('folder').value = '';
      document.getElementById('file').value = '';
      document.getElementById('limit').value = '5000';
      columnFilterState = {};
      await loadFiles();
      await loadLogs();
    });

    let searchTimer = null;
    document.getElementById('search').addEventListener('input', () => {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(loadLogs, 250);
    });

    document.getElementById('limit').addEventListener('change', loadLogs);
    document.getElementById('file').addEventListener('change', loadLogs);

    (async function init() {
      await loadFolders();
      await loadFiles();
      await loadLogs();
    })();
  </script>
</body>
</html>`

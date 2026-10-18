package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/automation"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/pathutil"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const maxUserAutomations = 200

func (s *Server) handleAutomationReference(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"schema_id":   automation.SchemaID,
		"catalog":     automation.Catalog(),
		"variables":   automation.Variables(),
		"filters":     automation.TemplateFilters,
		"json_schema": automation.JSONSchema(),
		"example":     automation.ExampleJSON(),
		"ai_guide":    automation.AIGuide(),
	})
}

func (s *Server) handleAutomationTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.ListAutomations(r.Context(), principal(r).User.ID, ptrBool(true), false)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	user := []map[string]any{}
	for _, row := range rows {
		user = append(user, map[string]any{"id": row.ID, "name": row.Name, "description": row.Description, "definition": json.RawMessage(row.Definition), "updated_at": row.UpdatedAt})
	}
	writeJSON(w, 200, map[string]any{"builtin": automation.BuiltinTemplates(), "user": user})
}

func (s *Server) handleAutomationValidate(w http.ResponseWriter, r *http.Request) {
	raw, err := readLimited(r, 1<<20)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	a, res := automation.Parse(raw)
	writeJSON(w, 200, map[string]any{"valid": res.Valid(), "errors": res.Errors, "warnings": res.Warnings, "definition": a})
}

type automationSave struct {
	Definition json.RawMessage `json:"definition"`
	IsTemplate bool            `json:"is_template"`
}

func (s *Server) saveAutomation(w http.ResponseWriter, r *http.Request, row *store.AutomationRow) {
	var req automationSave
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Definition) == 0 {
		writeErr(w, r, errf(400, "missing_definition", "definition is required"))
		return
	}
	a, res := automation.Parse(req.Definition)
	if a == nil {
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "invalid_automation", "message": "The automation JSON could not be read"}, "validation": res})
		return
	}
	if !res.Valid() && a.Enabled && !req.IsTemplate {
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "invalid_automation", "message": "Fix the validation errors before enabling this automation (you can save it disabled as a draft)"}, "validation": res})
		return
	}
	if strings.TrimSpace(a.Name) == "" {
		a.Name = "Untitled automation"
	}
	def, _ := json.Marshal(a)
	row.Name = truncateStr(a.Name, automation.MaxNameLen)
	row.Description = truncateStr(a.Description, automation.MaxDescLen)
	row.Enabled = a.Enabled && res.Valid() && !req.IsTemplate
	row.Priority = a.Priority
	row.IsTemplate = req.IsTemplate
	row.Valid = res.Valid()
	row.Definition = string(def)
	if err := s.st.SaveAutomation(r.Context(), row); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, row.UserID, "automation.saved", row.Name, map[bool]string{true: "enabled", false: "disabled"}[row.Enabled], false)
	writeJSON(w, 200, map[string]any{"automation": row, "definition": a, "validation": res})
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) handleAutomationList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.ListAutomations(r.Context(), principal(r).User.ID, ptrBool(false), false)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	type item struct {
		store.AutomationRow
		Triggers []string `json:"triggers"`
		Actions  []string `json:"actions"`
	}
	out := []item{}
	for _, row := range rows {
		it := item{AutomationRow: row, Triggers: []string{}, Actions: []string{}}
		var a automation.Automation
		if json.Unmarshal([]byte(row.Definition), &a) == nil {
			for _, n := range a.Nodes {
				switch n.Type {
				case automation.TypeTrigger:
					it.Triggers = append(it.Triggers, n.Kind)
				case automation.TypeAction:
					it.Actions = append(it.Actions, n.Kind)
				}
			}
		}
		out = append(out, it)
	}
	writeJSON(w, 200, map[string]any{"automations": out})
}

func (s *Server) handleAutomationCreate(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	all, err := s.st.ListAutomations(r.Context(), u.ID, nil, false)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if len(all) >= maxUserAutomations {
		writeErr(w, r, errf(400, "limit", "You can have at most %d automations and templates", maxUserAutomations))
		return
	}
	s.saveAutomation(w, r, &store.AutomationRow{UserID: u.ID})
}

func (s *Server) loadAutomation(w http.ResponseWriter, r *http.Request) *store.AutomationRow {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return nil
	}
	row, err := s.st.Automation(r.Context(), principal(r).User.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return nil
	}
	return row
}

func (s *Server) handleAutomationGet(w http.ResponseWriter, r *http.Request) {
	row := s.loadAutomation(w, r)
	if row == nil {
		return
	}
	a, res := automation.Parse([]byte(row.Definition))
	writeJSON(w, 200, map[string]any{"automation": row, "definition": a, "validation": res})
}

func (s *Server) handleAutomationUpdate(w http.ResponseWriter, r *http.Request) {
	row := s.loadAutomation(w, r)
	if row == nil {
		return
	}
	s.saveAutomation(w, r, row)
}

func (s *Server) handleAutomationDelete(w http.ResponseWriter, r *http.Request) {
	row := s.loadAutomation(w, r)
	if row == nil {
		return
	}
	if err := s.st.DeleteAutomation(r.Context(), row.UserID, row.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, row.UserID, "automation.deleted", row.Name, "", false)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleAutomationExport(w http.ResponseWriter, r *http.Request) {
	row := s.loadAutomation(w, r)
	if row == nil {
		return
	}
	var a automation.Automation
	if err := json.Unmarshal([]byte(row.Definition), &a); err != nil {
		writeErr(w, r, err)
		return
	}
	a.Enabled = row.Enabled
	b, _ := json.MarshalIndent(a, "", "  ")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", pathutil.SanitizeName(row.Name, "automation")+".vaultdock.json"))
	_, _ = w.Write(b)
}

func (s *Server) handleAutomationDryRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Definition json.RawMessage    `json:"definition"`
		Events     []automation.Event `json:"events"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Events) == 0 || len(req.Events) > 100 {
		writeErr(w, r, errf(400, "bad_events", "Provide between 1 and 100 sample events"))
		return
	}
	a, res := automation.Parse(req.Definition)
	if a == nil || !res.Valid() {
		writeJSON(w, 200, map[string]any{"validation": res, "results": []any{}})
		return
	}
	results := make([]map[string]any, 0, len(req.Events))
	for i := range req.Events {
		ev := &req.Events[i]
		if ev.Now.IsZero() {
			ev.Now = time.Now()
		}
		if ev.Destination == "" {
			ev.Destination = "/"
		}
		plan := automation.EvaluateAll([]automation.Named{{ID: "dry-run", Automation: a}}, ev)
		results = append(results, map[string]any{"event": ev, "plan": plan})
	}
	writeJSON(w, 200, map[string]any{"validation": res, "results": results})
}

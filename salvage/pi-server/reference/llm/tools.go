package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/giantbeaver9/pi-home-server-page/backend/internal/db"
	"github.com/giantbeaver9/pi-home-server-page/backend/internal/stats"
)

// defaultEventColor matches the web client's default so events created by the
// LLM look consistent with hand-created ones.
const defaultEventColor = "#5b9dff"

// toolSchemas returns the OpenAI `tools` array advertised to the model. Built
// as plain values so it marshals once per request without reflection surprises.
func toolSchemas() []any {
	obj := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	intp := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	fn := func(name, desc string, params map[string]any) any {
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": desc,
				"parameters":  params,
			},
		}
	}

	eventFields := func(required ...string) map[string]any {
		return obj(map[string]any{
			"id":        map[string]any{"type": "integer", "description": "Event id"},
			"title":     str("Event title"),
			"startsAt":  str("Start, ISO 8601 (e.g. 2026-07-09T15:00:00)"),
			"endsAt":    str("End, ISO 8601 (defaults to start)"),
			"allDay":    map[string]any{"type": "boolean"},
			"location":  str("Location"),
			"attendees": str("Comma-separated people, e.g. 'Mum, Ava'"),
			"notes":     str("Free-form notes"),
			"color":     str("Hex colour, e.g. #5b9dff"),
		}, required...)
	}

	return []any{
		fn("get_current_time",
			"Get the current date and time and this machine's timezone. Call this first to resolve relative dates like 'tomorrow' or 'Thursday 3pm' into concrete ISO datetimes before creating or querying events.",
			obj(map[string]any{})),
		fn("list_events",
			"List calendar events overlapping an optional time window. Times are ISO 8601. Omit both bounds to list everything.",
			obj(map[string]any{
				"from": str("Window start, ISO 8601"),
				"to":   str("Window end, ISO 8601"),
			})),
		fn("create_event",
			"Create a calendar event. `startsAt` is required (ISO 8601 wall-clock, e.g. 2026-07-09T15:00:00). For an all-day event set allDay=true and pass startsAt at 00:00:00.",
			eventFields("title", "startsAt")),
		fn("update_event",
			"Update fields of an existing event by id. Only pass the fields to change.",
			eventFields("id")),
		fn("delete_event",
			"Delete a calendar event by id.",
			obj(map[string]any{"id": map[string]any{"type": "integer", "description": "Event id"}}, "id")),
		fn("list_todos",
			"List all todo checklist items with their done state.",
			obj(map[string]any{})),
		fn("add_todo",
			"Add a new todo checklist item.",
			obj(map[string]any{"text": str("The todo text")}, "text")),
		fn("complete_todo",
			"Mark a todo item as done by id.",
			obj(map[string]any{"id": map[string]any{"type": "integer", "description": "Todo id"}}, "id")),
		fn("get_stats",
			"Get current server system stats: CPU %, memory, disk, uptime, and CPU temperature.",
			obj(map[string]any{})),
		fn("list_projects",
			"List the user's tracked projects with headline figures (percent complete, hours, open bugs).",
			obj(map[string]any{})),
		fn("create_project",
			"Create a project to track work (e.g. a Gauntlet AI project).",
			obj(map[string]any{"name": str("Project name"), "description": str("Optional description")}, "name")),
		fn("get_project",
			"Get one project with its full update history.",
			obj(map[string]any{"id": intp("Project id")}, "id")),
		fn("add_project_update",
			"Log a point-in-time update on a project; the figures you pass roll up onto it. Only include fields you're changing.",
			obj(map[string]any{
				"projectId":       intp("Project id"),
				"percentComplete": num("0–100"),
				"estEngHours":     num("Estimated engineer hours remaining"),
				"estAgentHours":   num("Estimated agent hours remaining"),
				"eta":             str("Estimated time to completion, e.g. '2 weeks'"),
				"notes":           str("What changed / notes"),
				"bugsFound":       intp("Bugs found since last update"),
				"bugsFixed":       intp("Bugs fixed since last update"),
			}, "projectId")),
		fn("log_checkin",
			"Record a personal self check-in. Ratings are 1–5 (omit or 0 if not given).",
			obj(map[string]any{
				"mood":   intp("Mood 1–5"),
				"energy": intp("Energy 1–5"),
				"focus":  intp("Focus 1–5"),
				"note":   str("Free-form reflection — wins, blockers, how it went"),
			})),
		fn("list_checkins",
			"List the user's recent personal check-ins.",
			obj(map[string]any{"limit": intp("Max to return (default 30)")})),
		fn("add_goal",
			"Add a personal goal, optionally with a target date.",
			obj(map[string]any{"title": str("Goal title"), "detail": str("Optional detail"), "targetDate": str("Optional target date, ISO 8601 or YYYY-MM-DD")}, "title")),
		fn("list_goals",
			"List the user's goals (open ones first).",
			obj(map[string]any{})),
		fn("complete_goal",
			"Mark a goal as done.",
			obj(map[string]any{"id": intp("Goal id")}, "id")),
	}
}

// dispatch runs one tool call by name against the store, returning a
// JSON-serializable result or an error (which Run encodes as {"error":...}).
// owner scopes the per-user tools (projects/check-ins/goals).
func (a *Agent) dispatch(name, argsJSON string, owner int64) (any, error) {
	switch name {
	case "get_current_time":
		now := time.Now()
		zone, _ := now.Zone()
		return map[string]any{
			"iso":      now.Format(time.RFC3339),
			"local":    now.Format("2006-01-02 15:04:05"),
			"today":    now.Format("2006-01-02"),
			"weekday":  now.Format("Monday"),
			"timezone": zone,
		}, nil

	case "list_events":
		var args struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		return a.store.ListEvents(args.From, args.To)

	case "create_event":
		var args eventArgs
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if args.Title == nil || *args.Title == "" {
			return nil, fmt.Errorf("title is required")
		}
		if args.StartsAt == nil || *args.StartsAt == "" {
			return nil, fmt.Errorf("startsAt is required")
		}
		start, err := normalizeTime(*args.StartsAt)
		if err != nil {
			return nil, err
		}
		end := start
		if args.EndsAt != nil && *args.EndsAt != "" {
			end, err = normalizeTime(*args.EndsAt)
			if err != nil {
				return nil, err
			}
			if end < start { // clamp a nonsensical end to the start
				end = start
			}
		}
		e := db.Event{
			Title:    *args.Title,
			StartsAt: start,
			EndsAt:   end,
			Color:    defaultEventColor,
		}
		applyOptionalEventFields(&e, args)
		return a.store.CreateEvent(e)

	case "update_event":
		var args eventArgs
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if args.ID == nil {
			return nil, fmt.Errorf("id is required")
		}
		e, err := a.store.GetEvent(*args.ID)
		if err != nil {
			return nil, fmt.Errorf("event %d not found", *args.ID)
		}
		oldStart := e.StartsAt
		if args.Title != nil {
			e.Title = *args.Title
		}
		if args.StartsAt != nil && *args.StartsAt != "" {
			if e.StartsAt, err = normalizeTime(*args.StartsAt); err != nil {
				return nil, err
			}
		}
		if args.EndsAt != nil && *args.EndsAt != "" {
			if e.EndsAt, err = normalizeTime(*args.EndsAt); err != nil {
				return nil, err
			}
		}
		applyOptionalEventFields(&e, args)
		// A moved event re-arms its reminder.
		if e.StartsAt != oldStart {
			e.Reminded = false
		}
		return a.store.UpdateEvent(e)

	case "delete_event":
		var args struct {
			ID *int64 `json:"id"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if args.ID == nil {
			return nil, fmt.Errorf("id is required")
		}
		if err := a.store.DeleteEvent(*args.ID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "deleted": *args.ID}, nil

	case "list_todos":
		return a.store.ListTodos()

	case "add_todo":
		var args struct {
			Text string `json:"text"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if args.Text == "" {
			return nil, fmt.Errorf("text is required")
		}
		return a.store.CreateTodo(db.Todo{Text: args.Text})

	case "complete_todo":
		var args struct {
			ID *int64 `json:"id"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if args.ID == nil {
			return nil, fmt.Errorf("id is required")
		}
		// No exported getTodo, so find it via ListTodos.
		todos, err := a.store.ListTodos()
		if err != nil {
			return nil, err
		}
		for _, t := range todos {
			if t.ID == *args.ID {
				t.Done = true
				return a.store.UpdateTodo(t)
			}
		}
		return nil, fmt.Errorf("todo %d not found", *args.ID)

	case "get_stats":
		return stats.Collect()

	case "list_projects":
		return a.store.ListProjects(owner)

	case "create_project":
		var args struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if strings.TrimSpace(args.Name) == "" {
			return nil, fmt.Errorf("name is required")
		}
		return a.store.CreateProject(db.Project{OwnerID: owner, Name: args.Name, Description: args.Description})

	case "get_project":
		var args struct {
			ID *int64 `json:"id"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if args.ID == nil {
			return nil, fmt.Errorf("id is required")
		}
		p, err := a.store.GetProject(owner, *args.ID)
		if err != nil {
			return nil, fmt.Errorf("project %d not found", *args.ID)
		}
		return p, nil

	case "add_project_update":
		var args struct {
			ProjectID       *int64   `json:"projectId"`
			PercentComplete *float64 `json:"percentComplete"`
			EstEngHours     *float64 `json:"estEngHours"`
			EstAgentHours   *float64 `json:"estAgentHours"`
			ETA             string   `json:"eta"`
			Notes           string   `json:"notes"`
			BugsFound       int      `json:"bugsFound"`
			BugsFixed       int      `json:"bugsFixed"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if args.ProjectID == nil {
			return nil, fmt.Errorf("projectId is required")
		}
		u := db.ProjectUpdate{ETA: args.ETA, Notes: args.Notes, BugsFound: args.BugsFound, BugsFixed: args.BugsFixed}
		if args.PercentComplete != nil {
			u.PercentComplete = *args.PercentComplete
		}
		if args.EstEngHours != nil {
			u.EstEngHours = *args.EstEngHours
		}
		if args.EstAgentHours != nil {
			u.EstAgentHours = *args.EstAgentHours
		}
		p, err := a.store.AddProjectUpdate(owner, *args.ProjectID, u, args.PercentComplete != nil, args.EstEngHours != nil, args.EstAgentHours != nil)
		if err != nil {
			return nil, fmt.Errorf("project %d not found", *args.ProjectID)
		}
		return p, nil

	case "log_checkin":
		var args struct {
			Mood   int    `json:"mood"`
			Energy int    `json:"energy"`
			Focus  int    `json:"focus"`
			Note   string `json:"note"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		return a.store.LogCheckin(db.Checkin{OwnerID: owner, Mood: args.Mood, Energy: args.Energy, Focus: args.Focus, Note: args.Note})

	case "list_checkins":
		var args struct {
			Limit int `json:"limit"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		return a.store.ListCheckins(owner, args.Limit)

	case "add_goal":
		var args struct {
			Title      string `json:"title"`
			Detail     string `json:"detail"`
			TargetDate string `json:"targetDate"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if strings.TrimSpace(args.Title) == "" {
			return nil, fmt.Errorf("title is required")
		}
		td := ""
		if strings.TrimSpace(args.TargetDate) != "" {
			t, err := normalizeTime(args.TargetDate)
			if err != nil {
				return nil, err
			}
			td = t
		}
		return a.store.CreateGoal(db.Goal{OwnerID: owner, Title: args.Title, Detail: args.Detail, TargetDate: td})

	case "list_goals":
		return a.store.ListGoals(owner)

	case "complete_goal":
		var args struct {
			ID *int64 `json:"id"`
		}
		if err := unmarshalArgs(argsJSON, &args); err != nil {
			return nil, err
		}
		if args.ID == nil {
			return nil, fmt.Errorf("id is required")
		}
		g, ok, err := a.store.SetGoalDone(owner, *args.ID, true)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("goal %d not found", *args.ID)
		}
		return g, nil

	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// eventArgs holds create/update event arguments. Pointers distinguish "omitted"
// from "empty" so update_event only overwrites the fields the model supplied.
type eventArgs struct {
	ID        *int64  `json:"id"`
	Title     *string `json:"title"`
	StartsAt  *string `json:"startsAt"`
	EndsAt    *string `json:"endsAt"`
	AllDay    *bool   `json:"allDay"`
	Location  *string `json:"location"`
	Attendees *string `json:"attendees"`
	Notes     *string `json:"notes"`
	Color     *string `json:"color"`
}

// applyOptionalEventFields copies the non-time optional fields onto e when the
// model provided them (used by both create and update).
func applyOptionalEventFields(e *db.Event, args eventArgs) {
	if args.AllDay != nil {
		e.AllDay = *args.AllDay
	}
	if args.Location != nil {
		e.Location = *args.Location
	}
	if args.Attendees != nil {
		e.Attendees = *args.Attendees
	}
	if args.Notes != nil {
		e.Notes = *args.Notes
	}
	if args.Color != nil && *args.Color != "" {
		e.Color = *args.Color
	}
}

// unmarshalArgs decodes a tool's JSON argument string, tolerating an empty
// string (no-arg tools) as an empty object.
func unmarshalArgs(argsJSON string, dst any) error {
	if argsJSON == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(argsJSON), dst); err != nil {
		return fmt.Errorf("bad tool arguments: %w", err)
	}
	return nil
}

// normalizeTime accepts an RFC3339 instant OR a bare wall-clock time (in the
// server's local zone) and returns it as a UTC RFC3339 string, so stored event
// times are always UTC and consistent with the web client.
func normalizeTime(s string) (string, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("unparseable time %q; use ISO 8601 like 2026-07-09T15:00:00", s)
}

package bot

import (
	"sort"
	"strings"
)

// Handler serves one user action.
type Handler func(*Ctx) error

// Middleware wraps a handler (chain of responsibility).
type Middleware func(Handler) Handler

// Feature is a self-contained bot section. Adding a feature means writing
// one type and registering it in main; nothing else changes (open/closed).
type Feature interface {
	Register(r *Router)
}

// MenuItem is a main-menu tile contributed by a feature.
type MenuItem struct {
	Order    int
	LabelKey string
	Route    string
}

// Command is a slash command with its help text key.
type Command struct {
	Name    string
	DescKey string
	Handler Handler
}

// Router maps incoming actions to handlers. It is filled once at startup
// and read-only afterwards, so it needs no locking.
type Router struct {
	callbacks map[string]Handler
	prompts   map[string]Handler
	documents map[string]Handler
	commands  map[string]Command
	menu      []MenuItem
}

func NewRouter() *Router {
	return &Router{
		callbacks: map[string]Handler{},
		prompts:   map[string]Handler{},
		documents: map[string]Handler{},
		commands:  map[string]Command{},
	}
}

// Callback registers a button route such as "wol:wake". The route must be
// exactly "<feature>:<action>"; everything after it becomes Ctx.Args.
func (r *Router) Callback(route string, h Handler) {
	if strings.Count(route, ":") != 1 {
		panic("bot: callback route must be feature:action, got " + route)
	}
	if _, dup := r.callbacks[route]; dup {
		panic("bot: duplicate callback route " + route)
	}
	r.callbacks[route] = h
}

// Prompt registers a handler for typed text after Ctx.Ask(key, ...).
func (r *Router) Prompt(key string, h Handler) { r.prompts[key] = h }

// Document registers a handler for an uploaded file after Ctx.Ask(key, ...).
func (r *Router) Document(key string, h Handler) { r.documents[key] = h }

// Command registers "/name".
func (r *Router) Command(name, descKey string, h Handler) {
	r.commands[name] = Command{Name: name, DescKey: descKey, Handler: h}
}

// Menu adds a main-menu tile.
func (r *Router) Menu(order int, labelKey, route string) {
	r.menu = append(r.menu, MenuItem{Order: order, LabelKey: labelKey, Route: route})
}

// MenuItems returns tiles sorted by Order.
func (r *Router) MenuItems() []MenuItem {
	out := append([]MenuItem(nil), r.menu...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// Commands returns registered commands sorted by name.
func (r *Router) Commands() []Command {
	out := make([]Command, 0, len(r.commands))
	for _, c := range r.commands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolveCallback splits "wol:wake:d1:x" into the handler for "wol:wake"
// and args ["d1","x"].
func (r *Router) resolveCallback(data string) (Handler, string, []string) {
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return nil, data, nil
	}
	route := parts[0] + ":" + parts[1]
	return r.callbacks[route], route, parts[2:]
}

package bot

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/lastharbor/owrt-bot/internal/telegram"
)

// Screen is what the single "menu" message shows.
type Screen struct {
	Text     string
	Keyboard *telegram.InlineKeyboard
}

// Ctx carries one user action through a handler and offers the only ways
// a handler talks back: Show/Toast/Ask/Notify. Handlers never call the
// Telegram client directly, so the UI conventions live in one place.
type Ctx struct {
	context.Context
	Chat  int64
	Route string
	Args  []string
	Text  string             // typed text or command arguments
	Doc   *telegram.Document // uploaded file, for Document handlers

	bot     *Bot
	session *Session
	cbID    string
	ackOnce sync.Once
	toast   string

	userMsg    int    // incoming message to delete after handling
	background bool   // created by Go()
	gen        uint64 // session generation the background task belongs to
}

// ErrStale is returned by Show from a background task after the user has
// moved to another screen; the task should Notify instead.
var ErrStale = errors.New("bot: screen changed by user")

// T translates via the bot's translator.
func (c *Ctx) T(key string, kv ...any) string { return c.bot.tr.T(key, kv...) }

// Arg returns the i-th callback argument or "".
func (c *Ctx) Arg(i int) string {
	if i < len(c.Args) {
		return c.Args[i]
	}
	return ""
}

// Session returns per-chat state.
func (c *Ctx) Session() *Session { return c.session }

// Log returns a logger tagged with this action.
func (c *Ctx) Log() *slog.Logger { return c.bot.log.With("chat", c.Chat, "route", c.Route) }

// Show renders s into the chat's menu message, editing it in place when
// there is one, so the chat stays a single evolving screen.
func (c *Ctx) Show(s Screen) error {
	if c.background {
		if c.session.generation() != c.gen {
			return ErrStale
		}
	} else {
		c.session.bump()
	}
	return c.bot.show(c.Context, c.session, c.Chat, s)
}

// Stale reports, for a background task, whether the user left its screen.
func (c *Ctx) Stale() bool { return c.background && c.session.generation() != c.gen }

// Reply sends a fresh menu message at the bottom of the chat and deletes
// the previous one, so there is never more than one menu. Used for slash
// commands, where the old menu may be far up the history.
func (c *Ctx) Reply(s Screen) error {
	c.session.bump()
	old := c.session.MenuMsg()
	id, err := c.bot.msgr.Send(c.Context, c.Chat, s.Text, s.Keyboard)
	if err != nil {
		return err
	}
	c.session.setMenuMsg(id)
	if old != 0 && old != id {
		c.bot.deleteAsync(c.Chat, old)
	}
	return nil
}

// Notify sends a standalone message that does not replace the menu.
func (c *Ctx) Notify(text string) error {
	_, err := c.bot.msgr.Send(c.Context, c.Chat, text, nil)
	return err
}

// Toast sets the popup shown when the button press is acknowledged.
func (c *Ctx) Toast(text string) { c.toast = text }

// Ack answers the callback now. Handlers about to do slow work call it so
// the button spinner stops immediately; a later Toast is then dropped.
func (c *Ctx) Ack() {
	if c.cbID == "" {
		return
	}
	c.ackOnce.Do(func() {
		text := c.toast
		go func() {
			if err := c.bot.msgr.Answer(context.WithoutCancel(c.Context), c.cbID, text); err != nil {
				c.bot.log.Debug("answer callback", "err", err)
			}
		}()
	})
}

// Ask shows s and routes the next typed text (or document) to the Prompt
// or Document handler registered under key; arg is passed back as Args[0].
func (c *Ctx) Ask(key, arg string, s Screen) error {
	c.session.setAsk(&pending{Key: key, Arg: arg})
	return c.Show(s)
}

// Go runs fn in the background, outliving this handler but not the bot.
// fn gets a Ctx bound to the current screen: call Go after Show, and once
// the user navigates away bg.Show returns ErrStale.
func (c *Ctx) Go(fn func(bg *Ctx)) {
	bg := &Ctx{
		Context: c.bot.ctx, Chat: c.Chat, Route: c.Route, Args: c.Args,
		bot: c.bot, session: c.session, background: true, gen: c.session.generation(),
	}
	c.bot.wg.Add(1)
	go func() {
		defer c.bot.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				c.bot.log.Error("background task panic", "route", c.Route, "panic", r)
			}
		}()
		fn(bg)
	}()
}

// SendDocument uploads a file to this chat.
func (c *Ctx) SendDocument(path, caption string) error {
	return c.bot.msgr.SendDocument(c.Context, c.Chat, path, caption)
}

// Download saves an uploaded file.
func (c *Ctx) Download(fileID, dest string) error {
	return c.bot.msgr.Download(c.Context, fileID, dest)
}

// MainMenu renders the menu assembled from registered features.
func (c *Ctx) MainMenu() Screen { return c.bot.mainMenu() }

// CommandArgs returns text after "/cmd".
func (c *Ctx) CommandArgs() string {
	if _, rest, ok := strings.Cut(c.Text, " "); ok {
		return strings.TrimSpace(rest)
	}
	return ""
}

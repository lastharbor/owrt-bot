package bot

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lastharbor/owrt-bot/internal/i18n"
	"github.com/lastharbor/owrt-bot/internal/telegram"
)

// Messenger is the slice of the Telegram client the bot core needs
// (interface segregation: tests use a fake).
type Messenger interface {
	Send(ctx context.Context, chat int64, text string, kb *telegram.InlineKeyboard) (int, error)
	Edit(ctx context.Context, chat int64, msg int, text string, kb *telegram.InlineKeyboard) (int, error)
	Answer(ctx context.Context, callbackID, text string) error
	Delete(ctx context.Context, chat int64, msg int) error
	SendDocument(ctx context.Context, chat int64, path, caption string) error
	Download(ctx context.Context, fileID, dest string) error
}

// UpdateSource yields updates (long-poll in production).
type UpdateSource interface {
	GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]telegram.Update, error)
}

// Options configures a Bot.
type Options struct {
	AllowedChats []int64
	OffsetFile   string // persists the update offset across restarts
	Middleware   []Middleware
	LiveMax      time.Duration // caps LiveOptions.For (tests use a few ms)
	// OnReject sees every chat that is not allowed (from may be nil), so
	// the owner can allow it from LuCI without looking up chat ids.
	OnReject func(chat int64, from *telegram.User)
}

// Bot is the dispatcher: auth, per-chat ordering, routing, UI plumbing.
type Bot struct {
	msgr     Messenger
	router   *Router
	tr       *i18n.Translator
	log      *slog.Logger
	opt      Options
	sessions sessions

	ctx context.Context
	wg  sync.WaitGroup

	queuesMu sync.Mutex
	queues   map[int64]chan telegram.Update
}

// New wires a bot; features must already be registered on router.
func New(msgr Messenger, router *Router, tr *i18n.Translator, log *slog.Logger, opt Options) *Bot {
	b := &Bot{msgr: msgr, router: router, tr: tr, log: log, opt: opt, queues: map[int64]chan telegram.Update{}}
	router.Callback("menu:main", func(c *Ctx) error { return c.Show(c.MainMenu()) })
	router.Command("start", "cmd_start", func(c *Ctx) error { return c.Reply(c.MainMenu()) })
	return b
}

// Translator exposes the shared translator to background services.
func (b *Bot) Translator() *i18n.Translator { return b.tr }

// Broadcast sends text to every allowed chat (alerts, presence events).
func (b *Bot) Broadcast(ctx context.Context, text string) {
	for _, chat := range b.opt.AllowedChats {
		if _, err := b.msgr.Send(ctx, chat, text, nil); err != nil {
			b.log.Warn("broadcast failed", "chat", chat, "err", err)
		}
	}
}

// Run long-polls src until ctx ends, then waits for in-flight work.
func (b *Bot) Run(ctx context.Context, src UpdateSource) {
	b.ctx = ctx
	offset := b.readOffset()
	backoff := time.Second
	b.log.Info("polling started", "offset", offset)
	for ctx.Err() == nil {
		updates, err := src.GetUpdates(ctx, offset, 25*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			b.log.Warn("getUpdates failed", "err", err, "retry_in", backoff)
			sleep(ctx, backoff)
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.enqueue(u)
		}
		if len(updates) > 0 {
			b.writeOffset(offset)
		}
	}
	b.wg.Wait()
}

// Dispatch handles one update synchronously, bypassing the queue. It is
// the entry point for tests and would be the one for webhook delivery.
func (b *Bot) Dispatch(ctx context.Context, u telegram.Update) {
	if b.ctx == nil {
		b.ctx = ctx
	}
	chat := chatOf(u)
	if !slices.Contains(b.opt.AllowedChats, chat) {
		b.reject(u, chat)
		return
	}
	b.handle(u)
}

// Wait blocks until background tasks started with Ctx.Go finish.
func (b *Bot) Wait() { b.wg.Wait() }

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func chatOf(u telegram.Update) int64 {
	switch {
	case u.CallbackQuery != nil && u.CallbackQuery.Message != nil:
		return u.CallbackQuery.Message.Chat.ID
	case u.CallbackQuery != nil:
		return u.CallbackQuery.From.ID
	case u.Message != nil:
		return u.Message.Chat.ID
	}
	return 0
}

// enqueue keeps updates of one chat in order while chats run in parallel.
// Unauthorized chats are rejected here, so queues cannot grow unbounded.
func (b *Bot) enqueue(u telegram.Update) {
	chat := chatOf(u)
	if chat == 0 {
		return
	}
	if !slices.Contains(b.opt.AllowedChats, chat) {
		b.reject(u, chat)
		return
	}
	b.queuesMu.Lock()
	q, ok := b.queues[chat]
	if !ok {
		q = make(chan telegram.Update, 64)
		b.queues[chat] = q
		b.wg.Add(1)
		go b.worker(q)
	}
	b.queuesMu.Unlock()
	select {
	case q <- u:
	default:
		b.log.Warn("chat queue full, dropping update", "chat", chat)
	}
}

func (b *Bot) worker(q chan telegram.Update) {
	defer b.wg.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case u := <-q:
			b.handle(u)
		}
	}
}

func (b *Bot) reject(u telegram.Update, chat int64) {
	var from *telegram.User
	switch {
	case u.Message != nil:
		from = u.Message.From
	case u.CallbackQuery != nil:
		from = &u.CallbackQuery.From
	}
	b.log.Warn("unauthorized", "chat", chat)
	if b.opt.OnReject != nil {
		b.opt.OnReject(chat, from)
	}
	if u.CallbackQuery != nil {
		_ = b.msgr.Answer(b.ctx, u.CallbackQuery.ID, b.tr.T("access_denied"))
		return
	}
	_, _ = b.msgr.Send(b.ctx, chat, b.tr.T("access_denied"), nil)
}

func (b *Bot) handle(u telegram.Update) {
	ctx, cancel := context.WithTimeout(b.ctx, 2*time.Minute)
	defer cancel()
	chat := chatOf(u)
	c := &Ctx{Context: ctx, Chat: chat, bot: b, session: b.sessions.get(chat)}

	var h Handler
	switch {
	case u.CallbackQuery != nil:
		h = b.routeCallback(c, u.CallbackQuery)
	case u.Message != nil:
		h = b.routeMessage(c, u.Message)
	}
	if h == nil {
		return
	}
	for _, mw := range slices.Backward(b.opt.Middleware) {
		h = mw(h)
	}
	if err := h(c); err != nil && !errors.Is(err, context.Canceled) {
		c.Log().Error("handler failed", "err", err)
		if c.toast == "" {
			c.Toast(b.tr.T("error_generic"))
		}
	}
	c.Ack() // no-op if the handler already acked
	// The user's own message (a typed answer, a command) has done its job;
	// removing it keeps the chat down to the single menu screen.
	if c.userMsg != 0 {
		b.deleteAsync(chat, c.userMsg)
	}
}

// deleteAsync removes a message without making the user wait for it.
func (b *Bot) deleteAsync(chat int64, id int) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := b.msgr.Delete(ctx, chat, id); err != nil {
			b.log.Debug("delete message", "chat", chat, "msg", id, "err", err)
		}
	}()
}

func (b *Bot) routeCallback(c *Ctx, cq *telegram.CallbackQuery) Handler {
	c.cbID = cq.ID
	if cq.Message != nil {
		c.session.setMenuMsg(cq.Message.MessageID)
	}
	// Any button press cancels a pending question.
	c.session.setAsk(nil)
	h, route, args := b.router.resolveCallback(cq.Data)
	c.Route, c.Args = route, args
	if h == nil {
		c.Log().Warn("unknown callback", "data", cq.Data)
		// Buttons from an older bot version: just show the main menu.
		return func(c *Ctx) error { return c.Show(c.MainMenu()) }
	}
	return h
}

func (b *Bot) routeMessage(c *Ctx, m *telegram.Message) Handler {
	c.Text = strings.TrimSpace(m.Text)
	c.userMsg = m.MessageID
	if strings.HasPrefix(c.Text, "/") {
		name := strings.TrimPrefix(strings.Fields(c.Text)[0], "/")
		name, _, _ = strings.Cut(name, "@")
		c.Route = "/" + name
		c.session.setAsk(nil)
		if cmd, ok := b.router.commands[name]; ok {
			return cmd.Handler
		}
		return func(c *Ctx) error { return c.Reply(c.MainMenu()) }
	}
	if p := c.session.peekAsk(); p != nil {
		var h Handler
		if m.Document != nil {
			h = b.router.documents[p.Key]
			c.Doc = m.Document
		} else {
			h = b.router.prompts[p.Key]
		}
		if h != nil {
			c.session.takeAsk()
			c.Route, c.Args = "ask:"+p.Key, []string{p.Arg}
			return h
		}
	}
	c.Route = "text"
	return func(c *Ctx) error {
		return c.Reply(Screen{Text: c.T("unknown_command") + "\n\n" + c.MainMenu().Text, Keyboard: c.MainMenu().Keyboard})
	}
}

func (b *Bot) show(ctx context.Context, s *Session, chat int64, sc Screen) error {
	if id := s.MenuMsg(); id != 0 {
		newID, err := b.msgr.Edit(ctx, chat, id, sc.Text, sc.Keyboard)
		if newID != 0 {
			s.setMenuMsg(newID)
		}
		return err
	}
	id, err := b.msgr.Send(ctx, chat, sc.Text, sc.Keyboard)
	if err == nil {
		s.setMenuMsg(id)
	}
	return err
}

func (b *Bot) mainMenu() Screen {
	kb := NewKB()
	var btns []telegram.InlineButton
	for _, it := range b.router.MenuItems() {
		btns = append(btns, Btn(b.tr.T(it.LabelKey), it.Route))
	}
	kb.Grid(2, btns...)
	return Screen{Text: "<b>" + b.tr.T("welcome") + "</b>", Keyboard: kb.Build()}
}

func (b *Bot) readOffset() int64 {
	if b.opt.OffsetFile == "" {
		return 0
	}
	raw, err := os.ReadFile(b.opt.OffsetFile)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return n
}

func (b *Bot) writeOffset(n int64) {
	if b.opt.OffsetFile != "" {
		_ = os.WriteFile(b.opt.OffsetFile, []byte(strconv.FormatInt(n, 10)), 0o600)
	}
}

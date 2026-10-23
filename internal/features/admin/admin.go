// Package admin groups the maintenance screens: backup/restore,
// logs and language. Each is its own Feature type.
package admin

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lastharbor/owrt-bot/internal/bot"
	"github.com/lastharbor/owrt-bot/internal/i18n"
	"github.com/lastharbor/owrt-bot/internal/sys"
)

// ---- backup / restore

type Backup struct {
	Run    sys.Runner
	TmpDir string
}

func (b *Backup) Register(r *bot.Router) {
	r.Menu(70, "btn_backup", "bk:show")
	r.Callback("bk:show", b.show)
	r.Callback("bk:make", b.make)
	r.Callback("bk:restore", b.askFile)
	r.Callback("bk:apply", b.apply)
	r.Document("bk_file", b.onFile)
}

func (b *Backup) screen(c *bot.Ctx, extra string) bot.Screen {
	text := "<b>" + c.T("bk_title") + "</b>\n\n" + c.T("bk_hint")
	if extra != "" {
		text += "\n\n" + extra
	}
	return bot.Screen{Text: text, Keyboard: bot.NewKB().
		Row(bot.Btn(c.T("btn_bk_make"), "bk:make"), bot.Btn(c.T("btn_bk_restore"), "bk:restore")).
		Row(bot.Btn(c.T("btn_back"), "menu:main")).Build()}
}

func (b *Backup) show(c *bot.Ctx) error { return c.Show(b.screen(c, "")) }

func (b *Backup) make(c *bot.Ctx) error {
	c.Ack()
	if err := c.Show(bot.Screen{Text: c.T("bk_running")}); err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	path := filepath.Join(b.TmpDir, "backup-"+stamp+".tar.gz")
	defer os.Remove(path)
	if _, err := b.Run.Run(c, "sysupgrade", "-b", path); err != nil {
		return c.Show(b.screen(c, c.T("bk_failed")+" <code>"+html.EscapeString(err.Error())+"</code>"))
	}
	if err := c.SendDocument(path, c.T("bk_caption", "date", stamp)); err != nil {
		return c.Show(b.screen(c, c.T("bk_failed")))
	}
	// The document landed below the menu; send a fresh menu under it.
	return c.Reply(b.screen(c, c.T("bk_done")))
}

func (b *Backup) askFile(c *bot.Ctx) error {
	return c.Ask("bk_file", "", bot.Screen{Text: c.T("bk_ask_file"),
		Keyboard: bot.NewKB().Row(bot.Btn(c.T("btn_cancel"), "bk:show")).Build()})
}

const sessionRestore = "backup.restore_path"

func (b *Backup) onFile(c *bot.Ctx) error {
	path := filepath.Join(b.TmpDir, "restore.tar.gz")
	if err := c.Download(c.Doc.FileID, path); err != nil {
		return c.Show(b.screen(c, c.T("bk_dl_failed")))
	}
	if out, err := b.Run.Run(c, "tar", "-tzf", path); err != nil || !strings.Contains(string(out), "etc/") {
		os.Remove(path)
		return c.Show(b.screen(c, c.T("bk_not_backup")))
	}
	c.Session().Set(sessionRestore, path)
	return c.Reply(bot.Screen{
		Text: c.T("bk_confirm", "file", html.EscapeString(c.Doc.FileName)),
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_yes"), "bk:apply"), bot.Btn(c.T("btn_cancel"), "bk:show")).Build(),
	})
}

func (b *Backup) apply(c *bot.Ctx) error {
	path, _ := c.Session().Get(sessionRestore).(string)
	c.Session().Set(sessionRestore, nil)
	if path == "" {
		return b.show(c)
	}
	defer os.Remove(path)
	c.Ack()
	if _, err := b.Run.Run(c, "sysupgrade", "-r", path); err != nil {
		return c.Show(b.screen(c, c.T("bk_restore_failed")+" <code>"+html.EscapeString(err.Error())+"</code>"))
	}
	return c.Show(b.screen(c, c.T("bk_restored")))
}

// ---- logs

type Tailer interface{ Tail(n int) string }

type Logs struct{ Log Tailer }

func (l *Logs) Register(r *bot.Router) {
	r.Menu(80, "btn_logs", "log:show")
	r.Callback("log:show", l.show)
}

// show tails the log live, like `tail -f`.
func (l *Logs) show(c *bot.Ctx) error {
	return c.Live(l.screen, bot.LiveOptions{Every: 3 * time.Second})
}

func (l *Logs) screen(c *bot.Ctx) bot.Screen {
	text := l.Log.Tail(20)
	if len(text) > 3300 {
		text = text[len(text)-3300:]
	}
	if text == "" {
		text = "—"
	}
	return bot.Screen{
		Text: "<b>" + c.T("log_title") + "</b>\n<pre>" + html.EscapeString(text) + "</pre>",
		Keyboard: bot.NewKB().
			Row(bot.Btn(c.T("btn_refresh"), "log:show"), bot.Btn(c.T("btn_back"), "menu:main")).Build(),
	}
}

// ---- language

type LangStore interface{ SetLanguage(code string) error }

type Language struct {
	T     *i18n.Translator
	Prefs LangStore
}

func (l *Language) Register(r *bot.Router) {
	r.Menu(90, "btn_language", "lang:show")
	r.Callback("lang:show", l.show)
	r.Callback("lang:set", l.set)
}

func (l *Language) show(c *bot.Ctx) error {
	codes, names := l.T.Languages()
	kb := bot.NewKB()
	for _, code := range codes {
		mark := ""
		if code == l.T.Lang() {
			mark = "✅ "
		}
		kb.Row(bot.Btn(mark+names[code], "lang:set", code))
	}
	kb.Row(bot.Btn(c.T("btn_back"), "menu:main"))
	return c.Show(bot.Screen{Text: "<b>" + c.T("lang_title") + "</b>", Keyboard: kb.Build()})
}

func (l *Language) set(c *bot.Ctx) error {
	if !l.T.Use(c.Arg(0)) {
		return l.show(c)
	}
	if err := l.Prefs.SetLanguage(c.Arg(0)); err != nil {
		return fmt.Errorf("save language: %w", err)
	}
	return c.Show(c.MainMenu())
}

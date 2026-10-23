// Package bottest provides a fake Telegram messenger for feature tests.
package bottest

import (
	"context"
	"sync"

	"github.com/lastharbor/owrt-bot/internal/telegram"
)

// Msg is one recorded outgoing message state.
type Msg struct {
	ID       int
	Text     string
	Keyboard *telegram.InlineKeyboard
}

// Messenger records everything the bot sends.
type Messenger struct {
	mu      sync.Mutex
	nextID  int
	Msgs    map[int]*Msg // current content by message id
	Sent    []int        // ids in send order
	Answers []string     // callback toast texts
	Deleted []int
}

func New() *Messenger { return &Messenger{nextID: 100, Msgs: map[int]*Msg{}} }

func (m *Messenger) Send(_ context.Context, _ int64, text string, kb *telegram.InlineKeyboard) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	m.Msgs[m.nextID] = &Msg{ID: m.nextID, Text: text, Keyboard: kb}
	m.Sent = append(m.Sent, m.nextID)
	return m.nextID, nil
}

func (m *Messenger) Edit(_ context.Context, _ int64, id int, text string, kb *telegram.InlineKeyboard) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Msgs[id] = &Msg{ID: id, Text: text, Keyboard: kb}
	return id, nil
}

func (m *Messenger) Answer(_ context.Context, _ string, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Answers = append(m.Answers, text)
	return nil
}

func (m *Messenger) Delete(_ context.Context, _ int64, id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Msgs, id)
	m.Deleted = append(m.Deleted, id)
	return nil
}

// WasDeleted reports whether message id was deleted.
func (m *Messenger) WasDeleted(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.Deleted {
		if d == id {
			return true
		}
	}
	return false
}

func (m *Messenger) SendDocument(context.Context, int64, string, string) error { return nil }
func (m *Messenger) Download(context.Context, string, string) error            { return nil }

// Get returns a copy of message id.
func (m *Messenger) Get(id int) Msg {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.Msgs[id]; p != nil {
		return *p
	}
	return Msg{}
}

// Last returns the most recently sent message.
func (m *Messenger) Last() Msg {
	m.mu.Lock()
	id := 0
	if len(m.Sent) > 0 {
		id = m.Sent[len(m.Sent)-1]
	}
	m.mu.Unlock()
	return m.Get(id)
}

// Buttons flattens a keyboard into callback data -> label.
func Buttons(kb *telegram.InlineKeyboard) map[string]string {
	out := map[string]string{}
	if kb == nil {
		return out
	}
	for _, row := range kb.Rows {
		for _, b := range row {
			out[b.CallbackData] = b.Text
		}
	}
	return out
}

// Press builds a callback update for message id.
func Press(chat int64, msgID int, data string) telegram.Update {
	return telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID: "cb", Data: data,
		Message: &telegram.Message{MessageID: msgID, Chat: telegram.Chat{ID: chat}},
	}}
}

var userMsgID = 1

// Type builds a text message update with a fresh user message id.
func Type(chat int64, text string) telegram.Update {
	userMsgID++
	return telegram.Update{Message: &telegram.Message{MessageID: userMsgID, Chat: telegram.Chat{ID: chat}, Text: text}}
}

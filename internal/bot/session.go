package bot

import "sync"

// pending is a question the bot asked and is waiting to be answered by
// typed text or an uploaded document.
type pending struct {
	Key string
	Arg string
}

// Session is per-chat UI state. It lives in memory only: after a restart
// the user simply lands on the main menu again.
type Session struct {
	mu      sync.Mutex
	menuMsg int
	gen     uint64 // bumped on every foreground Show; see Ctx.Go
	ask     *pending
	values  map[string]any
}

func (s *Session) MenuMsg() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.menuMsg
}

func (s *Session) bump() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	return s.gen
}

func (s *Session) generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

func (s *Session) setMenuMsg(id int) {
	s.mu.Lock()
	s.menuMsg = id
	s.mu.Unlock()
}

func (s *Session) takeAsk() *pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.ask
	s.ask = nil
	return p
}

func (s *Session) peekAsk() *pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ask
}

func (s *Session) setAsk(p *pending) {
	s.mu.Lock()
	s.ask = p
	s.mu.Unlock()
}

// Get returns a feature-scoped value (e.g. a draft being edited).
func (s *Session) Get(key string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key]
}

// Set stores a feature-scoped value; nil deletes it.
func (s *Session) Set(key string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]any{}
	}
	if v == nil {
		delete(s.values, key)
		return
	}
	s.values[key] = v
}

type sessions struct {
	mu sync.Mutex
	m  map[int64]*Session
}

func (ss *sessions) get(chat int64) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.m == nil {
		ss.m = map[int64]*Session{}
	}
	s, ok := ss.m[chat]
	if !ok {
		s = &Session{}
		ss.m[chat] = s
	}
	return s
}

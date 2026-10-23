// Package telegram is a small Bot API client.
//
// The whole point of rewriting the bot was this file: the Lua version
// spawned sh+curl per request, paying a fresh TLS handshake (~0.43 s
// through the tunnel) every time. Here one http.Transport keeps a single
// HTTP/2 connection open, so long-poll, edits and callback answers are
// multiplexed over it and each call costs one round trip (~0.2 s).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// APIError is a Telegram-side error (ok=false).
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration // set on 429 Too Many Requests
}

// RetryAfter returns how long Telegram asked to wait, or 0.
func RetryAfter(err error) time.Duration {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.RetryAfter
	}
	return 0
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// IsNotModified reports Telegram's "message is not modified" error, which
// is harmless when a screen is refreshed without changes.
func IsNotModified(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && strings.Contains(ae.Description, "not modified")
}

func isMessageGone(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (strings.Contains(ae.Description, "message to edit not found") ||
		strings.Contains(ae.Description, "message can't be edited"))
}

// Options tunes the HTTP layer.
type Options struct {
	APIBase   string // default https://api.telegram.org
	DNSServer string // optional "ip:port" to bypass the system resolver
}

// Client talks to the Bot API. It is safe for concurrent use.
type Client struct {
	base     string
	fileBase string
	http     *http.Client
}

// New creates a client with a keep-alive HTTP/2 transport.
func New(token string, opt Options) *Client {
	if opt.APIBase == "" {
		opt.APIBase = "https://api.telegram.org"
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	if opt.DNSServer != "" {
		server := opt.DNSServer
		dialer.Resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, server)
			},
		}
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			// IPv4 only: the router has no working IPv6 path to Telegram.
			return dialer.DialContext(ctx, "tcp4", addr)
		},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     10 * time.Minute,
		TLSHandshakeTimeout: 10 * time.Second,
		// Detect a connection silently killed by the tunnel instead of
		// waiting for the long-poll timeout.
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: 30 * time.Second,
			PingTimeout:     10 * time.Second,
		},
	}
	return &Client{
		base:     opt.APIBase + "/bot" + token + "/",
		fileBase: opt.APIBase + "/file/bot" + token + "/",
		http:     &http.Client{Transport: tr},
	}
}

func call[T any](ctx context.Context, c *Client, method string, params any) (T, error) {
	var zero T
	body, err := json.Marshal(params)
	if err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	return do[T](c, method, req)
}

func do[T any](c *Client, method string, req *http.Request) (T, error) {
	var zero T
	resp, err := c.http.Do(req)
	if err != nil {
		return zero, scrub(err)
	}
	defer resp.Body.Close()
	var r apiResponse[T]
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return zero, fmt.Errorf("telegram %s: decode: %w (http %d)", method, err, resp.StatusCode)
	}
	if !r.OK {
		return zero, &APIError{Method: method, Code: r.ErrorCode, Description: r.Description,
			RetryAfter: time.Duration(r.Parameters.RetryAfter) * time.Second}
	}
	return r.Result, nil
}

// scrub removes the bot token from transport errors (they embed the URL).
func scrub(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	msg := err.Error()
	if i := strings.Index(msg, "/bot"); i >= 0 {
		if j := strings.Index(msg[i+4:], "/"); j >= 0 {
			msg = msg[:i] + "/bot<token>" + msg[i+4+j:]
		}
	}
	return errors.New(msg)
}

// GetUpdates long-polls for new updates.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	return call[[]Update](ctx, c, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout.Seconds()),
		"allowed_updates": []string{"message", "callback_query"},
	})
}

// GetMe returns the bot's own account; it is the cheapest token check.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	return call[User](ctx, c, "getMe", map[string]any{})
}

// Send posts a new HTML message and returns its id.
func (c *Client) Send(ctx context.Context, chatID int64, text string, kb *InlineKeyboard) (int, error) {
	m, err := call[Message](ctx, c, "sendMessage", withKeyboard(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}, kb))
	return m.MessageID, err
}

// Edit rewrites a message in place. If the message is gone it sends a new
// one and returns the new id; otherwise it returns msgID.
func (c *Client) Edit(ctx context.Context, chatID int64, msgID int, text string, kb *InlineKeyboard) (int, error) {
	_, err := call[json.RawMessage](ctx, c, "editMessageText", withKeyboard(map[string]any{
		"chat_id":                  chatID,
		"message_id":               msgID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}, kb))
	switch {
	case err == nil, IsNotModified(err):
		return msgID, nil
	case isMessageGone(err):
		return c.Send(ctx, chatID, text, kb)
	default:
		return msgID, err
	}
}

// Delete removes a message; errors are not interesting to callers.
func (c *Client) Delete(ctx context.Context, chatID int64, msgID int) error {
	_, err := call[bool](ctx, c, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": msgID})
	return err
}

// Answer acknowledges a callback query, optionally with a toast.
func (c *Client) Answer(ctx context.Context, callbackID, text string) error {
	p := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		p["text"] = text
	}
	_, err := call[bool](ctx, c, "answerCallbackQuery", p)
	return err
}

// SetCommands publishes the slash-command list shown in the client.
func (c *Client) SetCommands(ctx context.Context, cmds []BotCommand) error {
	_, err := call[bool](ctx, c, "setMyCommands", map[string]any{"commands": cmds})
	return err
}

// SendDocument uploads a local file.
func (c *Client) SendDocument(ctx context.Context, chatID int64, path, caption string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", fmt.Sprint(chatID))
	_ = w.WriteField("caption", caption)
	part, err := w.CreateFormFile("document", filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"sendDocument", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	_, err = do[json.RawMessage](c, "sendDocument", req)
	return err
}

// Download fetches an uploaded file into dest (max 20 MB per Bot API).
func (c *Client) Download(ctx context.Context, fileID, dest string) error {
	info, err := call[file](ctx, c, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fileBase+info.FilePath, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return scrub(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: http %d", resp.StatusCode)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(resp.Body, 25<<20)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// withKeyboard adds reply_markup only when present: Telegram rejects null.
func withKeyboard(p map[string]any, kb *InlineKeyboard) map[string]any {
	if kb != nil {
		p["reply_markup"] = kb
	}
	return p
}

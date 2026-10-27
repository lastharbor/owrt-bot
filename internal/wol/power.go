package wol

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// Action is a power command.
type Action string

const (
	ActShutdown Action = "off"
	ActReboot   Action = "reboot"
	ActSleep    Action = "sleep"
)

// PowerController turns devices off. SSH is the only implementation
// today; an agent-based one would satisfy the same interface.
type PowerController interface {
	Do(ctx context.Context, d Device, ip string, a Action) error
}

// ErrHostKeyChanged means the device presented a different SSH host key
// than the first time: possibly a reinstall, possibly an impostor.
var ErrHostKeyChanged = errors.New("wol: SSH host key changed")

// ErrNoPower means the device has no SSH settings.
var ErrNoPower = errors.New("wol: power control not configured")

// ErrDenied means Windows refused the command (exit status 5). For a key
// login that is almost always a missing SeRemoteShutdownPrivilege.
var ErrDenied = errors.New("wol: access denied on the device")

// commands per OS. Windows: sleep goes through .NET so it suspends even
// when hibernation is on (rundll32 SetSuspendState would hibernate).
var commands = map[OS]map[Action]string{
	Windows: {
		ActShutdown: `shutdown /s /f /t 5 /c "owrt-bot"`,
		ActReboot:   `shutdown /r /f /t 5 /c "owrt-bot"`,
		ActSleep:    `powershell -NoProfile -Command "Add-Type -AssemblyName System.Windows.Forms; [void][System.Windows.Forms.Application]::SetSuspendState('Suspend', $false, $false)"`,
	},
	Linux: {
		ActShutdown: "systemctl poweroff",
		ActReboot:   "systemctl reboot",
		ActSleep:    "systemctl suspend",
	},
}

// SSHPower runs power commands over SSH with the bot's own ed25519 key.
// Host keys are trusted on first use and pinned in KnownHosts.
type SSHPower struct {
	signer     ssh.Signer
	knownHosts string
	mu         sync.Mutex
}

// NewSSHPower loads keyPath, generating a key on first run.
func NewSSHPower(keyPath, knownHosts string) (*SSHPower, error) {
	raw, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = generateKey(keyPath)
	}
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyPath, err)
	}
	return &SSHPower{signer: signer, knownHosts: knownHosts}, nil
}

func generateKey(path string) ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "owrt-bot")
	if err != nil {
		return nil, err
	}
	raw := pem.EncodeToMemory(block)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, err
	}
	return raw, nil
}

// AuthorizedKey is the line to put into the device's authorized_keys.
func (s *SSHPower) AuthorizedKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.signer.PublicKey()))) + " owrt-bot"
}

// command returns what to send for a: one word for script-configured PCs
// (their key only runs the owrt-bot wrapper), a full command otherwise.
func command(p *Power, a Action) (string, error) {
	if p.Restricted {
		return string(a), nil
	}
	cmd, ok := commands[p.OS][a]
	if !ok {
		return "", fmt.Errorf("wol: no %s command for OS %q", a, p.OS)
	}
	return cmd, nil
}

func (s *SSHPower) Do(ctx context.Context, d Device, ip string, a Action) error {
	if d.Power == nil || d.Power.User == "" {
		return ErrNoPower
	}
	cmd, err := command(d.Power, a)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, d, ip, cmd)
	// Shutting down or suspending often cuts the session before the exit
	// status arrives; that is success, not failure.
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) || errors.Is(err, io.EOF) {
		return nil
	}
	var exit *ssh.ExitError
	if d.Power.OS == Windows && errors.As(err, &exit) && exit.ExitStatus() == 5 {
		return fmt.Errorf("%w (%v)", ErrDenied, err)
	}
	return err
}

// Check logs in and runs a harmless command, proving the key, user and
// host are all right before the user needs the off button.
func (s *SSHPower) Check(ctx context.Context, d Device, ip string) error {
	if d.Power == nil || d.Power.User == "" {
		return ErrNoPower
	}
	cmd := "echo ok"
	if d.Power.Restricted {
		cmd = "check"
	}
	out, err := s.run(ctx, d, ip, cmd)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "ok") {
		return fmt.Errorf("wol: unexpected answer %q", strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *SSHPower) run(ctx context.Context, d Device, ip, cmd string) ([]byte, error) {
	addr := net.JoinHostPort(ip, strconv.Itoa(d.Power.PortOrDefault()))
	cfg := &ssh.ClientConfig{
		User:            d.Power.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(s.signer)},
		HostKeyCallback: s.tofu,
		Timeout:         5 * time.Second,
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", addr)
	if err != nil {
		return nil, err
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	client := ssh.NewClient(sc, chans, reqs)
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var stderr strings.Builder
	sess.Stderr = &stderr
	out, err := sess.Output(cmd)
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		if msg := lastLine(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return out, err
}

// lastLine is the last non-empty line of a command's stderr, which is
// where both shutdown.exe and systemctl put the reason. Windows writes it
// in the OEM code page, which on Russian systems is CP866.
func lastLine(s string) string {
	if !utf8.ValidString(s) {
		s = fromCP866(s)
	}
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(l); len(r) > 200 {
		l = string(r[:200]) + "…"
	}
	return l
}

// fromCP866 decodes the Cyrillic part of CP866; box drawing and the rest
// of the upper half are not expected in an error message and become "?".
func fromCP866(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x80:
			b.WriteByte(c)
		case c <= 0xAF: // А..я up to п
			b.WriteRune(rune(c-0x80) + 'А')
		case c >= 0xE0 && c <= 0xEF: // р..я
			b.WriteRune(rune(c-0xE0) + 'р')
		case c == 0xF0:
			b.WriteRune('Ё')
		case c == 0xF1:
			b.WriteRune('ё')
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// tofu pins the host key on first contact and rejects changes later.
func (s *SSHPower) tofu(host string, _ net.Addr, key ssh.PublicKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if f, err := os.Open(s.knownHosts); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			h, k, ok := strings.Cut(sc.Text(), " ")
			if ok && h == host {
				if k == line {
					return nil
				}
				return ErrHostKeyChanged
			}
		}
	}
	f, err := os.OpenFile(s.knownHosts, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", host, line)
	return err
}

// ForgetHost removes a pinned host key (after a deliberate reinstall).
func (s *SSHPower) ForgetHost(ip string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.knownHosts)
	if err != nil {
		return nil
	}
	host := net.JoinHostPort(ip, strconv.Itoa(port))
	var keep []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l != "" && !strings.HasPrefix(l, host+" ") {
			keep = append(keep, l)
		}
	}
	out := strings.Join(keep, "\n")
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(s.knownHosts, []byte(out), 0o600)
}

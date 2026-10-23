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

// commands per OS. Windows: sleep goes through .NET so it suspends even
// when hibernation is on (rundll32 SetSuspendState would hibernate).
var commands = map[OS]map[Action]string{
	Windows: {
		ActShutdown: `shutdown /s /t 5 /c "owrt-bot"`,
		ActReboot:   `shutdown /r /t 5 /c "owrt-bot"`,
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

func (s *SSHPower) Do(ctx context.Context, d Device, ip string, a Action) error {
	if d.Power == nil || d.Power.User == "" {
		return ErrNoPower
	}
	cmd, ok := commands[d.Power.OS][a]
	if !ok {
		return fmt.Errorf("wol: no %s command for OS %q", a, d.Power.OS)
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(d.Power.PortOrDefault()))
	cfg := &ssh.ClientConfig{
		User:            d.Power.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(s.signer)},
		HostKeyCallback: s.tofu,
		Timeout:         5 * time.Second,
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", addr)
	if err != nil {
		return err
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return err
	}
	client := ssh.NewClient(sc, chans, reqs)
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	err = sess.Run(cmd)
	// Shutting down or suspending often cuts the session before the exit
	// status arrives; that is success, not failure.
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
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

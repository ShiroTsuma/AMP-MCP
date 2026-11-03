//go:build linux

package secretstore

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	ssName       = "org.freedesktop.secrets"
	ssPath       = dbus.ObjectPath("/org/freedesktop/secrets")
	ssService    = "org.freedesktop.Secret.Service"
	ssCollection = "org.freedesktop.Secret.Collection"
	ssItem       = "org.freedesktop.Secret.Item"
	ssPrompt     = "org.freedesktop.Secret.Prompt"
	ssDefault    = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
)

// promptTimeout bounds waiting for a keyring unlock dialog nobody may see.
var promptTimeout = 2 * time.Minute

func platformStore(app string, opts Options, backend string) (Store, error) {
	switch backend {
	case BackendDPAPI:
		return nil, errors.New("this secret was sealed with Windows DPAPI and cannot be opened on Linux; re-enter it in the tool's configuration panel")
	case BackendSecretService:
		return newSecretService(app)
	}
	s, err := newSecretService(app)
	if err != nil {
		return nil, nil // headless: fall back to the vault
	}
	return s, nil
}

type ssSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type secretService struct {
	app  string
	conn *dbus.Conn
	svc  dbus.BusObject
}

// newSecretService connects without autolaunching a bus (no dbus-launch on
// headless hosts) and requires a running/activatable service with a default
// collection.
func newSecretService(app string) (*secretService, error) {
	ssMu.Lock()
	defer ssMu.Unlock()
	if s := ssConns[app]; s != nil && s.conn.Connected() {
		return s, nil
	}
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return nil, err
	}
	if err = conn.Auth(nil); err == nil {
		err = conn.Hello()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	s := &secretService{app: app, conn: conn, svc: conn.Object(ssName, ssPath)}
	var alias dbus.ObjectPath
	if err = s.svc.Call(ssService+".ReadAlias", 0, "default").Store(&alias); err != nil || alias == "/" {
		conn.Close()
		if err == nil {
			err = errors.New("secret service has no default collection")
		}
		return nil, err
	}
	ssConns[app] = s
	return s, nil
}

// One private bus connection per app and process.
var (
	ssMu    sync.Mutex
	ssConns = map[string]*secretService{}
)

func (s *secretService) Kind() string { return BackendSecretService }

func (s *secretService) session() (dbus.ObjectPath, func(), error) {
	var out dbus.Variant
	var path dbus.ObjectPath
	if err := s.svc.Call(ssService+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&out, &path); err != nil {
		return "", nil, fmt.Errorf("secret service: %w", err)
	}
	return path, func() { s.conn.Object(ssName, path).Call("org.freedesktop.Secret.Session.Close", 0) }, nil
}

func (s *secretService) Seal(plain []byte, label string) (string, error) {
	if err := s.unlock([]dbus.ObjectPath{ssDefault}); err != nil {
		return "", err
	}
	sess, done, err := s.session()
	if err != nil {
		return "", err
	}
	defer done()
	id := newID()
	props := map[string]dbus.Variant{
		ssItem + ".Label":      dbus.MakeVariant(s.app + ": " + label),
		ssItem + ".Attributes": dbus.MakeVariant(map[string]string{"app": s.app, "label": label, "id": id}),
	}
	secret := ssSecret{Session: sess, Parameters: []byte{}, Value: plain, ContentType: "text/plain"}
	var item, prompt dbus.ObjectPath
	if err = s.conn.Object(ssName, ssDefault).Call(ssCollection+".CreateItem", 0, props, secret, true).Store(&item, &prompt); err != nil {
		return "", fmt.Errorf("secret service: %w", err)
	}
	if _, err = s.prompt(prompt); err != nil {
		return "", err
	}
	return BackendSecretService + ":" + id, nil
}

func (s *secretService) find(sealed string) (dbus.ObjectPath, error) {
	id, ok := strings.CutPrefix(sealed, BackendSecretService+":")
	if !ok {
		return "", ErrUnsupported
	}
	var unlocked, locked []dbus.ObjectPath
	if err := s.svc.Call(ssService+".SearchItems", 0, map[string]string{"app": s.app, "id": id}).Store(&unlocked, &locked); err != nil {
		return "", fmt.Errorf("secret service: %w", err)
	}
	if len(locked) > 0 {
		if err := s.unlock(locked); err != nil {
			return "", err
		}
		unlocked = append(unlocked, locked...)
	}
	if len(unlocked) == 0 {
		return "", ErrNotFound
	}
	return unlocked[0], nil
}

func (s *secretService) Open(sealed string) ([]byte, error) {
	item, err := s.find(sealed)
	if err != nil {
		return nil, err
	}
	sess, done, err := s.session()
	if err != nil {
		return nil, err
	}
	defer done()
	var secret ssSecret
	if err = s.conn.Object(ssName, item).Call(ssItem+".GetSecret", 0, sess).Store(&secret); err != nil {
		return nil, fmt.Errorf("secret service: %w", err)
	}
	return secret.Value, nil
}

func (s *secretService) Delete(sealed string) error {
	item, err := s.find(sealed)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var prompt dbus.ObjectPath
	if err = s.conn.Object(ssName, item).Call(ssItem+".Delete", 0).Store(&prompt); err != nil {
		return fmt.Errorf("secret service: %w", err)
	}
	_, err = s.prompt(prompt)
	return err
}

func (s *secretService) unlock(objs []dbus.ObjectPath) error {
	var done []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := s.svc.Call(ssService+".Unlock", 0, objs).Store(&done, &prompt); err != nil {
		return fmt.Errorf("secret service unlock: %w", err)
	}
	dismissed, err := s.prompt(prompt)
	if err == nil && dismissed {
		err = errors.New("secret service unlock was dismissed")
	}
	return err
}

// prompt runs a Secret Service prompt (e.g. keyring password dialog) and
// reports whether the user dismissed it.
func (s *secretService) prompt(p dbus.ObjectPath) (bool, error) {
	if p == "/" || p == "" {
		return false, nil
	}
	opts := []dbus.MatchOption{dbus.WithMatchObjectPath(p), dbus.WithMatchInterface(ssPrompt)}
	if err := s.conn.AddMatchSignal(opts...); err != nil {
		return false, err
	}
	defer s.conn.RemoveMatchSignal(opts...)
	ch := make(chan *dbus.Signal, 4)
	s.conn.Signal(ch)
	defer s.conn.RemoveSignal(ch)
	if err := s.conn.Object(ssName, p).Call(ssPrompt+".Prompt", 0, "").Err; err != nil {
		return false, err
	}
	timeout := time.After(promptTimeout)
	for {
		select {
		case sig := <-ch:
			if sig.Path == p && sig.Name == ssPrompt+".Completed" && len(sig.Body) > 0 {
				dismissed, _ := sig.Body[0].(bool)
				return dismissed, nil
			}
		case <-timeout:
			return false, errors.New("timed out waiting for the keyring unlock prompt; unlock the login keyring and retry")
		}
	}
}

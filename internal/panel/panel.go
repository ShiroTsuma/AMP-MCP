// Package panel serves a small local configuration web page for an MCP tool.
//
// Security model: the server listens on 127.0.0.1 only, on a random port. Open
// returns a one-time URL carrying a random 256-bit launch token; visiting it
// sets an HttpOnly SameSite=Strict session cookie (another random 256-bit
// value) and redirects to the panel. Every request must carry an expected Host
// header (DNS-rebinding defence); POSTs must also be same-origin (Origin and
// Sec-Fetch-Site). Responses carry a strict CSP (no scripts), no-store and
// frame-deny headers. Stored passwords are never sent to the browser.
package panel

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"
)

// FieldType is the HTML input type of a field.
type FieldType string

const (
	Text     FieldType = "text"
	Number   FieldType = "number"
	Checkbox FieldType = "checkbox" // value "true" or ""
	Select   FieldType = "select"
	Password FieldType = "password" // never rendered back; empty input = keep stored value
)

// Field describes one form field.
type Field struct {
	Name     string
	Label    string
	Type     FieldType
	Options  []string // Select choices
	Required bool
	Help     string
}

// Record holds one configuration entry as field name -> string value.
//
// In records returned by Load, a non-empty Password value only signals that a
// secret is stored; it is never sent to the browser. In records passed to
// Save and actions, a Password key is present only when the user typed a new
// value; an absent key means "keep the stored secret".
type Record map[string]string

// Action is a button on the edit form (e.g. "Test"). Run receives the
// record's original key ("" for a new record) and the submitted values; its
// message is shown as a status line.
type Action struct {
	Name  string
	Label string
	Run   func(ctx context.Context, key string, rec Record) (string, error)
}

// Config plugs a tool's schema and handlers into the panel.
type Config struct {
	App    string // single-instance key, e.g. "db-browser"
	Title  string
	Fields []Field
	Key    string // name of the field that identifies a record (not a Password field)

	Load     func(ctx context.Context) ([]Record, error)
	Validate func(rec Record) error // optional, after built-in type checks
	// Save stores rec. oldKey is the record's previous key ("" when new), so a
	// rename can be detected.
	Save    func(ctx context.Context, oldKey string, rec Record) error
	Delete  func(ctx context.Context, key string) error // optional
	Actions []Action                                    // optional, e.g. Test

	// Optional vault hooks (Linux headless). When Locked returns true the
	// panel shows a passphrase prompt and calls Unlock before anything else.
	// Wire them to secretstore.VaultLocked / secretstore.UnlockVault.
	Locked func() bool
	Unlock func(passphrase string) error

	IdleTimeout time.Duration // default 10 minutes

	// ReadOnly hides New and Save and renders text fields read-only; only
	// Actions (and Delete, when set) remain.
	ReadOnly bool
}

const defaultIdle = 10 * time.Minute

var (
	//go:embed assets
	assets embed.FS
	tmpl   = template.Must(template.ParseFS(assets, "assets/page.gohtml"))

	mu        sync.Mutex
	instances = map[string]*server{}

	// OpenBrowser is replaced in tests.
	OpenBrowser = defaultOpenBrowser
)

type server struct {
	cfg     Config
	host    string // "127.0.0.1:port"
	cookie  string // cookie name, port-specific: browsers share cookies across ports
	session string
	srv     *http.Server
	idle    *time.Timer
	done    chan struct{}
	stop    sync.Once

	mu     sync.Mutex
	launch map[string]time.Time // one-time launch tokens -> expiry
}

// Open starts the panel for cfg.App (or reuses the running one), tries to
// open the default browser and returns a fresh one-time URL. It does not
// block: the server runs in a goroutine until the idle timeout or until the
// user presses Close. ctx only bounds startup; the panel outlives it.
//
// On a remote Linux host, forward the same port and open the URL locally:
//
//	ssh -L <port>:127.0.0.1:<port> user@host
func Open(ctx context.Context, cfg Config) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if cfg.App == "" || cfg.Load == nil || cfg.Save == nil || cfg.Key == "" {
		return "", errors.New("panel: App, Key, Load and Save are required")
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdle
	}
	mu.Lock()
	defer mu.Unlock()
	s := instances[cfg.App]
	if s == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		s = &server{cfg: cfg, host: ln.Addr().String(), session: token(), launch: map[string]time.Time{}, done: make(chan struct{})}
		s.cookie = "mcp_panel_" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
		s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
		s.idle = time.AfterFunc(cfg.IdleTimeout, s.shutdown)
		instances[cfg.App] = s
		go s.srv.Serve(ln)
	}
	t := token()
	s.mu.Lock()
	s.launch[t] = time.Now().Add(cfg.IdleTimeout)
	s.mu.Unlock()
	u := "http://" + s.host + "/?t=" + t
	_ = OpenBrowser(u) // no browser (headless/SSH): the caller shows the URL
	return u, nil
}

func (s *server) shutdown() {
	s.stop.Do(func() { close(s.done) })
	mu.Lock()
	if instances[s.cfg.App] == s {
		delete(instances, s.cfg.App)
	}
	mu.Unlock()
	s.idle.Stop()
	go s.srv.Shutdown(context.Background())
}

// Wait blocks until the panel for app stops (idle timeout or Close). It
// returns at once when no panel for app is running.
func Wait(app string) {
	mu.Lock()
	s := instances[app]
	mu.Unlock()
	if s != nil {
		<-s.done
	}
}

func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func defaultOpenBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no graphical session")
		}
		cmd = exec.Command("xdg-open", u)
	default:
		return errors.New("unsupported OS")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	if r.Host != s.host {
		http.Error(w, "unexpected Host header", http.StatusMisdirectedRequest)
		return
	}
	s.idle.Reset(s.cfg.IdleTimeout)
	if r.URL.Path == "/style.css" {
		http.ServeFileFS(w, r, assets, "assets/style.css")
		return
	}
	if t := r.URL.Query().Get("t"); t != "" && r.URL.Path == "/" && r.Method == http.MethodGet {
		if !s.useLaunchToken(t) {
			http.Error(w, "This panel link has expired or was already used. Ask the tool to open the configuration panel again.", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	c, err := r.Cookie(s.cookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.session)) != 1 {
		http.Error(w, "Not authorized. Open the link provided by the tool.", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+s.host {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		if f := r.Header.Get("Sec-Fetch-Site"); f != "" && f != "same-origin" {
			http.Error(w, "cross-site request rejected", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
	} else if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.route(w, r)
}

func (s *server) useLaunchToken(t string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, exp := range s.launch {
		if subtle.ConstantTimeCompare([]byte(k), []byte(t)) == 1 {
			delete(s.launch, k)
			return time.Now().Before(exp)
		}
	}
	return false
}

// view is the template model.
type view struct {
	Title    string
	Page     string // list | form | unlock | closed
	Status   string
	IsError  bool
	Fields   []Field
	Key      string
	Records  []Record
	Columns  []Field
	OrigKey  string
	Values   Record
	Stored   map[string]bool // password fields that have a stored secret
	Actions  []Action
	CanDel   bool
	ReadOnly bool
	IsNew    bool
}

func (s *server) render(w http.ResponseWriter, v view) {
	v.Title = s.cfg.Title
	if v.Title == "" {
		v.Title = s.cfg.App + " configuration"
	}
	v.Fields, v.Key, v.Actions, v.CanDel, v.ReadOnly = s.cfg.Fields, s.cfg.Key, s.cfg.Actions, s.cfg.Delete != nil, s.cfg.ReadOnly
	for _, f := range s.cfg.Fields {
		if f.Type != Password {
			v.Columns = append(v.Columns, f)
		}
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	if v.IsError {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	if err := tmpl.Execute(w, v); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *server) route(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	post := r.Method == http.MethodPost
	if post && r.URL.Path == "/close" {
		s.render(w, view{Page: "closed"})
		s.shutdown()
		return
	}
	if s.cfg.Locked != nil && s.cfg.Locked() {
		if post && r.URL.Path == "/unlock" && s.cfg.Unlock != nil {
			if err := s.cfg.Unlock(r.PostForm.Get("passphrase")); err != nil {
				s.render(w, view{Page: "unlock", Status: err.Error(), IsError: true})
				return
			}
			s.list(w, r, "Vault unlocked.", false)
			return
		}
		s.render(w, view{Page: "unlock"})
		return
	}
	switch {
	case !post && r.URL.Path == "/":
		s.list(w, r, "", false)
	case !post && r.URL.Path == "/edit":
		key := r.URL.Query().Get("key")
		if key == "" {
			s.render(w, view{Page: "form", IsNew: true, Values: Record{}})
			return
		}
		rec, err := s.find(ctx, key)
		if err != nil {
			s.list(w, r, err.Error(), true)
			return
		}
		s.renderForm(w, key, rec, rec, "", false)
	case post && r.URL.Path == "/submit":
		s.submit(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) list(w http.ResponseWriter, r *http.Request, status string, isErr bool) {
	recs, err := s.cfg.Load(r.Context())
	if err != nil {
		status, isErr = err.Error(), true
	}
	s.render(w, view{Page: "list", Records: recs, Status: status, IsError: isErr})
}

func (s *server) find(ctx context.Context, key string) (Record, error) {
	recs, err := s.cfg.Load(ctx)
	if err != nil {
		return nil, err
	}
	for _, rec := range recs {
		if rec[s.cfg.Key] == key {
			return rec, nil
		}
	}
	return nil, errors.New("record " + strconv.Quote(key) + " not found")
}

// renderForm shows values; password values are dropped and only "stored" flags
// (from the persisted record) reach the template.
func (s *server) renderForm(w http.ResponseWriter, origKey string, stored, values Record, status string, isErr bool) {
	v := view{Page: "form", OrigKey: origKey, IsNew: origKey == "", Values: Record{}, Stored: map[string]bool{}, Status: status, IsError: isErr}
	for _, f := range s.cfg.Fields {
		if f.Type == Password {
			v.Stored[f.Name] = stored[f.Name] != ""
			continue
		}
		v.Values[f.Name] = values[f.Name]
	}
	s.render(w, v)
}

func (s *server) submit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	origKey := r.PostForm.Get("_key")
	stored := Record{}
	if origKey != "" {
		var err error
		if stored, err = s.find(ctx, origKey); err != nil {
			s.list(w, r, err.Error(), true)
			return
		}
	}
	rec := Record{}
	for _, f := range s.cfg.Fields {
		val := r.PostForm.Get(f.Name)
		switch f.Type {
		case Password:
			if val != "" {
				rec[f.Name] = val
			}
			continue
		case Checkbox:
			if val != "" {
				val = "true"
			}
		}
		rec[f.Name] = val
	}
	op := r.PostForm.Get("_op")
	fail := func(err error) { s.renderForm(w, origKey, stored, rec, err.Error(), true) }
	switch {
	case op == "delete" && s.cfg.Delete != nil && origKey != "":
		if err := s.cfg.Delete(ctx, origKey); err != nil {
			fail(err)
			return
		}
		s.list(w, r, "Deleted "+origKey+".", false)
	case op == "save" && !s.cfg.ReadOnly:
		if err := s.validate(rec, stored); err != nil {
			fail(err)
			return
		}
		if err := s.cfg.Save(ctx, origKey, rec); err != nil {
			fail(err)
			return
		}
		s.list(w, r, "Saved "+rec[s.cfg.Key]+".", false)
	default:
		i := slices.IndexFunc(s.cfg.Actions, func(a Action) bool { return "action:"+a.Name == op })
		if i < 0 {
			fail(errors.New("unknown operation"))
			return
		}
		if err := s.validate(rec, stored); err != nil {
			fail(err)
			return
		}
		msg, err := s.cfg.Actions[i].Run(ctx, origKey, rec)
		if err != nil {
			fail(err)
			return
		}
		s.renderForm(w, origKey, stored, rec, msg, false)
	}
}

// validate applies the field schema, then cfg.Validate.
func (s *server) validate(rec, stored Record) error {
	for _, f := range s.cfg.Fields {
		val, label := rec[f.Name], f.Label
		if label == "" {
			label = f.Name
		}
		if f.Required && val == "" && !(f.Type == Password && stored[f.Name] != "") {
			return errors.New(label + " is required")
		}
		if val == "" {
			continue
		}
		switch f.Type {
		case Number:
			if _, err := strconv.ParseFloat(val, 64); err != nil {
				return errors.New(label + " must be a number")
			}
		case Select:
			if !slices.Contains(f.Options, val) {
				return errors.New(label + " has an invalid choice")
			}
		}
	}
	if s.cfg.Validate != nil {
		return s.cfg.Validate(rec)
	}
	return nil
}

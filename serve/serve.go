package serve

import (
	"crypto/rand"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/anton-evseev/cragcast/config"
)

//go:embed about.html error.gohtml
var embedded embed.FS

type ServeOptions struct {
	Regions []config.Region
	DataDir string
	Addr    string
}

type server struct {
	regions   []string            // region ids, in config order
	areas     map[string][]string // region id -> area ids (non-empty areas only)
	dataDir   string
	addr      string
	aboutPage []byte
	errorTmpl *template.Template
}

const (
	cookieUID    = "uid"
	cookieRegion = "region"
)

type errKind int

const (
	errNotFound errKind = iota
	errNoForecast
	errInternal
)

func Run(opts ServeOptions) error {
	s, err := newServer(opts)
	if err != nil {
		return fmt.Errorf("init serve: %w", err)
	}
	log.Printf("serving forecast data on %s", opts.Addr)
	return http.ListenAndServe(opts.Addr, s.middleware(s.routes()))
}

func newServer(opts ServeOptions) (*server, error) {
	if len(opts.Regions) == 0 {
		return nil, errors.New("no regions configured")
	}
	about, err := embedded.ReadFile("about.html")
	if err != nil {
		return nil, fmt.Errorf("read about.html: %w", err)
	}
	errTmpl, err := template.ParseFS(embedded, "error.gohtml")
	if err != nil {
		return nil, fmt.Errorf("parse error.gohtml: %w", err)
	}
	regionIds := make([]string, len(opts.Regions))
	areas := make(map[string][]string, len(opts.Regions))
	for i, r := range opts.Regions {
		regionIds[i] = r.Id
		var ids []string
		for _, a := range r.Areas {
			if len(a.Crags) > 0 { // empty areas have no subpage — no route
				ids = append(ids, a.Id)
			}
		}
		areas[r.Id] = ids
	}
	return &server{
		regions:   regionIds,
		areas:     areas,
		dataDir:   opts.DataDir,
		aboutPage: about,
		errorTmpl: errTmpl,
	}, nil
}

func (s *server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ""
		if c, err := r.Cookie(cookieUID); err == nil && c.Value != "" {
			id = c.Value
		} else {
			id = rand.Text()
			http.SetCookie(w, &http.Cookie{
				Name: cookieUID, Value: id, Path: "/",
				MaxAge: 60 * 60 * 24 * 730, HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
		}
		log.Printf("%s\t%s\t%s", time.Now().UTC().Format(time.RFC3339), id, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.HandleFunc("GET /about", s.handleAbout)
	for _, region := range s.regions {
		mux.HandleFunc("GET /"+region, func(w http.ResponseWriter, r *http.Request) {
			s.handleRegion(w, r, region)
		})
		for _, area := range s.areas[region] {
			mux.HandleFunc("GET /"+region+"/"+area, func(w http.ResponseWriter, r *http.Request) {
				s.handleArea(w, r, region, area)
			})
		}
	}
	mux.HandleFunc("GET /", s.handleNotFound)
	return mux
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	region := s.regions[0]
	if c, err := r.Cookie(cookieRegion); err == nil && slices.Contains(s.regions, c.Value) {
		region = c.Value
	}
	s.serveForecast(w, r, region)
}

func (s *server) handleAbout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.aboutPage)
}

func (s *server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.serveError(w, r, errNotFound)
}

func (s *server) serveForecast(w http.ResponseWriter, r *http.Request, region string) {
	f, err := os.Open(filepath.Join(s.dataDir, region+".html"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.serveError(w, r, errNoForecast)
			return
		}
		s.serveError(w, r, errInternal)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.serveError(w, r, errInternal)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, region+".html", info.ModTime(), f)
}

func (s *server) serveError(w http.ResponseWriter, r *http.Request, kind errKind) {
	status, msg := http.StatusInternalServerError, "Something went wrong on our end."
	switch kind {
	case errNotFound:
		status, msg = http.StatusNotFound, "That region isn't available."
	case errNoForecast:
		status, msg = http.StatusServiceUnavailable, "We're updating the forecast — check back in a minute."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.errorTmpl.Execute(w, map[string]any{"Status": status, "Message": msg}); err != nil {
		log.Printf("render error page: %v", err)
	}
}

func (s *server) handleRegion(w http.ResponseWriter, r *http.Request, region string) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieRegion, Value: region, Path: "/",
		MaxAge: 60 * 60 * 24 * 365, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	s.serveForecast(w, r, region)
}

// handleArea serves a single area's subpage. The region and area come from
// statically registered routes (built from the validated config), never from
// request input, so the join below can't be steered outside the data dir.
func (s *server) handleArea(w http.ResponseWriter, r *http.Request, region, area string) {
	f, err := os.Open(filepath.Join(s.dataDir, region, area+".html"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.serveError(w, r, errNoForecast) // route exists; data not scraped yet
			return
		}
		s.serveError(w, r, errInternal)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.serveError(w, r, errInternal)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, area+".html", info.ModTime(), f)
}

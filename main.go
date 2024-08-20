package main

import (
	"encoding/json"
	"path/filepath"
	"errors"
	"strings"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"text/template"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		slog.Error("Failed to read config", "err", err)
		os.Exit(1)
	}

	migrationsPath := cfg.migrationsPath()
	dbUrl := cfg.url()

	ls(cfg.templates)

	if err := renderTemplates(cfg.templates, cfg.migrations); err != nil {
		slog.Error("Failed to render the templates", "err", err)
		os.Exit(1)
	}

	ls(cfg.migrations)

	slog.Debug("Starting migration", "migrationsPath", migrationsPath, "dbUrl", dbUrl)

	m, err := migrate.New(
		migrationsPath, dbUrl,
	)
	if err != nil {
		slog.Error("Failed to instantiate migrations", "err", err)
		os.Exit(2)
	}

	m.Log = &logger{debug: cfg.debug}

	if err := m.Up(); err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			slog.Info("Already up-to-date")
		} else {
			slog.Error("Failed to migrate", "err", err)
			os.Exit(3)
		}
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		vers, dirty, err := m.Version()
		if err != nil {
			if errors.Is(err, migrate.ErrNilVersion) {
				slog.Info("No migration to be performed")
				http.Error(w, "No migration to be performed", http.StatusExpectationFailed)
				return
			}
			slog.Error("Failed to get version", "err", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"version": vers,
			"dirty":   dirty,
		})
	})

	err = http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", cfg.port), h)
	slog.Info("Execution terminated", "err", err)
}

type config struct {
	dbUser string
	dbPass string
	dbHost string
	dbPort uint16
	dbName string

	migrations string
	templates string
	port       uint16
	debug      bool
}

func (c config) url() string {
	return fmt.Sprintf(
		"mysql://%s:%s@tcp(%s:%d)/%s",
		url.QueryEscape(c.dbUser),
		url.QueryEscape(c.dbPass),
		url.QueryEscape(c.dbHost),
		c.dbPort,
		url.QueryEscape(c.dbName),
	)
}

func (c config) migrationsPath() string {
	return fmt.Sprintf("file://%s", c.migrations)
}

func configFromEnv() (c config, err error) {
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		err = fmt.Errorf("Missing DB_USER")
		return
	}
	c.dbUser = dbUser

	dbPass := os.Getenv("DB_PASS")
	if dbPass == "" {
		err = fmt.Errorf("Missing DB_PASS")
		return
	}
	c.dbPass = dbPass

	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		err = fmt.Errorf("Missing DB_HOST")
		return
	}
	c.dbHost = dbHost

	dbPort, err := getPort("DB_PORT", 3306)
	if err != nil {
		return
	}
	c.dbPort = dbPort

	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		dbName = "mysql"
	}
	c.dbName = dbName

	migrations := os.Getenv("MIGRATIONS")
	if migrations == "" {
		migrations = "/migrations"
	}
	c.migrations = migrations

	templates := os.Getenv("TEMPLATES")
	if templates == "" {
		templates = "/templates"
	}
	c.templates = templates

	port, err := getPort("PORT", 8080)
	if err != nil {
		return
	}
	c.port = port

	if os.Getenv("DEBUG") != "" {
		c.debug = true
	}

	return
}

func getPort(env string, defaultPort uint16) (p uint16, err error) {
	port := os.Getenv(env)
	if port == "" {
		p = defaultPort
		return
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return
	}
	p = uint16(parsed)
	return
}

type logger struct {
	debug bool
}

func (l *logger) Printf(format string, args ...any) {
	slog.Info(fmt.Sprintf("[migrate] "+format, args...))
}

func (l *logger) Verbose() bool {
	return l.debug
}

func ls(dir string) {
	files, err := os.ReadDir(dir)
	if err != nil {
		slog.Error("Cannot list directory", "err", err)
		return
	}
	for _, entry := range files {
		slog.Info("Entry", "basedir", dir, "filename", entry.Name())
	}
}

func renderTemplates(tmplDir, dstDir string) error {
	tmpls, err := template.ParseGlob(filepath.Join(tmplDir, "*.sql.tmpl"))
	if err != nil {
		// NOTE: the error returned by the ParseGlob function is from fmt.Errorf
		if strings.Contains(err.Error(), "pattern matches no files") {
			return nil
		}
		return fmt.Errorf("failed to read templates: %w", err)
	}

	envs := envToMap()

	for _, tmpl := range tmpls.Templates() {
		if err := renderTemplate(tmpl, envs, dstDir); err != nil {
			return fmt.Errorf("failed to render template %q: %w", tmpl.Name(), err)
		}
	}

	return nil
}

func envToMap() map[string]string {
	result := map[string]string{}
	for _, v := range os.Environ() {
		split := strings.SplitN(v, "=", 2)
		if len(split) != 2 {
			continue
		}
		result[split[0]] = split[1]
	}

	return result
}

func renderTemplate(tmpl *template.Template, envs map[string]string, baseDir string) error {
	if tmpl == nil {
		return fmt.Errorf("template is nil")
	}

	tmplName := tmpl.Name()
	fileName := strings.TrimSuffix(tmplName, ".tmpl")
	filePath := filepath.Join(baseDir, fileName)

	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file to render template %q: %w", tmplName, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, envs); err != nil {
		return fmt.Errorf("failed to execute template %q: %w", tmplName, err)
	}

	return nil
}

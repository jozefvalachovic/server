// Package swagger provides a lightweight, zero-dependency API documentation
// handler for Go HTTP servers. Schema reflection runs once at Handler
// construction time (not per request). The page is rendered with Go's
// html/template; no JavaScript frameworks or external CDN dependencies are
// required. CSS respects the OS system theme via prefers-color-scheme.
//
// Usage:
//
//	mux.Handle("/docs", swagger.Handler(swagger.Config{
//	    Title:   "My API",
//	    Version: "1.0.0",
//	    Endpoints: []swagger.Endpoint{
//	        {
//	            Method:   swagger.GET,
//	            Path:     "/organisations",
//	            Summary:  "List organisations",
//	            Response: Organisation{},
//	        },
//	        {
//	            Method:    swagger.POST,
//	            Path:      "/organisations",
//	            Summary:   "Create organisation",
//	            Request:   Organisation{},
//	            Response:  Organisation{},
//	        },
//	        {
//	            Method:    swagger.PUT,
//	            Path:      "/organisations/{id}",
//	            Summary:   "Update organisation",
//	            Request:   Organisation{},
//	            Response:  Organisation{},
//	        },
//	    },
//	}))
package swagger

import (
	"embed"
	"html/template"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jozefvalachovic/server/internal/ui"
)

// Handler returns an http.Handler that serves the API documentation UI.
// It registers three internal routes (relative to whatever prefix it is
// mounted at):
//
//	/          — the rendered HTML page
//	/style.css — stylesheet
//	/script.js — interactive filter / expand-collapse JS
//
// Mount it with a trailing-slash pattern and http.StripPrefix so that the
// sub-routes resolve correctly:
//
//	mux.Handle("/docs/", http.StripPrefix("/docs", swagger.Handler(cfg)))

//go:embed templates
var templateFS embed.FS

// Method is the HTTP verb for a documented endpoint.
type Method string

const (
	GET    Method = "GET"
	POST   Method = "POST"
	PUT    Method = "PUT"
	DELETE Method = "DELETE"
)

// Field describes one JSON property resolved from a Go struct field.
type Field struct {
	Name     string
	Type     string
	Required bool    // non-pointer field without omitempty
	Fields   []Field // set for nested object types
}

// Endpoint documents one HTTP route.
type Endpoint struct {
	Method      Method
	Path        string
	Summary     string
	Description string
	// Tags are small labels rendered next to the path (e.g. "auth", "admin").
	Tags []string
	// Request is an example value (or typed nil pointer) for the JSON request
	// body. Pass nil for GET endpoints.
	Request any
	// Response is an example value (or typed nil pointer) for the success
	// payload — the T in APIResponse[T]. The full response envelope is
	// rendered automatically.
	Response any
}

// Config holds all metadata passed to Handler.
type Config struct {
	Title       string
	Description string
	Version     string
	Endpoints   []Endpoint
}

// ── internal template data types ─────────────────────────────────────────────

type endpointData struct {
	Method      string
	MethodLower string
	Path        string
	Summary     string
	Description string
	Tags        []string
	Request     []Field
	Response    []Field
}

type pageData struct {
	Title       string
	Description string
	Version     string
	Endpoints   []endpointData
}

// Handler returns an http.Handler serving the API documentation UI.
// See the package-level doc comment for the expected mount pattern.
func Handler(cfg Config) http.Handler {
	// Compile template once. ParseFS names each file by its base name, so the
	// primary entry point is "swagger.html". The {{define "fields"}} block
	// defined inside that file is registered as an associated template.
	tmpl, err := template.ParseFS(templateFS, "templates/swagger.html")
	if err != nil {
		panic("swagger: failed to parse embedded swagger.html: " + err.Error())
	}

	// Resolve schemas and build page data once, not per request.
	endpoints := make([]endpointData, 0, len(cfg.Endpoints))
	for _, ep := range cfg.Endpoints {
		var req, resp []Field
		if ep.Request != nil {
			req = schemaFields(reflect.TypeOf(ep.Request))
		}
		if ep.Response != nil {
			resp = schemaFields(reflect.TypeOf(ep.Response))
		}
		endpoints = append(endpoints, endpointData{
			Method:      string(ep.Method),
			MethodLower: strings.ToLower(string(ep.Method)),
			Path:        ep.Path,
			Summary:     ep.Summary,
			Description: ep.Description,
			Tags:        ep.Tags,
			Request:     req,
			Response:    resp,
		})
	}

	data := pageData{
		Title:       cfg.Title,
		Description: cfg.Description,
		Version:     cfg.Version,
		Endpoints:   endpoints,
	}

	// Static asset handlers — served from the embedded FS so the binary is
	// fully self-contained; no files need to be deployed alongside it.
	staticHandler := func(path, contentType string) http.HandlerFunc {
		b, ferr := templateFS.ReadFile(path)
		if ferr != nil {
			panic("swagger: failed to read embedded " + path + ": " + ferr.Error())
		}
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write(b)
		}
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(ui.StyleCSS)
	})
	mux.HandleFunc("/script.js", staticHandler("templates/script.js", "application/javascript; charset=utf-8"))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			http.Error(w, "swagger: failed to render docs", http.StatusInternalServerError)
		}
	})

	// Override the strict API CSP that may be set by an outer security middleware.
	// The docs UI loads same-origin CSS and JS, so we need to permit those.
	docsMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})

	return docsMux
}

// ── schema reflection ─────────────────────────────────────────────────────────

var timeType = reflect.TypeFor[time.Time]()

// schemaFields reflects t into a slice of Fields for the template.
// Anonymous (embedded) struct fields — e.g. Base — are inlined so their
// fields appear flat in the schema, matching JSON output.
func schemaFields(t reflect.Type) []Field {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	var out []Field
	for sf := range t.Fields() {
		if !sf.IsExported() {
			continue
		}
		// Inline embedded structs (Base → ID, CreatedAt, … appear flat).
		if sf.Anonymous {
			out = append(out, schemaFields(sf.Type)...)
			continue
		}

		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}

		jsonName, rest, _ := strings.Cut(tag, ",")
		if jsonName == "" {
			jsonName = sf.Name
		}
		omitempty := strings.Contains(rest, "omitempty")

		ft := sf.Type
		isPtr := ft.Kind() == reflect.Pointer
		if isPtr {
			ft = ft.Elem()
		}

		required := !isPtr && !omitempty

		typeName := goTypeName(ft)
		var children []Field
		if ft.Kind() == reflect.Struct && ft != timeType {
			children = schemaFields(ft)
		}

		out = append(out, Field{
			Name:     jsonName,
			Type:     typeName,
			Required: required,
			Fields:   children,
		})
	}
	return out
}

// goTypeName maps a reflected Go type to a human-readable type label.
func goTypeName(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType {
		return "string (datetime)"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice:
		return "array<" + goTypeName(t.Elem()) + ">"
	case reflect.Map:
		return "object"
	case reflect.Struct:
		return t.Name()
	default:
		return t.String()
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jozefvalachovic/server/cache"
	"github.com/jozefvalachovic/server/mcp"
	"github.com/jozefvalachovic/server/middleware"
	"github.com/jozefvalachovic/server/request"
	"github.com/jozefvalachovic/server/response"
	"github.com/jozefvalachovic/server/routes"
)

// ── domain types ──────────────────────────────────────────────────────────────

// Product is the core domain model returned by the products endpoints.
type Product struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	InStock     bool    `json:"inStock"`
}

// CreateProductRequest is the decoded request body for POST /products.
type CreateProductRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
}

// UpdateProductRequest is the decoded request body for PUT /products/{id}.
// All fields are optional — only non-zero values overwrite the stored product.
type UpdateProductRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	InStock     *bool   `json:"inStock"` // pointer so false is distinguishable from omitted
}

// EmailRequest is the decoded request body for POST /validate-email.
type EmailRequest struct {
	Email string `json:"email"`
}

// AdminStats is returned by GET /admin.
type AdminStats struct {
	TotalProducts int    `json:"totalProducts"`
	ServerVersion string `json:"serverVersion"`
}

// ── in-memory store ───────────────────────────────────────────────────────────

var (
	mu       sync.RWMutex
	products = []Product{
		{ID: 1, Name: "Widget Pro", Description: "Industrial-grade widget", Price: 49.99, InStock: true},
		{ID: 2, Name: "Gadget Mini", Description: "Compact gadget for everyday use", Price: 19.99, InStock: true},
		{ID: 3, Name: "Doohickey Max", Description: "The biggest doohickey on the market", Price: 99.99, InStock: false},
	}
	nextID = 4
)

// ── registrars ────────────────────────────────────────────────────────────────

// productRegistrar registers all /products routes, using cache when available.
func productRegistrar(mux *http.ServeMux) {
	// GET /products – paginated, cached. POST /products – mutate + invalidates.
	// The cache store is injected automatically by routes.CachedRouteHandler.
	mux.HandleFunc("/products", routes.CachedRouteHandler(
		routes.Routes{
			http.MethodGet:  listProducts,
			http.MethodPost: createProduct,
		},
		middleware.HTTPCacheConfig{
			KeyPrefix: func(r *http.Request) string {
				// Include query string in cache key so different pages are separate entries.
				// Must not contain ':' — the cache middleware uses it as a prefix separator.
				if q := r.URL.RawQuery; q != "" {
					return "products_" + q
				}
				return "products"
			},
			InvalidateMethods: []string{http.MethodPost, http.MethodPut, http.MethodDelete},
		},
	))

	// GET /products/{id}  PUT /products/{id}  DELETE /products/{id}
	mux.HandleFunc("/products/{id}", routes.RouteHandler(routes.Routes{
		http.MethodGet:    getProduct,
		http.MethodPut:    updateProduct,
		http.MethodDelete: deleteProduct,
	}))
}

// utilRegistrar registers miscellaneous demo endpoints.
func utilRegistrar(mux *http.ServeMux) {
	// /me is protected by Bearer auth; any non-empty token is accepted in this demo.
	authCfg := middleware.AuthConfig{
		Scheme: middleware.AuthSchemeBearer,
		Realm:  "Example API",
		Verify: func(_ context.Context, token string) (string, error) {
			// Demo verifier: echo the token back as the identity.
			// Replace with real JWT parsing / DB lookup in production.
			return "user:" + token, nil
		},
		// Emit structured audit events on every failed auth attempt.
		OnAuthFailure: middleware.AuditAuthFailure,
	}
	protectedMe := middleware.Auth(authCfg)(http.HandlerFunc(getMe))

	routes.RegisterRouteList(mux, []routes.Route{
		{Method: http.MethodGet, Path: "/me", Handler: protectedMe.ServeHTTP},
		{Method: http.MethodGet, Path: "/admin", Handler: getAdmin},
		{Method: http.MethodGet, Path: "/ip", Handler: getIP},
		{Method: http.MethodPost, Path: "/validate-email", Handler: validateEmail},
		{Method: http.MethodGet, Path: "/cache-demo", Handler: cacheDemo},
		{Method: http.MethodGet, Path: "/fetch", Handler: fetchDemo},
	})
}

// ── product handlers ──────────────────────────────────────────────────────────

func listProducts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 10
	}

	var all []Product
	mu.RLock()
	all = make([]Product, len(products))
	copy(all, products)
	mu.RUnlock()

	total := len(all)
	end := min(offset+limit, total)
	if offset > total {
		offset = total
	}

	page := all[offset:end]
	response.APIResponseWriterWithPagination(w, page, http.StatusOK, limit, offset, total)
}

func createProduct(w http.ResponseWriter, r *http.Request) {
	req, apiErr := response.ValidateAndDecode[CreateProductRequest](r)
	if apiErr != nil {
		response.APIErrorWriter(w, *apiErr)
		return
	}

	mu.Lock()
	p := Product{
		ID:          nextID,
		Name:        req.Name,
		Description: req.Description,
		Price:       req.Price,
		InStock:     true,
	}
	nextID++
	products = append(products, p)
	mu.Unlock()

	response.APICreated(w, p, "/products/"+strconv.Itoa(p.ID))
}

func getProduct(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		msg := "invalid product id"
		response.APIErrorWriter(w, response.APIError[Product]{
			Code:    http.StatusBadRequest,
			Message: msg,
			Error:   new(msg),
		})
		return
	}

	mu.RLock()
	var found *Product
	for i := range products {
		if products[i].ID == id {
			found = &products[i]
			break
		}
	}
	mu.RUnlock()

	if found == nil {
		msg := fmt.Sprintf("product %d not found", id)
		response.APIErrorWriter(w, response.APIError[Product]{
			Code:    http.StatusNotFound,
			Message: msg,
			Error:   new(msg),
		})
		return
	}

	response.APIResponseWriter(w, *found, http.StatusOK)
}

func updateProduct(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		msg := "invalid product id"
		response.APIErrorWriter(w, response.APIError[Product]{
			Code:    http.StatusBadRequest,
			Message: msg,
			Error:   new(msg),
		})
		return
	}

	req, apiErr := response.ValidateAndDecode[UpdateProductRequest](r)
	if apiErr != nil {
		response.APIErrorWriter(w, *apiErr)
		return
	}

	mu.Lock()
	var updated *Product
	for i := range products {
		if products[i].ID == id {
			if req.Name != "" {
				products[i].Name = req.Name
			}
			if req.Description != "" {
				products[i].Description = req.Description
			}
			if req.Price != 0 {
				products[i].Price = req.Price
			}
			if req.InStock != nil {
				products[i].InStock = *req.InStock
			}
			updated = &products[i]
			break
		}
	}
	mu.Unlock()

	if updated == nil {
		msg := fmt.Sprintf("product %d not found", id)
		response.APIErrorWriter(w, response.APIError[Product]{
			Code:    http.StatusNotFound,
			Message: msg,
			Error:   new(msg),
		})
		return
	}

	response.APIResponseWriter(w, *updated, http.StatusOK)
}

func deleteProduct(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		msg := "invalid product id"
		response.APIErrorWriter(w, response.APIError[Product]{
			Code:    http.StatusBadRequest,
			Message: msg,
			Error:   new(msg),
		})
		return
	}

	mu.Lock()
	found := false
	for i := range products {
		if products[i].ID == id {
			products = append(products[:i], products[i+1:]...)
			found = true
			break
		}
	}
	mu.Unlock()

	if !found {
		msg := fmt.Sprintf("product %d not found", id)
		response.APIErrorWriter(w, response.APIError[Product]{
			Code:    http.StatusNotFound,
			Message: msg,
			Error:   new(msg),
		})
		return
	}

	response.APINoContent(w)
}

// ── util handlers ─────────────────────────────────────────────────────────────

// getMe returns the authenticated user's identity.
// Authentication is enforced by the Auth middleware wrapper in utilRegistrar;
// by the time this handler is reached, a valid token is guaranteed.
func getMe(w http.ResponseWriter, r *http.Request) {
	identity := middleware.AuthIdentityFromContext(r)
	type User struct {
		ID       int    `json:"id"`
		Email    string `json:"email"`
		Role     string `json:"role"`
		Identity string `json:"identity"`
	}
	response.APIResponseWriter(w, User{ID: 42, Email: "demo@example.com", Role: "user", Identity: identity}, http.StatusOK)
}

// getAdmin demonstrates response.APIForbidden – requires X-Admin header.
func getAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Admin") == "" {
		response.APIForbidden(w, "X-Admin header required")
		return
	}
	mu.RLock()
	n := len(products)
	mu.RUnlock()
	response.APIResponseWriter(w, AdminStats{TotalProducts: n, ServerVersion: "1.0.0"}, http.StatusOK)
}

// getIP demonstrates request.GetIPAddress.
func getIP(w http.ResponseWriter, r *http.Request) {
	type IPResult struct {
		IP string `json:"ip"`
	}
	response.APIResponseWriter(w, IPResult{IP: request.GetIPAddress(r)}, http.StatusOK)
}

// validateEmail demonstrates request.SanitizeEmail + request.ValidateEmail.
func validateEmail(w http.ResponseWriter, r *http.Request) {
	req, apiErr := response.ValidateAndDecode[EmailRequest](r)
	if apiErr != nil {
		response.APIErrorWriter(w, *apiErr)
		return
	}

	sanitized := request.SanitizeEmail(req.Email)
	if err := request.ValidateEmail(sanitized); err != nil {
		response.APIErrorWriter(w, response.APIError[any]{
			Code:    http.StatusUnprocessableEntity,
			Message: "invalid email address",
			Error:   new(err.Error()),
		})
		return
	}

	type EmailResult struct {
		Original  string `json:"original"`
		Sanitized string `json:"sanitized"`
		Valid     bool   `json:"valid"`
	}
	response.APIResponseWriter(w, EmailResult{
		Original:  req.Email,
		Sanitized: sanitized,
		Valid:     true,
	}, http.StatusOK)
}

// cacheDemo demonstrates cache.NewCacheStore, Set, Get, GetStats, and Export.
func cacheDemo(w http.ResponseWriter, r *http.Request) {
	s, err := cache.NewCacheStore(cache.CacheConfig{
		MaxSize:         10,
		DefaultTTL:      5 * time.Second,
		CleanupInterval: 2 * time.Second,
		MaxMemoryMB:     1,
	})
	if err != nil {
		response.APIErrorWriter(w, response.APIError[any]{
			Code:    http.StatusInternalServerError,
			Message: "failed to create cache store",
			Error:   new(err.Error()),
		})
		return
	}
	defer s.Stop()

	// Store and retrieve a value.
	if err := s.Set("demo-key", "hello from cache", nil); err != nil {
		response.APIErrorWriter(w, response.APIError[any]{
			Code:    http.StatusInternalServerError,
			Message: "cache set failed",
			Error:   new(err.Error()),
		})
		return
	}

	val, _ := s.Get("demo-key")
	stats := s.GetStats()
	exported := s.Export()

	type CacheDemoResult struct {
		StoredValue any              `json:"storedValue"`
		Stats       cache.CacheStats `json:"stats"`
		Exported    map[string]any   `json:"exported"`
	}
	response.APIResponseWriter(w, CacheDemoResult{
		StoredValue: val,
		Stats:       stats,
		Exported:    exported,
	}, http.StatusOK)
}

// fetchDemo demonstrates the shared resilient outbound HTTP client.
// It calls this server's own /healthz endpoint and returns the result,
// showcasing retry-with-backoff and circuit breaker in action.
func fetchDemo(w http.ResponseWriter, r *http.Request) {
	host := os.Getenv("HTTP_HOST")
	port := os.Getenv("HTTP_PORT")
	target := fmt.Sprintf("http://%s:%s/healthz", host, port)

	resp, err := httpClient.Get(r.Context(), target)
	if err != nil {
		response.APIErrorWriter(w, response.APIError[any]{
			Code:    http.StatusBadGateway,
			Error:   new("Fetch failed"),
			Message: err.Error(),
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	var result any
	_ = json.Unmarshal(body, &result)

	type FetchResult struct {
		Target   string `json:"target"`
		Status   int    `json:"status"`
		Response any    `json:"response"`
	}
	response.APIResponseWriter(w, FetchResult{
		Target:   target,
		Status:   resp.StatusCode,
		Response: result,
	}, http.StatusOK)
}

// ── MCP tools ─────────────────────────────────────────────────────────────

// GetProductInput is the typed input for the "get_product" MCP tool.
type GetProductInput struct {
	ID int `json:"id" description:"The numeric product ID to fetch."`
}

// SearchProductsInput is the typed input for the "search_products" MCP tool.
type SearchProductsInput struct {
	Query string `json:"query" description:"Case-insensitive substring to match against product name or description."`
}

// mcpTools returns the MCP tool definitions for the example server.
// Each Tool.Handler re-uses the same in-memory store as the HTTP handlers.
func mcpTools() []mcp.Tool {
	tools := []mcp.Tool{
		{
			Name:        "list_products",
			Description: "Return the full list of products.",
			Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
				mu.RLock()
				all := make([]Product, len(products))
				copy(all, products)
				mu.RUnlock()
				return all, nil
			},
		},
		{
			Name:        "get_product",
			Description: "Fetch a single product by its numeric ID. Returns an error when not found.",
			Input:       (*GetProductInput)(nil),
			Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
				var in GetProductInput
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, err
				}
				mu.RLock()
				defer mu.RUnlock()
				for _, p := range products {
					if p.ID == in.ID {
						return p, nil
					}
				}
				return nil, fmt.Errorf("product %d not found", in.ID)
			},
		},
		{
			Name:        "search_products",
			Description: "Search products by name or description (case-insensitive substring match).",
			Input:       (*SearchProductsInput)(nil),
			Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
				var in SearchProductsInput
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, err
				}
				q := strings.ToLower(in.Query)
				mu.RLock()
				source := make([]Product, len(products))
				copy(source, products)
				mu.RUnlock()
				var matched []Product
				for _, p := range source {
					if strings.Contains(strings.ToLower(p.Name), q) ||
						strings.Contains(strings.ToLower(p.Description), q) {
						matched = append(matched, p)
					}
				}
				if matched == nil {
					matched = []Product{}
				}
				return matched, nil
			},
		},
	}
	return tools
}

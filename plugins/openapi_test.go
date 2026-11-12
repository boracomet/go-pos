package plugins

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/boracomet/go-pos/core"
)

// openapiTestEmbeddedScript pulls the document out of the reference page: it is
// the text of the api-reference element, so it has to parse back into the spec.
var openapiTestEmbeddedScript = regexp.MustCompile(`<script id="api-reference"[^>]*>([\s\S]*?)</script>`)

var openapiTestScalarPin = regexp.MustCompile(`cdn\.jsdelivr\.net/npm/@scalar/api-reference@\d+\.\d+\.\d+`)

type openapiTestSetup struct {
	Options        OpenAPIOptions
	Plugins        []core.Plugin
	Mode           core.Mode
	BasePath       string
	AllowedActions []core.HandlerAction
	Authorize      func(context.Context, core.HandlerContext) (bool, error)
}

func openapiTestPlugin(t *testing.T, options OpenAPIOptions) core.Plugin {
	t.Helper()
	plugin, err := OpenAPI(options)
	if err != nil {
		t.Fatalf("openapi: %v", err)
	}
	return plugin
}

func openapiTestNew(t *testing.T, setup openapiTestSetup) *failoverTestFacade {
	t.Helper()
	authorize := setup.Authorize
	if authorize == nil {
		authorize = func(context.Context, core.HandlerContext) (bool, error) { return true, nil }
	}
	plugins := append(append([]core.Plugin{}, setup.Plugins...), openapiTestPlugin(t, setup.Options))
	return failoverTestNewFacade(t, failoverTestSetup{
		Providers:      []*failoverTestProvider{failoverTestNewProvider("mock")},
		Plugins:        plugins,
		Mode:           setup.Mode,
		BasePath:       setup.BasePath,
		AllowedActions: setup.AllowedActions,
		Authorize:      authorize,
	})
}

// openapiTestEndpointPlugin is a plugin that contributes one documented endpoint.
func openapiTestEndpointPlugin(id, name, path string, privileged bool, meta *core.EndpointOpenAPIMetadata) core.Plugin {
	return core.DefinePlugin(core.Plugin{
		ID: id,
		Endpoints: map[string]core.PaymentEndpoint{
			name: {
				Method:     "GET",
				Path:       path,
				Privileged: privileged,
				OpenAPI:    meta,
				Handler: func(context.Context, core.EndpointContext) (any, error) {
					return map[string]any{"ok": true}, nil
				},
			},
		},
	})
}

func openapiTestWantStatus(t *testing.T, response *core.HandlerResponse, want int) {
	t.Helper()
	if response.Status != want {
		t.Errorf("HTTP status = %d, want %d (body %v)", response.Status, want, response.Body)
	}
}

func openapiTestSpec(t *testing.T, response *core.HandlerResponse) map[string]any {
	t.Helper()
	openapiTestWantStatus(t, response, 200)
	return failoverTestJSONMap(response.Body)
}

func openapiTestHTML(t *testing.T, response *core.HandlerResponse) string {
	t.Helper()
	openapiTestWantStatus(t, response, 200)
	page, ok := response.Body.(string)
	if !ok {
		t.Fatalf("body type = %T, want an HTML string", response.Body)
	}
	return page
}

func openapiTestPathKeys(spec map[string]any) []string {
	paths := failoverTestJSONMap(spec["paths"])
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func openapiTestOperation(t *testing.T, spec map[string]any, path, method string) map[string]any {
	t.Helper()
	paths := failoverTestJSONMap(spec["paths"])
	document, ok := paths[path]
	if !ok {
		t.Fatalf("spec has no path %q (paths %v)", path, openapiTestPathKeys(spec))
	}
	operation := failoverTestJSONMap(failoverTestJSONMap(document)[method])
	if len(operation) == 0 {
		t.Fatalf("path %q has no %s operation", path, method)
	}
	return operation
}

func openapiTestWantSecurity(t *testing.T, operation map[string]any, want string) {
	t.Helper()
	value, ok := operation["security"]
	if want == "" {
		if ok {
			t.Errorf("security = %v, want none", value)
		}
		return
	}
	if !ok {
		t.Fatalf("operation has no security, want %s", want)
	}
	if got := failoverTestJSON(t, value); got != want {
		t.Errorf("security = %s, want %s", got, want)
	}
}

func openapiTestWantStrings(t *testing.T, got []string, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOpenAPISpec(t *testing.T) {
	t.Run("generates a valid OpenAPI 3.1.0 specification", func(t *testing.T) {
		spec := BuildOpenAPISpec(OpenAPIOptions{
			Info: &OpenAPIInfoOptions{
				Title:       "Custom Payment API",
				Version:     "2.0.0",
				Description: "Payment handler documentation",
			},
		}, nil, nil)

		if spec.OpenAPI != "3.1.0" {
			t.Errorf("openapi = %q, want 3.1.0", spec.OpenAPI)
		}
		if spec.Info.Title != "Custom Payment API" {
			t.Errorf("info.title = %q", spec.Info.Title)
		}
		if spec.Info.Version != "2.0.0" {
			t.Errorf("info.version = %q", spec.Info.Version)
		}
		if spec.Info.Description != "Payment handler documentation" {
			t.Errorf("info.description = %q", spec.Info.Description)
		}
		if len(spec.Servers) != 1 || spec.Servers[0].URL != "/api/pay" {
			t.Errorf("servers = %v, want [{/api/pay}]", spec.Servers)
		}
		keys := make([]string, 0, len(spec.Paths))
		for key := range spec.Paths {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		openapiTestWantStrings(t, keys, []string{
			"/health",
			"/{provider}/bin-check",
			"/{provider}/callback",
			"/{provider}/installment",
			"/{provider}/payment/complete-3ds",
		})
		if _, ok := spec.Components.SecuritySchemes["bearerAuth"]; !ok {
			t.Error("components.securitySchemes has no bearerAuth")
		}
	})
}

func TestOpenAPIRoutes(t *testing.T) {
	t.Run("serves Scalar reference HTML and openapi.json through the handler", func(t *testing.T) {
		facade := openapiTestNew(t, openapiTestSetup{
			Options: OpenAPIOptions{Path: "/reference", Info: &OpenAPIInfoOptions{Title: "Test Store"}},
			Mode:    core.ModeSandbox,
		})

		html := facade.get(t, "/reference")
		openapiTestWantStatus(t, html, 200)
		if contentType := html.Headers["Content-Type"]; !strings.Contains(contentType, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", contentType)
		}
		page := openapiTestHTML(t, html)
		for _, want := range []string{"<!doctype html>", "Test Store", "@scalar/api-reference"} {
			if !strings.Contains(page, want) {
				t.Errorf("page does not contain %q", want)
			}
		}

		spec := openapiTestSpec(t, facade.get(t, "/reference/openapi.json"))
		if failoverTestText(spec, "openapi") != "3.1.0" {
			t.Errorf("openapi = %q, want 3.1.0", failoverTestText(spec, "openapi"))
		}
		if title := failoverTestText(failoverTestJSONMap(spec["info"]), "title"); title != "Test Store" {
			t.Errorf("info.title = %q, want Test Store", title)
		}
	})

	t.Run("documents registered plugin endpoints with openapi metadata", func(t *testing.T) {
		facade := openapiTestNew(t, openapiTestSetup{
			Options: OpenAPIOptions{Path: "/reference"},
			Mode:    core.ModeSandbox,
			Plugins: []core.Plugin{openapiTestEndpointPlugin("custom-inventory", "stockCheck", "/inventory/check", true,
				&core.EndpointOpenAPIMetadata{
					Summary:     "Check product stock",
					Description: "Returns remaining product stock levels",
					Tags:        []string{"Inventory"},
				})},
		})

		spec := openapiTestSpec(t, facade.get(t, "/reference/openapi.json"))
		operation := openapiTestOperation(t, spec, "/inventory/check", "get")
		if failoverTestText(operation, "summary") != "Check product stock" {
			t.Errorf("summary = %q", failoverTestText(operation, "summary"))
		}
		if failoverTestText(operation, "description") != "Returns remaining product stock levels" {
			t.Errorf("description = %q", failoverTestText(operation, "description"))
		}
		tags, _ := operation["tags"].([]any)
		if len(tags) != 1 || tags[0] != "Inventory" {
			t.Errorf("tags = %v, want [Inventory]", tags)
		}
		openapiTestWantSecurity(t, operation, `[{"bearerAuth":[]}]`)
	})

	t.Run("returns 404 when disabled option is true", func(t *testing.T) {
		facade := openapiTestNew(t, openapiTestSetup{
			Options: OpenAPIOptions{Path: "/reference", Disabled: pluginTestPtr(true)},
			Mode:    core.ModeSandbox,
		})

		openapiTestWantStatus(t, facade.get(t, "/reference"), 404)
		openapiTestWantStatus(t, facade.get(t, "/reference/openapi.json"), 404)
	})

	t.Run("supports custom path option", func(t *testing.T) {
		facade := openapiTestNew(t, openapiTestSetup{
			Options: OpenAPIOptions{Path: "/docs"},
			Mode:    core.ModeSandbox,
		})

		openapiTestWantStatus(t, facade.get(t, "/docs"), 200)
		openapiTestWantStatus(t, facade.get(t, "/docs/openapi.json"), 200)
	})

	t.Run("keeps quotes and angle brackets in the document from breaking the page", func(t *testing.T) {
		const tricky = "Doesn't need a card & it's </script><script>alert(1)</script>"
		facade := openapiTestNew(t, openapiTestSetup{
			Mode: core.ModeSandbox,
			Plugins: []core.Plugin{openapiTestEndpointPlugin("quotes", "check", "/quote-check", false,
				&core.EndpointOpenAPIMetadata{Summary: "Quote check", Description: tricky})},
		})

		page := openapiTestHTML(t, facade.get(t, "/reference"))
		embedded := openapiTestEmbeddedScript.FindStringSubmatch(page)
		if embedded == nil {
			t.Fatal("the page has no api-reference element")
		}
		var spec map[string]any
		if err := json.Unmarshal([]byte(embedded[1]), &spec); err != nil {
			t.Fatalf("embedded document does not parse: %v", err)
		}
		operation := openapiTestOperation(t, spec, "/quote-check", "get")
		if failoverTestText(operation, "description") != tricky {
			t.Errorf("description = %q, want %q", failoverTestText(operation, "description"), tricky)
		}
		if got := strings.Count(page, "</script>"); got != 2 {
			t.Errorf("closing script tags = %d, want 2", got)
		}
		if got := strings.Count(page, "<script"); got != 2 {
			t.Errorf("opening script tags = %d, want 2", got)
		}
	})

	t.Run("escapes the page title", func(t *testing.T) {
		facade := openapiTestNew(t, openapiTestSetup{
			Options: OpenAPIOptions{Info: &OpenAPIInfoOptions{Title: `Tom & Jerry's "API"`}},
			Mode:    core.ModeSandbox,
		})

		page := openapiTestHTML(t, facade.get(t, "/reference"))
		if !strings.Contains(page, `<title>Tom &amp; Jerry&#39;s &quot;API&quot;</title>`) {
			t.Errorf("page title is not escaped: %s", page)
		}
	})

	t.Run("pins the Scalar script and accepts a self-hosted URL", func(t *testing.T) {
		facade := openapiTestNew(t, openapiTestSetup{Mode: core.ModeSandbox})
		if page := openapiTestHTML(t, facade.get(t, "/reference")); !openapiTestScalarPin.MatchString(page) {
			t.Error("the reference page does not pin a Scalar version")
		}

		selfHosted := openapiTestNew(t, openapiTestSetup{
			Options: OpenAPIOptions{ScalarScriptURL: "/assets/scalar.js"},
			Mode:    core.ModeSandbox,
		})
		page := openapiTestHTML(t, selfHosted.get(t, "/reference"))
		if !strings.Contains(page, `<script src="/assets/scalar.js"></script>`) {
			t.Error("the self-hosted script URL is not served")
		}
		if strings.Contains(page, "jsdelivr") {
			t.Error("the self-hosted page still loads the CDN script")
		}
	})

	t.Run("documents only the actions the handler serves, under its basePath", func(t *testing.T) {
		facade := openapiTestNew(t, openapiTestSetup{
			Mode:           core.ModeSandbox,
			BasePath:       "/shop/pay",
			AllowedActions: core.AllHandlerActions,
		})

		spec := openapiTestSpec(t, facade.get(t, "/reference/openapi.json"))
		servers, _ := spec["servers"].([]any)
		if len(servers) != 1 || failoverTestText(failoverTestJSONMap(servers[0]), "url") != "/shop/pay" {
			t.Errorf("servers = %v, want [{/shop/pay}]", servers)
		}
		openapiTestOperation(t, spec, "/{provider}/payment", "post")
		openapiTestOperation(t, spec, "/{provider}/cards/save", "post")

		openapiTestWantSecurity(t, openapiTestOperation(t, spec, "/{provider}/refund", "post"), `[{"bearerAuth":[]}]`)
		openapiTestWantSecurity(t, openapiTestOperation(t, spec, "/{provider}/payment/{paymentId}", "get"), `[{"bearerAuth":[]}]`)
		openapiTestWantSecurity(t, openapiTestOperation(t, spec, "/{provider}/cards/list", "post"), `[{"bearerAuth":[]}]`)
		openapiTestWantSecurity(t, openapiTestOperation(t, spec, "/{provider}/payment", "post"), "")
	})

	t.Run("leaves out the routes that are not enabled", func(t *testing.T) {
		facade := openapiTestNew(t, openapiTestSetup{
			Mode: core.ModeSandbox,
			AllowedActions: []core.HandlerAction{
				core.ActionPayment, core.ActionPaymentInit3DS, core.ActionPaymentComplete3DS,
			},
		})

		spec := openapiTestSpec(t, facade.get(t, "/reference/openapi.json"))
		openapiTestWantStrings(t, openapiTestPathKeys(spec), []string{
			"/health",
			"/{provider}/payment",
			"/{provider}/payment/complete-3ds",
			"/{provider}/payment/init-3ds",
		})
	})

	t.Run("is off in production mode unless enabled", func(t *testing.T) {
		production := openapiTestNew(t, openapiTestSetup{Mode: core.ModeProduction})
		openapiTestWantStatus(t, production.get(t, "/reference/openapi.json"), 404)

		enabled := openapiTestNew(t, openapiTestSetup{
			Options: OpenAPIOptions{Disabled: pluginTestPtr(false)},
			Mode:    core.ModeProduction,
		})
		openapiTestWantStatus(t, enabled.get(t, "/reference/openapi.json"), 200)
	})

	t.Run("puts the reference behind the authorize hook when privileged", func(t *testing.T) {
		plugin := openapiTestPlugin(t, OpenAPIOptions{Privileged: true})
		withoutHook := failoverTestNewFacade(t, failoverTestSetup{
			Providers: []*failoverTestProvider{failoverTestNewProvider("mock")},
			Plugins:   []core.Plugin{plugin},
			Mode:      core.ModeSandbox,
		})
		if _, err := core.NewHandler(withoutHook, core.HandlerOptions{BasePath: "/api/pay"}); err == nil {
			t.Fatal("handler built without an authorize hook")
		} else if !strings.Contains(strings.ToLower(err.Error()), "authorize") {
			t.Errorf("error = %q, want it to name the authorize hook", err.Error())
		}

		facade := openapiTestNew(t, openapiTestSetup{
			Options: OpenAPIOptions{Privileged: true},
			Mode:    core.ModeSandbox,
			Authorize: func(_ context.Context, hctx core.HandlerContext) (bool, error) {
				return hctx.Request.Headers["authorization"] == "Bearer docs", nil
			},
		})

		openapiTestWantStatus(t, facade.serve(t, "GET", "/reference", nil, nil), 403)
		openapiTestWantStatus(t, facade.serve(t, "GET", "/reference", nil, map[string]string{
			"authorization": "Bearer docs",
		}), 200)
	})
}

func TestOpenAPIPluginContextEndpoints(t *testing.T) {
	t.Run("gives plugins read-only endpoint descriptions without handlers", func(t *testing.T) {
		var seen core.PluginContext
		observer := core.DefinePlugin(core.Plugin{
			ID: "observer",
			Methods: func(_ context.Context, pctx core.PluginContext) (map[string]any, error) {
				seen = pctx
				return map[string]any{}, nil
			},
		})
		facade := failoverTestNewFacade(t, failoverTestSetup{
			Providers: []*failoverTestProvider{failoverTestNewProvider("mock")},
			Plugins: []core.Plugin{
				openapiTestEndpointPlugin("inventory", "stock", "/stock", false, nil),
				observer,
			},
			Mode: core.ModeSandbox,
		})
		if seen == nil {
			t.Fatal("the observer plugin never received a context")
		}

		endpoints := seen.Endpoints()
		want := []core.EndpointInfo{{
			PluginID: "inventory", Name: "stock", Method: "GET", Path: "/stock", Privileged: false,
		}}
		if !reflect.DeepEqual(endpoints, want) {
			t.Errorf("endpoints = %+v, want %+v", endpoints, want)
		}

		// Go's equivalent of a frozen record: the description is a value copy with
		// no handler field, and editing it cannot reach the registration.
		if _, hasHandler := reflect.TypeOf(core.EndpointInfo{}).FieldByName("Handler"); hasHandler {
			t.Error("core.EndpointInfo exposes a handler to other plugins")
		}
		endpoints[0].Path = "/tampered"
		if again := seen.Endpoints(); again[0].Path != "/stock" {
			t.Errorf("endpoint path = %q after editing the returned slice, want /stock", again[0].Path)
		}
		if len(facade.Endpoints()) != 1 || facade.Endpoints()[0].Path != "/stock" {
			t.Error("the registered endpoint changed")
		}
	})
}

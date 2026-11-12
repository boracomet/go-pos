// Command frameworks shows how to mount the payment handler. Go needs no
// framework adapter: the handler is an http.Handler, so it works with net/http,
// any mux that accepts one, and as middleware in front of your own routes.
//
//	go run ./examples/frameworks
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/boracomet/go-pos"
	"github.com/boracomet/go-pos/adapters"
	"github.com/boracomet/go-pos/core"
)

// Lazy construction, so the program starts (and tests build) without credentials.
var (
	once     sync.Once
	instance *pos.Payment
	buildErr error
)

func getPayment() (*pos.Payment, error) {
	once.Do(func() {
		instance, buildErr = pos.New(pos.Config{
			Mode: core.ModeSandbox,
			Providers: map[string]core.ProviderEntry{
				"iyzico": pos.IyzicoConfig{
					APIKey:    os.Getenv("IYZICO_API_KEY"),
					SecretKey: os.Getenv("IYZICO_SECRET_KEY"),
				},
			},
			// Mounting somewhere else? Set BasePath to the same prefix you
			// register below; the handler routes on the full request path.
			Handler: &core.HandlerOptions{BasePath: "/api/pay"},
		})
	})
	return instance, buildErr
}

func main() {
	payment, err := getPayment()
	if err != nil {
		log.Fatal(err)
	}

	// 1. Straight onto a mux. The trailing slash makes it a prefix route, so
	//    /api/pay/iyzico/callback and every plugin endpoint reach the handler.
	mux := http.NewServeMux()
	mux.Handle("/api/pay/", adapters.HTTPHandler(payment))

	// 2. As middleware in front of your own routes: paths under the prefix go to
	//    the handler, everything else falls through to your application.
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "kendi uygulaman")
	})
	mux.Handle("/", adapters.Middleware("/api/pay", payment)(app))

	// Bank callbacks arrive as application/x-www-form-urlencoded. Do not put a
	// JSON-only body parser in front of these routes: the handler reads the raw
	// body itself, and the signature covers the exact bytes the bank sent.
	server := &http.Server{Addr: ":8080", Handler: mux}

	fmt.Println("varsayılan açık uçlar:")
	for _, action := range core.DefaultHandlerActions {
		fmt.Println("  /api/pay/{sağlayıcı}/" + string(action))
	}
	fmt.Println("dinleniyor:", server.Addr)
	log.Fatal(server.ListenAndServe())
}

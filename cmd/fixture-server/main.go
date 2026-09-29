package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/themartz90/folderhug/internal/testfixture"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8091", "Loopback listening address")
	format := flag.String("format", string(testfixture.ApacheTable), "Listing format: apache-table, apache-pre, nginx")
	behavior := flag.String("behavior", string(testfixture.Normal), "Default synthetic response behavior")
	auth := flag.Bool("basic-auth", false, "Require the synthetic username fixture and password fixture")
	flag.Parse()
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("fixture-server requires an explicit loopback IP address")
	}
	config := testfixture.Config{Format: testfixture.Format(*format), Behavior: testfixture.Behavior(*behavior)}
	if *auth {
		config.Username, config.Password = "fixture", "fixture"
	}
	server := &http.Server{Addr: *addr, Handler: testfixture.Handler(config), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	log.Printf("Synthetic fixture listening at http://%s", *addr)
	log.Fatal(server.ListenAndServe())
}

package bfmonitor_test

import (
	"net/http"
	"time"

	bfmonitor "github.com/bernisoftware/bfocus-monitor-go"
)

// O snippet do painel (web/src/lib/monitor.ts → installSnippet 'go'), compilado aqui para
// não quebrar sem ninguém ver. Sem "Output:", o exemplo só compila.
func Example() {
	mux := http.NewServeMux()
	bfmonitor.Init(bfmonitor.Options{Key: "bf_mon_…", Release: "1.4.2", Environment: "production"})
	defer bfmonitor.Flush(2 * time.Second)

	// net/http
	http.ListenAndServe(":8080", bfmonitor.Middleware(mux))
}

func ExampleRecover() {
	bfmonitor.Init(bfmonitor.Options{Key: "bf_mon_…", Release: "1.4.2"})
	defer bfmonitor.Flush(2 * time.Second)
	defer bfmonitor.Recover()
	// ... o programa
}

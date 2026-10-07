//go:build !go1.23

package bfmonitor

import "net/http"

// requestPattern: antes do Go 1.23 a requisição não traz o padrão da rota.
func requestPattern(*http.Request) string { return "" }

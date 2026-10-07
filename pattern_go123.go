//go:build go1.23

package bfmonitor

import "net/http"

// requestPattern: o padrão da rota que o ServeMux casou ("GET /pedidos/{id}"), Go 1.23+.
func requestPattern(r *http.Request) string { return r.Pattern }

// Package bfmonitor manda os erros do seu sistema em Go para o Monitoramento do bFocus,
// onde o mesmo erro é agrupado entre todos os clientes e vira demanda para a equipe.
//
//	bfmonitor.Init(bfmonitor.Options{Key: "bf_mon_…", Release: "1.4.2", Environment: "production"})
//	defer bfmonitor.Flush(2 * time.Second)
//	http.ListenAndServe(":8080", bfmonitor.Middleware(mux))
//
// Go não tem gancho global de panic: use Middleware (servidor HTTP), defer Recover() (main e
// goroutines) e CaptureError/CaptureErrorCtx (erros devolvidos). O envio é em segundo plano,
// numa fila limitada; nenhuma falha do monitor chega ao seu programa. Só biblioteca padrão.
package bfmonitor

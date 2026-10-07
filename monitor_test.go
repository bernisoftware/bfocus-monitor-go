package bfmonitor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bfmonitor "github.com/bernisoftware/bfocus-monitor-go"
)

func setup(t *testing.T, opts bfmonitor.Options, replies ...bfmonitor.Reply) *bfmonitor.FakeServer {
	t.Helper()
	srv := bfmonitor.NewFakeServer(replies...)
	t.Cleanup(srv.Close)
	if opts.Key == "" {
		opts.Key = "bf_mon_teste"
	}
	opts.BaseURL = srv.URL
	if err := bfmonitor.Init(opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bfmonitor.Close)
	return srv
}

func onlyEvent(t *testing.T, srv *bfmonitor.FakeServer) map[string]any {
	t.Helper()
	if !bfmonitor.Flush(3 * time.Second) {
		t.Fatal("Flush não esvaziou")
	}
	evs := srv.AllEvents()
	if len(evs) != 1 {
		t.Fatalf("%d eventos, esperado 1", len(evs))
	}
	return evs[0]
}

func frames(ev map[string]any) []map[string]any {
	exc, _ := ev["exception"].(map[string]any)
	list, _ := exc["frames"].([]any)
	out := []map[string]any{}
	for _, f := range list {
		out = append(out, f.(map[string]any))
	}
	return out
}

func lastFrame(t *testing.T, ev map[string]any) map[string]any {
	t.Helper()
	fr := frames(ev)
	if len(fr) == 0 {
		t.Fatal("sem frames")
	}
	return fr[len(fr)-1]
}

func TestVersionMatchesRelease(t *testing.T) {
	raw, err := os.ReadFile("../release.json")
	if err != nil {
		t.Skip("fora do monorepo: sem monitor/release.json")
	}
	var rel struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &rel); err != nil {
		t.Fatal(err)
	}
	if rel.Version != bfmonitor.Version {
		t.Fatalf("Version = %s, monitor/release.json = %s", bfmonitor.Version, rel.Version)
	}
	readme, _ := os.ReadFile("README.md")
	if !strings.Contains(string(readme), "@v"+bfmonitor.Version) {
		t.Fatalf("README não instala a v%s", bfmonitor.Version)
	}
}

func TestInitRequiresKey(t *testing.T) {
	if err := bfmonitor.Init(bfmonitor.Options{Key: "  "}); err == nil {
		t.Fatal("chave vazia devia dar erro")
	}
}

func TestNoInitIsNoop(t *testing.T) {
	bfmonitor.Close()
	bfmonitor.CaptureError(errors.New("x"))
	bfmonitor.CaptureMessage("x", bfmonitor.LevelInfo)
	bfmonitor.SetUser("u", "c")
	bfmonitor.SetTag("k", "v")
	bfmonitor.AddBreadcrumb("a", "b", bfmonitor.LevelInfo)
	if !bfmonitor.Flush(time.Millisecond) {
		t.Fatal("sem Init, Flush devia devolver true")
	}
}

type pedidoError struct{ id int }

func (e *pedidoError) Error() string { return fmt.Sprintf("pedido %d não encontrado", e.id) }

func TestCaptureErrorWrappedUsesRootType(t *testing.T) {
	srv := setup(t, bfmonitor.Options{Release: "1.4.2"})
	root := &pedidoError{id: 42}
	bfmonitor.CaptureError(fmt.Errorf("carregar: %w", root))
	ev := onlyEvent(t, srv)
	exc := ev["exception"].(map[string]any)
	if exc["type"] != "*bfmonitor_test.pedidoError" {
		t.Errorf("type = %v", exc["type"])
	}
	// O Error() externo já contém o interno (%w): só o tipo externo.
	want := "pedido 42 não encontrado (dentro de: *fmt.wrapError)"
	if exc["message"] != want {
		t.Errorf("message = %q", exc["message"])
	}
	if ev["environment"] != "production" || ev["release"] != "1.4.2" {
		t.Errorf("environment/release = %v/%v", ev["environment"], ev["release"])
	}
	lf := lastFrame(t, ev)
	if !strings.HasSuffix(lf["function"].(string), "TestCaptureErrorWrappedUsesRootType") || lf["inApp"] != true {
		t.Errorf("último frame = %v", lf)
	}
	if lf["file"] != "monitor_test.go" {
		t.Errorf("file devia ser relativo ao diretório atual: %v", lf["file"])
	}
	for _, f := range frames(ev) {
		fn := f["function"].(string)
		if strings.HasPrefix(fn, "github.com/bernisoftware/bfocus-monitor-go.") {
			t.Errorf("frame do próprio pacote: %v", fn)
		}
		if strings.HasPrefix(fn, "testing.") && f["inApp"] != false {
			t.Errorf("stdlib marcada como do sistema: %v", f)
		}
	}
	ctx := ev["contexts"].(map[string]any)
	if ctx["runtime"].(map[string]any)["name"] != "go" {
		t.Errorf("contexts = %v", ctx)
	}
}

func TestPlainErrorType(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	bfmonitor.CaptureError(errors.New("simples"))
	exc := onlyEvent(t, srv)["exception"].(map[string]any)
	if exc["type"] != "*errors.errorString" || exc["message"] != "simples" {
		t.Errorf("exception = %v", exc)
	}
}

func TestCaptureMessageDefaults(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	bfmonitor.CaptureMessage("estoque negativo", "")
	ev := onlyEvent(t, srv)
	if ev["level"] != "info" {
		t.Errorf("level = %v", ev["level"])
	}
	if fp, _ := ev["fingerprint"].([]any); len(fp) != 1 || fp[0] != "estoque negativo" {
		t.Errorf("fingerprint = %v", ev["fingerprint"])
	}
}

func TestTagsMerge(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	bfmonitor.SetTag("modulo", "fiscal")
	bfmonitor.CaptureError(errors.New("t"), bfmonitor.WithTags(map[string]string{"etapa": "nfe"}))
	tags := onlyEvent(t, srv)["tags"].(map[string]any)
	if tags["modulo"] != "fiscal" || tags["etapa"] != "nfe" {
		t.Errorf("tags = %v", tags)
	}
}

func TestBeforeSend(t *testing.T) {
	srv := setup(t, bfmonitor.Options{BeforeSend: func(e *bfmonitor.Event) *bfmonitor.Event {
		if strings.Contains(e.Exception.Message, "descartar") {
			return nil
		}
		if strings.Contains(e.Exception.Message, "quebrar") {
			panic("bug no beforeSend")
		}
		e.Tags = map[string]string{"alterado": "sim"}
		return e
	}})
	bfmonitor.CaptureError(errors.New("descartar"))
	bfmonitor.CaptureError(errors.New("quebrar"))
	bfmonitor.CaptureError(errors.New("alterar"))
	bfmonitor.Flush(3 * time.Second)
	evs := srv.AllEvents()
	if len(evs) != 2 {
		t.Fatalf("%d eventos, esperado 2", len(evs))
	}
	if evs[1]["tags"].(map[string]any)["alterado"] != "sim" {
		t.Errorf("beforeSend não alterou: %v", evs[1])
	}
}

func TestSampleRateNegativeSendsNothing(t *testing.T) {
	srv := setup(t, bfmonitor.Options{SampleRate: -1})
	bfmonitor.CaptureError(errors.New("x"))
	bfmonitor.Flush(time.Second)
	if n := len(srv.AllEvents()); n != 0 {
		t.Fatalf("%d eventos", n)
	}
}

func TestRateLimitPerMinute(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	for i := 0; i < 150; i++ {
		bfmonitor.CaptureError(fmt.Errorf("erro %d", i))
		if i%20 == 0 {
			time.Sleep(5 * time.Millisecond) // deixa a fila (100) escoar
		}
	}
	bfmonitor.Flush(5 * time.Second)
	if n := len(srv.AllEvents()); n != 100 {
		t.Fatalf("%d eventos, esperado 100 (teto por minuto)", n)
	}
	for _, r := range srv.Requests() {
		if len(r.Events()) > 20 {
			t.Fatalf("lote com %d eventos (máx. 20)", len(r.Events()))
		}
	}
}

func TestServerErrorRetriesOnceThenDrops(t *testing.T) {
	srv := setup(t, bfmonitor.Options{}, bfmonitor.Reply{Status: 503}, bfmonitor.Reply{Status: 500})
	bfmonitor.CaptureError(errors.New("instável"))
	bfmonitor.Flush(3 * time.Second)
	if n := len(srv.Requests()); n != 2 {
		t.Fatalf("%d requisições, esperado 2 (1 + 1 nova tentativa)", n)
	}
	bfmonitor.CaptureError(errors.New("seguinte"))
	bfmonitor.Flush(3 * time.Second)
	if n := len(srv.Requests()); n != 3 {
		t.Fatalf("5xx não desliga o envio: %d requisições", n)
	}
}

func TestForbiddenDisables(t *testing.T) {
	srv := setup(t, bfmonitor.Options{}, bfmonitor.Reply{Status: 403})
	bfmonitor.CaptureError(errors.New("a"))
	bfmonitor.Flush(3 * time.Second)
	bfmonitor.CaptureError(errors.New("b"))
	bfmonitor.Flush(time.Second)
	if n := len(srv.Requests()); n != 1 || !bfmonitor.DisabledForTest() {
		t.Fatalf("403: %d requisições, desligado=%v", n, bfmonitor.DisabledForTest())
	}
}

func TestPayloadTooLargeDropsButKeepsSending(t *testing.T) {
	srv := setup(t, bfmonitor.Options{}, bfmonitor.Reply{Status: 413})
	bfmonitor.CaptureError(errors.New("a"))
	bfmonitor.Flush(3 * time.Second)
	bfmonitor.CaptureError(errors.New("b"))
	bfmonitor.Flush(3 * time.Second)
	if n := len(srv.Requests()); n != 2 || bfmonitor.DisabledForTest() {
		t.Fatalf("413: %d requisições (esperado 2, sem nova tentativa)", n)
	}
}

func TestNetworkErrorNeverPanics(t *testing.T) {
	if err := bfmonitor.Init(bfmonitor.Options{Key: "bf_mon_x", BaseURL: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	defer bfmonitor.Close()
	bfmonitor.CaptureError(errors.New("sem rede"))
	bfmonitor.Flush(2 * time.Second)
}

func TestSigningRefreshesAfterSixDays(t *testing.T) {
	srv := setup(t, bfmonitor.Options{SigningSecret: "whs_secret_A"})
	t0 := time.Unix(1760000000, 0)
	restore := bfmonitor.SetSignClockForTest(t0)
	bfmonitor.SetUser("u-123", "cliente-9")
	restore()
	defer bfmonitor.SetSignClockForTest(t0.Add(7 * 24 * time.Hour))()
	bfmonitor.CaptureError(errors.New("velho"))
	user := onlyEvent(t, srv)["user"].(map[string]any)
	want := bfmonitor.UserHash("whs_secret_A", "u-123", "cliente-9", t0.Add(7*24*time.Hour))
	if user["userHash"] != want {
		t.Fatalf("userHash = %v, esperado refeito %v", user["userHash"], want)
	}
}

func TestEventCappedAt64KB(t *testing.T) {
	ev := &bfmonitor.Event{Timestamp: "x", Level: bfmonitor.LevelError,
		Exception: bfmonitor.Exception{Type: "T", Message: strings.Repeat("m", 2000)}}
	for i := 0; i < 60; i++ {
		ev.Exception.Frames = append(ev.Exception.Frames, bfmonitor.Frame{File: strings.Repeat("f", 2000), Function: "fn", Line: i})
	}
	for i := 0; i < 30; i++ {
		ev.Breadcrumbs = append(ev.Breadcrumbs, bfmonitor.Breadcrumb{Category: "c", Message: strings.Repeat("b", 300)})
	}
	b := bfmonitor.EncodeEventForTest(ev)
	if b == nil || len(b) > 64*1024 {
		t.Fatalf("evento com %d bytes", len(b))
	}
	if ev.Exception.Frames[len(ev.Exception.Frames)-1].Line != 59 {
		t.Fatal("o corte devia manter os frames mais internos")
	}
}

// --- HTTP ---------------------------------------------------------------------

type thing struct{ name string }

func TestMiddlewareRecoversAndResponds500(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	mux := http.NewServeMux()
	mux.HandleFunc("/pedidos/", func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(bfmonitor.WithUser(r.Context(), "u-1", "c-1", "v2.1.abc"))
		bfmonitor.SetTagCtx(r.Context(), "rota", "pedidos")
		var p *thing
		_ = p.name // nil pointer: panic do runtime
	})
	app := httptest.NewServer(bfmonitor.Middleware(mux))
	defer app.Close()

	resp, err := http.Get(app.URL + "/pedidos/42?token=segredo")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("status %d, esperado 500", resp.StatusCode)
	}
	ev := onlyEvent(t, srv)
	if ev["level"] != "error" {
		t.Errorf("level = %v", ev["level"])
	}
	if tr := ev["transaction"]; tr != "GET /pedidos/" && tr != "GET /pedidos/42" {
		t.Errorf("transaction = %v", tr)
	}
	if u, _ := ev["url"].(string); strings.Contains(u, "?") || !strings.HasSuffix(u, "/pedidos/42") {
		t.Errorf("url = %v", u)
	}
	if ev["user"].(map[string]any)["externalId"] != "u-1" || ev["customer"].(map[string]any)["externalId"] != "c-1" {
		t.Errorf("identidade = %v %v", ev["user"], ev["customer"])
	}
	if ev["tags"].(map[string]any)["rota"] != "pedidos" {
		t.Errorf("tags = %v", ev["tags"])
	}
	exc := ev["exception"].(map[string]any)
	if exc["type"] != "runtime.errorString" && !strings.HasPrefix(exc["type"].(string), "runtime.") {
		t.Errorf("type = %v", exc["type"])
	}
	lf := lastFrame(t, ev)
	if !strings.Contains(lf["function"].(string), "TestMiddlewareRecoversAndResponds500") || lf["inApp"] != true {
		t.Errorf("último frame devia ser o handler: %v", lf)
	}
}

func TestMiddlewareKeepsWrittenStatus(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	h := bfmonitor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "ok")
		panic(errors.New("depois de escrever"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "http://app.local/x", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, esperado 202 (não escreve 500 por cima)", rec.Code)
	}
	if ev := onlyEvent(t, srv); ev["transaction"] != "POST /x" || ev["url"] != "http://app.local/x" {
		t.Errorf("transaction/url = %v %v", ev["transaction"], ev["url"])
	}
}

func TestMiddlewareAbortHandlerPassesThrough(t *testing.T) {
	setup(t, bfmonitor.Options{})
	h := bfmonitor.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Fatal("ErrAbortHandler devia seguir adiante")
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}

func TestConcurrentRequestsKeepTheirIdentity(t *testing.T) {
	srv := setup(t, bfmonitor.Options{SigningSecret: "s"})
	h := bfmonitor.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("u")
		ctx := bfmonitor.WithUser(r.Context(), u, "c-"+u)
		time.Sleep(10 * time.Millisecond)
		bfmonitor.CaptureErrorCtx(ctx, errors.New("falha de "+u))
	}))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/x?u=%d", i), nil))
		}(i)
	}
	wg.Wait()
	bfmonitor.Flush(3 * time.Second)
	evs := srv.AllEvents()
	if len(evs) != 20 {
		t.Fatalf("%d eventos", len(evs))
	}
	for _, ev := range evs {
		u := ev["user"].(map[string]any)["externalId"].(string)
		if ev["exception"].(map[string]any)["message"] != "falha de "+u || ev["customer"].(map[string]any)["externalId"] != "c-"+u {
			t.Errorf("identidade trocada: %v", ev)
		}
		if h, _ := ev["user"].(map[string]any)["userHash"].(string); !strings.HasPrefix(h, "v2.") {
			t.Errorf("sem assinatura: %v", ev["user"])
		}
		if ev["transaction"] != "GET /x" {
			t.Errorf("transaction = %v", ev["transaction"])
		}
	}
}

func TestWithUserOutsideMiddleware(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	bfmonitor.SetUser("processo", "")
	ctx := bfmonitor.WithUser(context.Background(), "u-req", "c-req")
	bfmonitor.CaptureErrorCtx(ctx, errors.New("job"))
	ev := onlyEvent(t, srv)
	if ev["user"].(map[string]any)["externalId"] != "u-req" {
		t.Errorf("a identidade do context devia vencer: %v", ev["user"])
	}
}

func TestRecoverCapturesFatalAndRepanics(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	var got any
	func() {
		defer func() { got = recover() }()
		func() {
			defer bfmonitor.Recover()
			panic("estourou")
		}()
	}()
	if got != "estourou" {
		t.Fatalf("Recover devia repetir o panic; veio %v", got)
	}
	ev := onlyEvent(t, srv)
	exc := ev["exception"].(map[string]any)
	if ev["level"] != "fatal" || exc["type"] != "panic" || exc["message"] != "estourou" {
		t.Errorf("evento = %v", ev)
	}
	lf := lastFrame(t, ev)
	if !strings.Contains(lf["function"].(string), "TestRecoverCapturesFatalAndRepanics") {
		t.Errorf("último frame devia ser onde estourou: %v", lf)
	}
	for _, f := range frames(ev) {
		if fn := f["function"].(string); strings.HasPrefix(fn, "runtime.gopanic") {
			t.Errorf("frame do mecanismo de panic: %v", fn)
		}
	}
}

type opaqueWrap struct{ inner error }

func (w opaqueWrap) Error() string { return "falha ao salvar" }
func (w opaqueWrap) Unwrap() error { return w.inner }

func TestWrappedWithoutInnerMessage(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	bfmonitor.CaptureError(opaqueWrap{inner: &pedidoError{id: 7}})
	exc := onlyEvent(t, srv)["exception"].(map[string]any)
	want := "pedido 7 não encontrado (dentro de: bfmonitor_test.opaqueWrap: falha ao salvar)"
	if exc["type"] != "*bfmonitor_test.pedidoError" || exc["message"] != want {
		t.Errorf("exception = %v", exc)
	}
}

func waitBeats(srv *bfmonitor.FakeServer, n int) []bfmonitor.Recorded {
	deadline := time.Now().Add(3 * time.Second)
	for len(srv.Heartbeats()) < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return srv.Heartbeats()
}

func TestHeartbeatRepeatsAndStopsOnClose(t *testing.T) {
	defer bfmonitor.SetHeartbeatIntervalForTest(30 * time.Millisecond)()
	srv := setup(t, bfmonitor.Options{Release: "9.9.9"})
	beats := waitBeats(srv, 3)
	if len(beats) < 3 {
		t.Fatalf("%d sinais de vida, esperado ≥ 3", len(beats))
	}
	if beats[0].Body["instance"] != beats[2].Body["instance"] || beats[0].Body["instance"] == "" {
		t.Errorf("instance devia ser estável: %v / %v", beats[0].Body["instance"], beats[2].Body["instance"])
	}
	if rt, _ := beats[0].Body["runtime"].(map[string]any); rt["name"] != "go" {
		t.Errorf("runtime = %v", beats[0].Body["runtime"])
	}
	bfmonitor.Close()
	n := len(srv.Heartbeats())
	time.Sleep(120 * time.Millisecond)
	if len(srv.Heartbeats()) != n {
		t.Fatal("Close devia parar o sinal de vida")
	}
	// Flush e Close não mandam sinal de vida; nem eventos chegam por esse caminho.
	if len(srv.Requests()) != 0 {
		t.Fatalf("%d requisições de eventos inesperadas", len(srv.Requests()))
	}
}

func TestHeartbeat401Disables(t *testing.T) {
	defer bfmonitor.SetHeartbeatIntervalForTest(30 * time.Millisecond)()
	srv := bfmonitor.NewFakeServer()
	srv.HeartbeatStatus = 401
	defer srv.Close()
	if err := bfmonitor.Init(bfmonitor.Options{Key: "bf_mon_x", BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	defer bfmonitor.Close()
	waitBeats(srv, 1)
	time.Sleep(100 * time.Millisecond)
	if !bfmonitor.DisabledForTest() {
		t.Fatal("401 no sinal de vida devia desligar o envio")
	}
	if n := len(srv.Heartbeats()); n != 1 {
		t.Fatalf("%d sinais de vida depois do 401 (esperado 1)", n)
	}
	bfmonitor.CaptureError(errors.New("x"))
	bfmonitor.Flush(time.Second)
	if len(srv.Requests()) != 0 {
		t.Fatal("desligado não envia eventos")
	}
}

// Binário com -trimpath: biblioteca aparece como "módulo@versão/arquivo" (sem /pkg/mod/).
func TestTrimpathLibraryIsNotInApp(t *testing.T) {
	fr := bfmonitor.FramesForTest(nil, []bfmonitor.RawFrameForTest{
		{Function: "github.com/acme/loja/apps/api/internal/server.(*S).pedido", File: "github.com/acme/loja/apps/api/internal/server/pedido.go", Line: 10},
		{Function: "github.com/labstack/echo/v4.(*Echo).ServeHTTP", File: "github.com/labstack/echo/v4@v4.15.4/echo.go", Line: 663},
	})
	byFn := map[string]bool{}
	for _, f := range fr {
		byFn[f.Function] = f.InApp
	}
	if byFn["github.com/labstack/echo/v4.(*Echo).ServeHTTP"] {
		t.Error("frame do echo (módulo@versão) marcado como código do sistema")
	}
	if !byFn["github.com/acme/loja/apps/api/internal/server.(*S).pedido"] {
		t.Error("frame do sistema não marcado como inApp")
	}
}

// Quem já tem recover por fora (Echo, chi, gin): o panic é registrado e REPASSADO.
func TestMiddlewareRepanicHandsThePanicToTheOuterRecover(t *testing.T) {
	srv := setup(t, bfmonitor.Options{})
	var outer any
	h := func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				outer = v
				// O recover do framework entrega o panic ao handler de erros, que manda os 5xx:
				// o mesmo problema NÃO pode ir duas vezes.
				bfmonitor.CaptureErrorCtx(r.Context(), fmt.Errorf("%v", v))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"message":"Internal Server Error"}`))
			}
		}()
		bfmonitor.MiddlewareRepanic(http.HandlerFunc(func(_ http.ResponseWriter, inner *http.Request) {
			r = inner // como o Echo: o contexto passa a carregar a requisição do middleware
			panic("quebrou")
		})).ServeHTTP(w, r)
	}
	app := httptest.NewServer(http.HandlerFunc(h))
	defer app.Close()
	resp, err := http.Get(app.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if outer != "quebrou" {
		t.Fatalf("o recover de fora não recebeu o panic: %v", outer)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("a resposta devia ser a do recover de fora, veio %q", resp.Header.Get("Content-Type"))
	}
	if ev := onlyEvent(t, srv); ev["exception"].(map[string]any)["message"] != "quebrou" {
		t.Errorf("evento = %v", ev["exception"])
	}
}

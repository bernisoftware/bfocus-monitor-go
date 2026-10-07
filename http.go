package bfmonitor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// scope é o que vale só para uma requisição: identidade, tags, transação e URL.
type scope struct {
	mu          sync.Mutex
	id          *identity
	tags        map[string]string
	transaction string
	url         string
	req         *http.Request // a requisição que o roteador recebeu (padrão da rota)
	// panicked: o panic que o MiddlewareRepanic já registrou nesta requisição. O recover do
	// framework (Echo, gin) costuma transformá-lo em erro e entregá-lo ao handler de erros —
	// que, se chamar CaptureErrorCtx, mandaria o mesmo problema duas vezes.
	panicked any
}

type scopeKey struct{}

func scopeFrom(ctx context.Context) *scope {
	if ctx == nil {
		return nil
	}
	sc, _ := ctx.Value(scopeKey{}).(*scope)
	return sc
}

// apply copia a requisição para o evento; devolve true se a requisição tinha identidade
// (ela vence a do processo). Chamado com c.mu travado.
func (s *scope) apply(ev *Event, tags map[string]string, secret string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transaction != "" {
		ev.Transaction = s.transaction
	} else if s.req != nil {
		ev.Transaction = transactionName(s.req)
	}
	if s.url != "" {
		ev.URL = s.url
	}
	for k, v := range s.tags {
		tags[k] = v
	}
	if s.id == nil {
		return false
	}
	setIdentity(ev, s.id, secret, now)
	return true
}

func setIdentity(ev *Event, id *identity, secret string, now time.Time) {
	if id.user != "" {
		ev.User = &User{ExternalID: id.user, UserHash: id.hash(secret, now)}
	}
	if id.customer != "" {
		ev.Customer = &Customer{ExternalID: id.customer}
	}
}

// WithUser guarda a pessoa e o cliente afetados no context da requisição — dois usuários
// simultâneos nunca trocam de identidade. Dentro do Middleware, a identidade vale para o
// resto da requisição (inclusive para o panic que o Middleware recuperar); fora dele, use o
// context devolvido em CaptureErrorCtx.
//
//	r = r.WithContext(bfmonitor.WithUser(r.Context(), usuario.ID, empresa.ID))
func WithUser(ctx context.Context, userExternalID, customerExternalID string, userHash ...string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	id := newIdentity(userExternalID, customerExternalID, userHash)
	if sc := scopeFrom(ctx); sc != nil {
		sc.mu.Lock()
		sc.id = id
		sc.mu.Unlock()
		return ctx
	}
	return context.WithValue(ctx, scopeKey{}, &scope{id: id})
}

// SetTagCtx acrescenta uma tag só aos eventos desta requisição (precisa do Middleware ou de
// um context vindo de WithUser).
func SetTagCtx(ctx context.Context, key, value string) {
	defer swallow()
	if sc := scopeFrom(ctx); sc != nil && key != "" {
		sc.mu.Lock()
		if sc.tags == nil {
			sc.tags = map[string]string{}
		}
		sc.tags[truncate(key, 64)] = truncate(value, 200)
		sc.mu.Unlock()
	}
}

// CaptureErrorCtx é o CaptureError com a identidade, a transação e a URL da requisição.
func CaptureErrorCtx(ctx context.Context, err error, opts ...CaptureOption) {
	defer swallow()
	if err == nil {
		return
	}
	c := current()
	if c == nil {
		return
	}
	sc := scopeFrom(ctx)
	if sc != nil && samePanic(sc, err) {
		return
	}
	c.capture(describeError(err), trimOwn(callers(1)), sc, opts)
}

// samePanic: o erro é o panic que esta requisição já mandou (ele mesmo, ou o texto dele).
func samePanic(sc *scope, err error) bool {
	sc.mu.Lock()
	v := sc.panicked
	sc.mu.Unlock()
	if v == nil {
		return false
	}
	if pe, ok := v.(error); ok && (errors.Is(err, pe) || err.Error() == pe.Error()) {
		return true
	}
	return err.Error() == fmt.Sprint(v)
}

// Recover, com defer no topo do main ou de uma goroutine, captura o panic (nível fatal),
// espera o envio (até 2 s) e ENTRA EM PANIC DE NOVO: o programa termina exatamente como
// terminaria sem o monitor.
//
//	func main() {
//		bfmonitor.Init(...)
//		defer bfmonitor.Recover()
//		...
//	}
func Recover() {
	v := recover()
	if v == nil {
		return
	}
	if v != http.ErrAbortHandler {
		capturePanic(v, nil, LevelFatal)
		Flush(2 * time.Second)
	}
	panic(v)
}

func capturePanic(v any, sc *scope, level Level) {
	defer swallow()
	c := current()
	if c == nil {
		return
	}
	c.capture(describePanic(v), trimPanic(callers(1)), sc, []CaptureOption{WithLevel(level)})
}

// Middleware (net/http) recupera o panic da requisição, manda ao bFocus com a transação
// ("GET /pedidos/{id}") e a URL sem query, responde 500 se nada foi escrito e NÃO entra em
// panic de novo: o servidor segue atendendo. Também deixa a requisição pronta para
// WithUser, SetTagCtx e CaptureErrorCtx.
//
//	http.ListenAndServe(":8080", bfmonitor.Middleware(mux))
//
// Echo: e.Use(echo.WrapMiddleware(bfmonitor.Middleware)) DEPOIS do middleware.Recover().
func Middleware(next http.Handler) http.Handler {
	return middleware(next, false)
}

// MiddlewareRepanic é o Middleware para quem JÁ tem um recover por fora (o middleware.Recover()
// do Echo, o Recoverer do chi, o do gin): registra o panic com a requisição e a identidade e
// o REPASSA — quem está por fora responde e loga exatamente como antes do monitor.
//
//	e.Use(middleware.Recover())
//	e.Use(echo.WrapMiddleware(bfmonitor.MiddlewareRepanic))
func MiddlewareRepanic(next http.Handler) http.Handler {
	return middleware(next, true)
}

func middleware(next http.Handler, repanic bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := &scope{url: requestURL(r)}
		if existing := scopeFrom(r.Context()); existing != nil {
			existing.mu.Lock()
			sc.id, sc.tags = existing.id, existing.tags
			existing.mu.Unlock()
		}
		r = r.WithContext(context.WithValue(r.Context(), scopeKey{}, sc))
		sc.req = r
		rw := &trackingWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v) // convenção do net/http: abortar sem registrar
			}
			capturePanic(v, sc, LevelError)
			if repanic {
				sc.mu.Lock()
				sc.panicked = v
				sc.mu.Unlock()
				panic(v)
			}
			if !rw.wrote {
				func() {
					defer swallow()
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}()
			}
		}()
		next.ServeHTTP(rw, r)
	})
}

func transactionName(r *http.Request) string {
	p := requestPattern(r)
	if p == "" {
		return r.Method + " " + r.URL.Path
	}
	if strings.Contains(p, " ") { // o padrão já traz o método ("GET /x/{id}")
		return p
	}
	return r.Method + " " + p
}

// requestURL monta a URL SEM query string nem fragmento (é onde mora token e e-mail).
func requestURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
		scheme = p
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	if host == "" {
		return r.URL.Path
	}
	return scheme + "://" + host + r.URL.Path
}

// trackingWriter sabe se o handler já escreveu algo (para não mandar um 500 por cima).
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackingWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

// Unwrap deixa o http.ResponseController chegar ao writer original.
func (t *trackingWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

func (t *trackingWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		t.wrote = true
		f.Flush()
	}
}

func (t *trackingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := t.ResponseWriter.(http.Hijacker); ok {
		t.wrote = true
		return h.Hijack()
	}
	return nil, nil, errors.New("bfmonitor: o ResponseWriter não suporta Hijack")
}

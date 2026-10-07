package bfmonitor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Ganchos só para os testes (este arquivo não entra no pacote publicado como API).

func init() {
	flushInterval = 20 * time.Millisecond
	retryDelay = 50 * time.Millisecond
}

// SetSignClockForTest fixa o relógio da assinatura; devolve a função que restaura.
func SetSignClockForTest(t time.Time) func() {
	signNow = func() time.Time { return t }
	return func() { signNow = time.Now }
}

// CaptureRawForTest captura com tipo e mensagem prontos (o conformance traz nomes de
// tipo neutros, como "ValueError", que um erro Go não tem).
func CaptureRawForTest(typ, msg string, opts ...CaptureOption) {
	c := current()
	if c == nil {
		return
	}
	c.capture(excInfo{Type: typ, Message: msg}, trimOwn(callers(1)), nil, opts)
}

// DisabledForTest diz se o envio está desligado (401/403).
func DisabledForTest() bool {
	c := current()
	return c != nil && c.disabled.Load()
}

// RawFrameForTest e FramesForTest expõem a conversão de rastro para o caso neutro.
type RawFrameForTest = rawFrame

func FramesForTest(prefixes []string, fr []RawFrameForTest) []Frame {
	return classifier{inAppPrefixes: prefixes}.toFrames(fr)
}

func EncodeEventForTest(ev *Event) []byte { return encodeEvent(ev) }

// Recorded é uma requisição que o servidor falso recebeu.
type Recorded struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
	Raw    []byte
}

// Events devolve os eventos do corpo.
func (r Recorded) Events() []map[string]any {
	list, _ := r.Body["events"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// Reply é a resposta do servidor falso.
type Reply struct {
	Status int
	Body   any
}

// FakeServer é o bFocus de mentira: grava cada requisição e responde a sequência dada
// (depois dela, 202).
type FakeServer struct {
	*httptest.Server
	mu      sync.Mutex
	reqs    []Recorded
	replies []Reply
	beats   []Recorded
	// HeartbeatStatus é a resposta ao sinal de vida (padrão 204).
	HeartbeatStatus int
}

func NewFakeServer(replies ...Reply) *FakeServer {
	f := &FakeServer{replies: replies}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := Recorded{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Raw: raw}
		_ = json.Unmarshal(raw, &rec.Body)
		f.mu.Lock()
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			f.beats = append(f.beats, rec)
			status := f.HeartbeatStatus
			f.mu.Unlock()
			if status == 0 {
				status = http.StatusNoContent
			}
			w.WriteHeader(status)
			return
		}
		i := len(f.reqs)
		f.reqs = append(f.reqs, rec)
		reply := Reply{Status: 202, Body: map[string]any{"accepted": 1}}
		if i < len(f.replies) {
			reply = f.replies[i]
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.Status)
		_ = json.NewEncoder(w).Encode(reply.Body)
	}))
	return f
}

func (f *FakeServer) Requests() []Recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Recorded(nil), f.reqs...)
}

// Heartbeats devolve os sinais de vida recebidos.
func (f *FakeServer) Heartbeats() []Recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Recorded(nil), f.beats...)
}

// SetHeartbeatIntervalForTest troca o intervalo (vale para o próximo Init).
func SetHeartbeatIntervalForTest(d time.Duration) func() {
	old := heartbeatInterval
	heartbeatInterval = d
	return func() { heartbeatInterval = old }
}

// AllEvents junta os eventos de todas as requisições.
func (f *FakeServer) AllEvents() []map[string]any {
	var out []map[string]any
	for _, r := range f.Requests() {
		out = append(out, r.Events()...)
	}
	return out
}

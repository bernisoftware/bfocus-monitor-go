package bfmonitor

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultBaseURL é a API do bFocus.
const DefaultBaseURL = "https://api.bfocus.com.br"

// Options configura o monitor. Só Key é obrigatória.
type Options struct {
	// Key é a chave de envio do agente (bf_mon_…), criada em Monitoramento → Agentes.
	Key string
	// Release é a versão do SEU sistema (ex.: "1.4.2").
	Release string
	// Environment: padrão "production".
	Environment string
	// BaseURL: padrão https://api.bfocus.com.br (sem barra final).
	BaseURL string
	// SampleRate: fração dos eventos enviada, 0..1. Zero (não preenchido) vale 1.
	// Para não enviar nada, use um valor negativo.
	SampleRate float64
	// Ignore: eventos cuja mensagem CONTÉM algum destes textos são descartados.
	Ignore []string
	// BeforeSend recebe o evento pronto e devolve o evento (alterado ou não) ou nil para
	// descartar. Um panic aqui é engolido e o evento vai como estava.
	BeforeSend func(*Event) *Event
	// SigningSecret é o segredo da chave de assinatura do sistema (o mesmo do userHash do
	// widget). Com ele SetUser e WithUser assinam a identidade sozinhos. SÓ NO SERVIDOR.
	SigningSecret string
	// InAppPrefixes: prefixos de função (ex.: "github.com/minhaempresa/") ou trechos de
	// caminho que são SEMPRE código do sistema — para quando a heurística não basta.
	InAppPrefixes []string
}

// Ajustes internos (os testes encurtam).
var (
	flushInterval     = time.Second
	retryDelay        = 2 * time.Second
	heartbeatInterval = 5 * time.Minute
	signNow           = time.Now
)

const (
	maxQueue     = 100
	batchSize    = 20
	dedupeWindow = 30 * time.Second
	perMinute    = 100
)

type client struct {
	opts       Options
	endpoint   string
	hbEndpoint string
	userAgent  string
	classifier classifier
	contexts   map[string]map[string]string

	mu       sync.Mutex
	id       *identity
	tags     map[string]string
	crumbs   []Breadcrumb
	seen     map[string]time.Time
	minute   time.Time
	inMinute int

	queue    chan []byte // eventos já serializados
	kick     chan struct{}
	pending  int64 // na fila + em envio
	disabled atomic.Bool
	closed   atomic.Bool
	stop     chan struct{}
	done     chan struct{}
	sender   sender
}

var (
	globalMu sync.RWMutex
	global   *client
)

func current() *client {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// Init liga o monitor. Não faz nenhuma chamada de rede. Chamar de novo troca a
// configuração (e volta a enviar depois de um 401/403).
func Init(opts Options) error {
	key := strings.TrimSpace(opts.Key)
	if key == "" {
		return errors.New("bfmonitor: Options.Key é obrigatória (chave bf_mon_… do agente)")
	}
	opts.Key = key
	if opts.Environment == "" {
		opts.Environment = "production"
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	c := &client{
		opts:       opts,
		endpoint:   opts.BaseURL + "/api/v1/monitor/events",
		hbEndpoint: opts.BaseURL + "/api/v1/monitor/heartbeat",
		userAgent:  SDKName + "/" + Version,
		classifier: classifier{inAppPrefixes: opts.InAppPrefixes},
		contexts: map[string]map[string]string{
			"runtime": {"name": "go", "version": runtime.Version()},
			"os":      {"name": runtime.GOOS, "arch": runtime.GOARCH},
		},
		tags:  map[string]string{},
		seen:  map[string]time.Time{},
		queue: make(chan []byte, maxQueue),
		kick:  make(chan struct{}, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	c.sender = newHTTPSender(c)
	go c.loop()
	go c.heartbeatLoop()

	globalMu.Lock()
	old := global
	global = c
	globalMu.Unlock()
	if old != nil {
		old.shutdown(0)
	}
	return nil
}

// CaptureOption ajusta uma captura.
type CaptureOption func(*captureConfig)

type captureConfig struct {
	level       Level
	tags        map[string]string
	fingerprint []string
}

// WithLevel muda o nível (padrão: error).
func WithLevel(l Level) CaptureOption { return func(c *captureConfig) { c.level = l } }

// WithTags acrescenta tags só a este evento.
func WithTags(tags map[string]string) CaptureOption {
	return func(c *captureConfig) {
		if c.tags == nil {
			c.tags = map[string]string{}
		}
		for k, v := range tags {
			c.tags[k] = v
		}
	}
}

// WithFingerprint força o agrupamento (eventos com a mesma impressão viram um grupo só).
func WithFingerprint(parts ...string) CaptureOption {
	return func(c *captureConfig) { c.fingerprint = parts }
}

// CaptureError manda o erro ao bFocus (assíncrono). err nil não faz nada.
func CaptureError(err error, opts ...CaptureOption) {
	defer swallow()
	if err == nil {
		return
	}
	c := current()
	if c == nil {
		return
	}
	c.capture(describeError(err), trimOwn(callers(1)), nil, opts)
}

// CaptureMessage manda uma mensagem como evento (padrão: info).
func CaptureMessage(msg string, level Level) {
	defer swallow()
	c := current()
	if c == nil {
		return
	}
	if !level.valid() {
		level = LevelInfo
	}
	c.capture(excInfo{Type: "Message", Message: msg}, trimOwn(callers(1)), nil,
		[]CaptureOption{WithLevel(level), WithFingerprint(msg)})
}

// SetUser define a pessoa e o cliente afetados nos próximos eventos do processo (para
// CLI, worker, job). Em servidor HTTP use WithUser no context da requisição.
// Com SigningSecret o pacote assina sozinho; sem ele, passe o userHash que o seu
// servidor calculou. Strings vazias limpam a identidade.
func SetUser(userExternalID, customerExternalID string, userHash ...string) {
	defer swallow()
	c := current()
	if c == nil {
		return
	}
	id := newIdentity(userExternalID, customerExternalID, userHash)
	if id != nil {
		id.hash(c.opts.SigningSecret, signNow())
	}
	c.mu.Lock()
	c.id = id
	c.mu.Unlock()
}

func newIdentity(user, customer string, userHash []string) *identity {
	if user == "" && customer == "" {
		return nil
	}
	id := &identity{user: user, customer: customer}
	if len(userHash) > 0 {
		id.given = userHash[0]
	}
	return id
}

// SetTag acrescenta uma tag a todos os próximos eventos do processo.
func SetTag(key, value string) {
	defer swallow()
	c := current()
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	c.tags[truncate(key, 64)] = truncate(value, 200)
	c.mu.Unlock()
}

// AddBreadcrumb registra um passo (os últimos 30 vão junto com o próximo erro).
func AddBreadcrumb(category, message string, level Level) {
	defer swallow()
	c := current()
	if c == nil {
		return
	}
	if !level.valid() {
		level = LevelInfo
	}
	c.mu.Lock()
	c.crumbs = append(c.crumbs, Breadcrumb{Timestamp: isoUTC(time.Now()), Category: truncate(category, 40),
		Message: truncate(message, 300), Level: level})
	if len(c.crumbs) > maxCrumbs {
		c.crumbs = c.crumbs[len(c.crumbs)-maxCrumbs:]
	}
	c.mu.Unlock()
}

// Flush envia o que está na fila e espera até timeout. Devolve true se esvaziou.
// Use com defer no main (CLI, serverless): defer bfmonitor.Flush(2 * time.Second).
func Flush(timeout time.Duration) bool {
	defer swallow()
	c := current()
	if c == nil {
		return true
	}
	return c.flush(timeout)
}

// Close envia o que falta (até 2 s) e desliga o monitor. Capturas depois disso são ignoradas.
func Close() {
	defer swallow()
	globalMu.Lock()
	c := global
	global = nil
	globalMu.Unlock()
	if c != nil {
		c.shutdown(2 * time.Second)
	}
}

// swallow: nenhuma falha do monitor chega ao app.
func swallow() { _ = recover() }

// excInfo é o erro já descrito (tipo e mensagem do contrato).
type excInfo struct {
	Type    string
	Message string
}

// describeError aplica a regra das exceções encadeadas (BRIEF §4): tipo e mensagem são os
// do erro mais interno (a causa raiz agrupa), com " (dentro de: <TipoExterno>: <msg externa>)";
// quando a mensagem externa já contém a interna (o caso do %w), só " (dentro de: <TipoExterno>)".
func describeError(err error) excInfo {
	root := err
	for depth := 0; depth < 32; depth++ {
		next := unwrapOne(root)
		if next == nil {
			break
		}
		root = next
	}
	info := excInfo{Type: fmt.Sprintf("%T", root), Message: safeError(root)}
	if root != err {
		outerType, outerMsg := fmt.Sprintf("%T", err), safeError(err)
		if info.Message != "" && strings.Contains(outerMsg, info.Message) {
			info.Message += " (dentro de: " + outerType + ")"
		} else {
			info.Message += " (dentro de: " + outerType + ": " + outerMsg + ")"
		}
	}
	return info
}

func unwrapOne(err error) error {
	switch e := err.(type) {
	case interface{ Unwrap() error }:
		return e.Unwrap()
	case interface{ Unwrap() []error }:
		for _, x := range e.Unwrap() {
			if x != nil {
				return x
			}
		}
	}
	return nil
}

func safeError(err error) (s string) {
	defer func() {
		if recover() != nil {
			s = fmt.Sprintf("%T", err)
		}
	}()
	return err.Error()
}

// describePanic: erro → como CaptureError; outro valor → tipo "panic".
func describePanic(v any) excInfo {
	if err, ok := v.(error); ok {
		return describeError(err)
	}
	return excInfo{Type: "panic", Message: fmt.Sprint(v)}
}

// capture monta o evento, aplica filtros e põe na fila. scope (opcional) é a requisição.
func (c *client) capture(exc excInfo, raw []rawFrame, sc *scope, opts []CaptureOption) {
	if c.closed.Load() || c.disabled.Load() {
		return
	}
	cfg := captureConfig{level: LevelError}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if !cfg.level.valid() {
		cfg.level = LevelError
	}
	exc.Message = truncate(exc.Message, maxMessage)
	for _, p := range c.opts.Ignore {
		if p != "" && strings.Contains(exc.Message, p) {
			return
		}
	}
	sr := c.opts.SampleRate
	if sr < 0 || (sr > 0 && sr < 1 && rand.Float64() >= sr) {
		return
	}
	frames := c.classifier.toFrames(raw)

	now := time.Now()
	key := exc.Type + "|" + exc.Message + "|" + dedupeFrame(frames)
	c.mu.Lock()
	if last, ok := c.seen[key]; ok && now.Sub(last) < dedupeWindow {
		c.mu.Unlock()
		return
	}
	if now.Sub(c.minute) >= time.Minute {
		c.minute, c.inMinute = now, 0
	}
	if c.inMinute >= perMinute {
		c.mu.Unlock()
		return
	}
	c.inMinute++
	c.seen[key] = now
	if len(c.seen) > 1000 {
		for k, t := range c.seen {
			if now.Sub(t) >= dedupeWindow {
				delete(c.seen, k)
			}
		}
	}
	ev := &Event{
		Timestamp:   isoUTC(now),
		Level:       cfg.level,
		Release:     c.opts.Release,
		Environment: c.opts.Environment,
		Exception:   Exception{Type: exc.Type, Message: exc.Message, Frames: frames},
		Fingerprint: cfg.fingerprint,
		Contexts:    copyContexts(c.contexts),
		SDK:         SDK{Name: SDKName, Version: Version},
	}
	if len(c.crumbs) > 0 {
		ev.Breadcrumbs = append([]Breadcrumb(nil), c.crumbs...)
	}
	tags := map[string]string{}
	for k, v := range c.tags {
		tags[k] = v
	}
	fromRequest := sc != nil && sc.apply(ev, tags, c.opts.SigningSecret, signNow())
	if !fromRequest && c.id != nil {
		setIdentity(ev, c.id, c.opts.SigningSecret, signNow())
	}
	c.mu.Unlock()
	for k, v := range cfg.tags {
		tags[truncate(k, 64)] = truncate(v, 200)
	}
	if len(tags) > 0 {
		ev.Tags = tags
	}

	if c.opts.BeforeSend != nil {
		ev = c.runBeforeSend(ev)
		if ev == nil {
			return
		}
	}
	body := encodeEvent(ev)
	if body == nil {
		return
	}
	atomic.AddInt64(&c.pending, 1)
	select {
	case c.queue <- body:
		if len(c.queue) >= batchSize {
			c.wake()
		}
	default:
		atomic.AddInt64(&c.pending, -1) // fila cheia: descarta o mais novo
	}
}

// copyContexts: cada evento com o seu mapa (o BeforeSend pode alterar sem afetar os outros).
func copyContexts(src map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(src))
	for k, v := range src {
		m := make(map[string]string, len(v))
		for kk, vv := range v {
			m[kk] = vv
		}
		out[k] = m
	}
	return out
}

func (c *client) runBeforeSend(ev *Event) (out *Event) {
	defer func() {
		if recover() != nil {
			out = ev
		}
	}()
	return c.opts.BeforeSend(ev)
}

// dedupeFrame: o frame do erro (o mais interno do sistema; senão o mais interno).
func dedupeFrame(frames []Frame) string {
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].InApp {
			return fmt.Sprintf("%s:%d", frames[i].File, frames[i].Line)
		}
	}
	if len(frames) > 0 {
		f := frames[len(frames)-1]
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return ""
}

func (c *client) wake() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// loop é a goroutine de envio: lote a cada flushInterval ou batchSize eventos.
func (c *client) loop() {
	defer close(c.done)
	defer swallow()
	t := time.NewTimer(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-c.kick:
		case <-t.C:
		}
		c.drain()
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(flushInterval)
	}
}

// drain envia tudo o que está na fila, em lotes.
func (c *client) drain() {
	for {
		batch := make([][]byte, 0, batchSize)
	fill:
		for len(batch) < batchSize {
			select {
			case b := <-c.queue:
				batch = append(batch, b)
			default:
				break fill
			}
		}
		if len(batch) == 0 {
			return
		}
		if !c.disabled.Load() {
			c.sendBatch(batch)
		}
		atomic.AddInt64(&c.pending, -int64(len(batch)))
	}
}

func (c *client) sendBatch(batch [][]byte) {
	defer swallow()
	body := make([]byte, 0, 64)
	body = append(body, `{"events":[`...)
	for i, b := range batch {
		if i > 0 {
			body = append(body, ',')
		}
		body = append(body, b...)
	}
	body = append(body, "]}"...)
	for attempt := 0; attempt < 2; attempt++ {
		status, err := c.sender.send(body)
		switch {
		case err == nil && status >= 200 && status < 300:
			return
		case err == nil && (status == 401 || status == 403):
			c.disabled.Store(true) // chave errada/revogada: não martelar até o próximo Init
			c.dropQueue()
			return
		case err != nil || status == 429 || status >= 500:
			if attempt == 0 {
				select {
				case <-time.After(retryDelay):
				case <-c.stop:
					return
				}
				continue
			}
			return
		default: // 400, 413…: descarta o lote
			return
		}
	}
}

func (c *client) dropQueue() {
	for {
		select {
		case <-c.queue:
			atomic.AddInt64(&c.pending, -1)
		default:
			return
		}
	}
}

func (c *client) flush(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	c.wake()
	for atomic.LoadInt64(&c.pending) > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
		if len(c.queue) > 0 {
			c.wake()
		}
	}
	return true
}

func (c *client) shutdown(timeout time.Duration) {
	if c.closed.Swap(true) {
		return
	}
	if timeout > 0 {
		c.flush(timeout)
	}
	close(c.stop)
	select {
	case <-c.done:
	case <-time.After(timeout + 100*time.Millisecond):
	}
}

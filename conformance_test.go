package bfmonitor_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	bfmonitor "github.com/bernisoftware/bfocus-monitor-go"
)

// Roda TODOS os casos de testdata/cases.json (cópia de monitor/conformance/cases.json,
// conferida por `python monitor/conformance/generate.py --check`).

const casesFormat = 1

type capture struct {
	Kind        string            `json:"kind"`
	Type        string            `json:"type"`
	Message     string            `json:"message"`
	Level       string            `json:"level"`
	Tags        map[string]string `json:"tags"`
	Fingerprint []string          `json:"fingerprint"`
}

type expectReq struct {
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Headers      map[string]string `json:"headers"`
	HeaderPrefix map[string]string `json:"header_prefix"`
	Event        map[string]any    `json:"event"`
}

type sendCase struct {
	Name string `json:"name"`
	Init struct {
		Key           string   `json:"key"`
		Release       string   `json:"release"`
		Environment   string   `json:"environment"`
		SigningSecret string   `json:"signing_secret"`
		Ignore        []string `json:"ignore"`
	} `json:"init"`
	SetUser *struct {
		UserExternalID     string `json:"user_external_id"`
		CustomerExternalID string `json:"customer_external_id"`
		UserHash           string `json:"user_hash"`
		Ts                 int64  `json:"ts"`
	} `json:"set_user"`
	Breadcrumbs []struct {
		Category string `json:"category"`
		Message  string `json:"message"`
		Level    string `json:"level"`
	} `json:"breadcrumbs"`
	Capture  capture `json:"capture"`
	Repeat   int     `json:"repeat"`
	Requests []struct {
		Expect  expectReq `json:"expect"`
		Respond struct {
			Status int `json:"status"`
			Body   any `json:"body"`
		} `json:"respond"`
	} `json:"requests"`
	ThenCapture *capture `json:"then_capture"`
	After       string   `json:"after"`
}

type casesFile struct {
	Version  int    `json:"version"`
	Key      string `json:"key"`
	Path     string `json:"path"`
	UserHash []struct {
		Secret             string `json:"secret"`
		Ts                 int64  `json:"ts"`
		UserExternalID     string `json:"user_external_id"`
		CustomerExternalID string `json:"customer_external_id"`
		Expected           string `json:"expected"`
	} `json:"user_hash"`
	Send      []sendCase `json:"send"`
	Heartbeat []struct {
		Name string `json:"name"`
		Init struct {
			Key         string `json:"key"`
			Release     string `json:"release"`
			Environment string `json:"environment"`
		} `json:"init"`
		Expect struct {
			Method       string            `json:"method"`
			Path         string            `json:"path"`
			Headers      map[string]string `json:"headers"`
			HeaderPrefix map[string]string `json:"header_prefix"`
			Body         map[string]any    `json:"body"`
			BodyPresent  []string          `json:"body_present"`
		} `json:"expect"`
		Respond struct {
			Status int `json:"status"`
		} `json:"respond"`
	} `json:"heartbeat"`
	Frames []struct {
		Name         string `json:"name"`
		RuntimeOrder []struct {
			File     string `json:"file"`
			Function string `json:"function"`
			Line     int    `json:"line"`
			Library  bool   `json:"library"`
		} `json:"runtime_order"`
		Expected []struct {
			File     string `json:"file"`
			Function string `json:"function"`
			Line     int    `json:"line"`
			InApp    bool   `json:"inApp"`
		} `json:"expected"`
	} `json:"frames"`
}

func loadCases(t *testing.T) casesFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatalf("testdata/cases.json: %v (rode python monitor/conformance/generate.py)", err)
	}
	var f casesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != casesFormat {
		t.Fatalf("cases.json no formato %d; esta suíte entende o %d", f.Version, casesFormat)
	}
	return f
}

func TestConformanceUserHash(t *testing.T) {
	for _, c := range loadCases(t).UserHash {
		got := bfmonitor.UserHash(c.Secret, c.UserExternalID, c.CustomerExternalID, time.Unix(c.Ts, 0))
		if got != c.Expected {
			t.Errorf("%s/%s@%d: %s, esperado %s", c.UserExternalID, c.CustomerExternalID, c.Ts, got, c.Expected)
		}
	}
}

func doCapture(c capture) {
	switch c.Kind {
	case "message":
		bfmonitor.CaptureMessage(c.Message, bfmonitor.Level(c.Level))
	default:
		var opts []bfmonitor.CaptureOption
		if c.Level != "" {
			opts = append(opts, bfmonitor.WithLevel(bfmonitor.Level(c.Level)))
		}
		if c.Tags != nil {
			opts = append(opts, bfmonitor.WithTags(c.Tags))
		}
		if c.Fingerprint != nil {
			opts = append(opts, bfmonitor.WithFingerprint(c.Fingerprint...))
		}
		bfmonitor.CaptureRawForTest(c.Type, c.Message, opts...)
	}
}

func TestConformanceSend(t *testing.T) {
	cases := loadCases(t)
	for _, sc := range cases.Send {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			replies := make([]bfmonitor.Reply, 0, len(sc.Requests))
			for _, r := range sc.Requests {
				replies = append(replies, bfmonitor.Reply{Status: r.Respond.Status, Body: r.Respond.Body})
			}
			srv := bfmonitor.NewFakeServer(replies...)
			defer srv.Close()

			err := bfmonitor.Init(bfmonitor.Options{
				Key: sc.Init.Key, Release: sc.Init.Release, Environment: sc.Init.Environment,
				SigningSecret: sc.Init.SigningSecret, Ignore: sc.Init.Ignore, BaseURL: srv.URL + "/",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer bfmonitor.Close()

			if u := sc.SetUser; u != nil {
				if u.Ts != 0 {
					defer bfmonitor.SetSignClockForTest(time.Unix(u.Ts, 0))()
				}
				if u.UserHash != "" {
					bfmonitor.SetUser(u.UserExternalID, u.CustomerExternalID, u.UserHash)
				} else {
					bfmonitor.SetUser(u.UserExternalID, u.CustomerExternalID)
				}
			}
			for _, b := range sc.Breadcrumbs {
				bfmonitor.AddBreadcrumb(b.Category, b.Message, bfmonitor.Level(b.Level))
			}
			n := sc.Repeat
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				doCapture(sc.Capture)
			}
			if !bfmonitor.Flush(5 * time.Second) {
				t.Fatal("Flush não esvaziou a fila em 5 s")
			}
			got := srv.Requests()
			if len(got) != len(sc.Requests) {
				t.Fatalf("%d requisição(ões), esperado %d", len(got), len(sc.Requests))
			}
			for i, want := range sc.Requests {
				checkRequest(t, i, got[i], want.Expect)
				// Nova tentativa (429/5xx) repete o MESMO corpo.
				if s := want.Respond.Status; (s == 429 || s >= 500) && i+1 < len(got) && string(got[i+1].Raw) != string(got[i].Raw) {
					t.Errorf("req %d: a nova tentativa mudou o corpo", i+1)
				}
			}
			if sc.ThenCapture != nil {
				doCapture(*sc.ThenCapture)
				bfmonitor.Flush(2 * time.Second)
				time.Sleep(60 * time.Millisecond)
			}
			if after := srv.Requests(); len(after) != len(sc.Requests) {
				t.Fatalf("requisição a mais depois do caso: %d (esperado %d)", len(after), len(sc.Requests))
			}
			switch sc.After {
			case "disabled":
				if !bfmonitor.DisabledForTest() {
					t.Fatal("o envio devia estar desligado")
				}
				// Novo Init volta a enviar.
				_ = bfmonitor.Init(bfmonitor.Options{Key: sc.Init.Key, BaseURL: srv.URL})
				if bfmonitor.DisabledForTest() {
					t.Fatal("novo Init devia religar o envio")
				}
			case "ok":
				if bfmonitor.DisabledForTest() {
					t.Fatal("o envio não devia estar desligado")
				}
			default:
				t.Fatalf("after desconhecido: %q", sc.After)
			}
		})
	}
}

func checkRequest(t *testing.T, i int, got bfmonitor.Recorded, want expectReq) {
	t.Helper()
	if got.Method != want.Method || got.Path != want.Path {
		t.Errorf("req %d: %s %s, esperado %s %s", i, got.Method, got.Path, want.Method, want.Path)
	}
	for h, v := range want.Headers {
		if got.Header.Get(h) != v {
			t.Errorf("req %d: header %s = %q, esperado %q", i, h, got.Header.Get(h), v)
		}
	}
	for h, p := range want.HeaderPrefix {
		if !strings.HasPrefix(got.Header.Get(h), p) {
			t.Errorf("req %d: header %s = %q, esperado prefixo %q", i, h, got.Header.Get(h), p)
		}
	}
	if c := got.Header.Get("X-bFocus-Client"); c != "bfocus-monitor-go/"+bfmonitor.Version {
		t.Errorf("req %d: X-bFocus-Client = %q", i, c)
	}
	if ua := got.Header.Get("User-Agent"); ua != "bfocus-monitor-go/"+bfmonitor.Version {
		t.Errorf("req %d: User-Agent = %q", i, ua)
	}
	events := got.Events()
	if len(events) != 1 {
		t.Fatalf("req %d: %d eventos, esperado 1 (corpo %s)", i, len(events), got.Raw)
	}
	ev := events[0]
	for path, wantV := range want.Event {
		if s, ok := wantV.(string); ok && s == "$version" {
			wantV = bfmonitor.Version
		}
		gotV, ok := dig(ev, path)
		if !ok {
			t.Errorf("req %d: campo %s ausente (corpo %s)", i, path, got.Raw)
			continue
		}
		if !reflect.DeepEqual(gotV, wantV) {
			t.Errorf("req %d: %s = %#v, esperado %#v", i, path, gotV, wantV)
		}
	}
	assertNoNulls(t, ev, "")
	if sdk, _ := dig(ev, "sdk.name"); sdk != "bfocus-monitor-go" {
		t.Errorf("sdk.name = %v", sdk)
	}
	if ts, _ := ev["timestamp"].(string); !strings.HasSuffix(ts, "Z") {
		t.Errorf("timestamp sem Z: %q", ts)
	} else if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("timestamp inválido: %q", ts)
	}
}

// dig segue um caminho com ponto ("breadcrumbs.0.category").
func dig(v any, path string) (any, bool) {
	cur := v
	for _, part := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

func assertNoNulls(t *testing.T, v any, path string) {
	t.Helper()
	switch node := v.(type) {
	case nil:
		t.Errorf("campo nulo em %q", path)
	case map[string]any:
		for k, x := range node {
			assertNoNulls(t, x, path+"."+k)
		}
	case []any:
		for i, x := range node {
			assertNoNulls(t, x, path+"."+strconv.Itoa(i))
		}
	}
}

func TestConformanceFrames(t *testing.T) {
	// O rastro neutro vira um rastro de Go: biblioteca mora no cache de módulos
	// (/pkg/mod/), código do sistema no pacote main.
	const mod = "/home/app/go/pkg/mod/"
	goFile := func(file string, library bool) string {
		if library {
			return mod + strings.TrimPrefix(file, "/")
		}
		return file
	}
	for _, fc := range loadCases(t).Frames {
		raw := make([]bfmonitor.RawFrameForTest, 0, len(fc.RuntimeOrder))
		for _, f := range fc.RuntimeOrder {
			raw = append(raw, bfmonitor.RawFrameForTest{Function: "main." + f.Function, File: goFile(f.File, f.Library), Line: f.Line})
		}
		got := bfmonitor.FramesForTest(nil, raw)
		if len(got) != len(fc.Expected) {
			t.Fatalf("%s: %d frames, esperado %d", fc.Name, len(got), len(fc.Expected))
		}
		for i, want := range fc.Expected {
			g := got[i]
			if g.Function != "main."+want.Function || g.Line != want.Line || g.InApp != want.InApp ||
				g.File != goFile(want.File, !want.InApp) {
				t.Errorf("%s: frame %d = %+v, esperado %+v", fc.Name, i, g, want)
			}
		}
	}
}

func TestConformanceHeartbeat(t *testing.T) {
	for _, hc := range loadCases(t).Heartbeat {
		hc := hc
		t.Run(hc.Name, func(t *testing.T) {
			srv := bfmonitor.NewFakeServer()
			srv.HeartbeatStatus = hc.Respond.Status
			defer srv.Close()
			if err := bfmonitor.Init(bfmonitor.Options{Key: hc.Init.Key, Release: hc.Init.Release,
				Environment: hc.Init.Environment, BaseURL: srv.URL}); err != nil {
				t.Fatal(err)
			}
			defer bfmonitor.Close()
			deadline := time.Now().Add(3 * time.Second)
			for len(srv.Heartbeats()) == 0 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			beats := srv.Heartbeats()
			if len(beats) == 0 {
				t.Fatal("Init não mandou o sinal de vida")
			}
			got := beats[0]
			want := hc.Expect
			if got.Method != want.Method || got.Path != want.Path {
				t.Errorf("%s %s, esperado %s %s", got.Method, got.Path, want.Method, want.Path)
			}
			for h, v := range want.Headers {
				if got.Header.Get(h) != v {
					t.Errorf("header %s = %q, esperado %q", h, got.Header.Get(h), v)
				}
			}
			for h, p := range want.HeaderPrefix {
				if !strings.HasPrefix(got.Header.Get(h), p) {
					t.Errorf("header %s = %q, esperado prefixo %q", h, got.Header.Get(h), p)
				}
			}
			for path, wantV := range want.Body {
				if s, ok := wantV.(string); ok && s == "$version" {
					wantV = bfmonitor.Version
				}
				if v, ok := dig(got.Body, path); !ok || !reflect.DeepEqual(v, wantV) {
					t.Errorf("%s = %#v, esperado %#v (corpo %s)", path, v, wantV, got.Raw)
				}
			}
			for _, path := range want.BodyPresent {
				if v, ok := dig(got.Body, path); !ok || v == "" {
					t.Errorf("%s ausente (corpo %s)", path, got.Raw)
				}
			}
			assertNoNulls(t, got.Body, "")
			if bfmonitor.DisabledForTest() {
				t.Error("204 não desliga")
			}
		})
	}
}

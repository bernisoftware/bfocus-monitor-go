package bfmonitor

import (
	"encoding/json"
	"time"
)

// Level é a gravidade do evento.
type Level string

const (
	// LevelFatal: o processo vai morrer (panic não tratado no topo, via Recover).
	LevelFatal Level = "fatal"
	// LevelError: o padrão de CaptureError e do Middleware.
	LevelError Level = "error"
	// LevelWarning: algo errado que não interrompeu a operação.
	LevelWarning Level = "warning"
	// LevelInfo: o padrão de CaptureMessage.
	LevelInfo Level = "info"
)

func (l Level) valid() bool {
	return l == LevelFatal || l == LevelError || l == LevelWarning || l == LevelInfo
}

// Frame é uma linha do rastro. A lista vai de FORA para DENTRO: o último frame é onde
// o erro aconteceu.
type Frame struct {
	File     string `json:"file,omitempty"`
	Function string `json:"function,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	InApp    bool   `json:"inApp"`
}

// Exception é o erro em si.
type Exception struct {
	Type    string  `json:"type"`
	Message string  `json:"message"`
	Frames  []Frame `json:"frames,omitempty"`
}

// User identifica a pessoa afetada. UserHash é a assinatura v2 (a mesma do widget).
type User struct {
	ExternalID string `json:"externalId"`
	UserHash   string `json:"userHash,omitempty"`
}

// Customer identifica o cliente (empresa) afetado.
type Customer struct {
	ExternalID string `json:"externalId"`
}

// Breadcrumb é um passo que aconteceu antes do erro.
type Breadcrumb struct {
	Timestamp string `json:"timestamp"`
	Category  string `json:"category"`
	Message   string `json:"message"`
	Level     Level  `json:"level"`
}

// SDK identifica este pacote no evento.
type SDK struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Event é o evento enviado ao bFocus (contrato em monitor/BRIEF.md §4). Campos vazios
// não vão no JSON.
type Event struct {
	Timestamp   string                       `json:"timestamp"`
	Level       Level                        `json:"level"`
	Release     string                       `json:"release,omitempty"`
	Environment string                       `json:"environment,omitempty"`
	Exception   Exception                    `json:"exception"`
	Transaction string                       `json:"transaction,omitempty"`
	URL         string                       `json:"url,omitempty"`
	User        *User                        `json:"user,omitempty"`
	Customer    *Customer                    `json:"customer,omitempty"`
	Tags        map[string]string            `json:"tags,omitempty"`
	Breadcrumbs []Breadcrumb                 `json:"breadcrumbs,omitempty"`
	Fingerprint []string                     `json:"fingerprint,omitempty"`
	Contexts    map[string]map[string]string `json:"contexts,omitempty"`
	SDK         SDK                          `json:"sdk"`
}

const (
	maxEventBytes = 64 * 1024
	maxMessage    = 2000
	maxFrames     = 60
	maxCrumbs     = 30
)

func isoUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Não corta no meio de um caractere UTF-8.
	for n > 0 && n < len(s) && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

// encodeEvent serializa o evento cortando mensagem, breadcrumbs e frames até caber em 64 KB.
// Devolve nil se não der para serializar.
func encodeEvent(ev *Event) json.RawMessage {
	for attempt := 0; attempt < 5; attempt++ {
		b, err := json.Marshal(ev)
		if err != nil {
			return nil
		}
		if len(b) <= maxEventBytes {
			return b
		}
		switch attempt {
		case 0:
			ev.Breadcrumbs = nil
		case 1:
			if len(ev.Exception.Frames) > 20 {
				ev.Exception.Frames = ev.Exception.Frames[len(ev.Exception.Frames)-20:]
			}
		case 2:
			ev.Exception.Message = truncate(ev.Exception.Message, 500)
			ev.Transaction = truncate(ev.Transaction, 300)
			ev.Tags = nil
			ev.Contexts = nil
		case 3:
			for i := range ev.Exception.Frames {
				ev.Exception.Frames[i].File = truncate(ev.Exception.Frames[i].File, 300)
				ev.Exception.Frames[i].Function = truncate(ev.Exception.Frames[i].Function, 200)
			}
			ev.Fingerprint = nil
		}
	}
	return nil
}

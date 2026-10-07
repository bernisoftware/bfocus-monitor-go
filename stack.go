package bfmonitor

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

const ownPkg = "github.com/bernisoftware/bfocus-monitor-go."

// rawFrame é um frame como o runtime dá (de DENTRO para fora).
type rawFrame struct {
	Function string
	File     string
	Line     int
}

// callers lê a pilha de quem chamou, de dentro para fora. skip conta a partir de quem
// chamou callers.
func callers(skip int) []rawFrame {
	pcs := make([]uintptr, 100)
	n := runtime.Callers(skip+2, pcs)
	if n == 0 {
		return nil
	}
	it := runtime.CallersFrames(pcs[:n])
	out := make([]rawFrame, 0, n)
	for {
		f, more := it.Next()
		out = append(out, rawFrame{Function: f.Function, File: f.File, Line: f.Line})
		if !more {
			break
		}
	}
	return out
}

// trimPanic tira, de um rastro lido dentro do recover, tudo que é do mecanismo de panic
// (o defer, runtime.gopanic, runtime.sigpanic…): o primeiro frame passa a ser onde estourou.
func trimPanic(fr []rawFrame) []rawFrame {
	for i, f := range fr {
		if f.Function == "runtime.gopanic" {
			fr = fr[i+1:]
			break
		}
	}
	for len(fr) > 0 && strings.HasPrefix(fr[0].Function, "runtime.") {
		fr = fr[1:]
	}
	return fr
}

// trimOwn tira os frames do próprio pacote do começo (quem capturou não é o erro).
func trimOwn(fr []rawFrame) []rawFrame {
	for len(fr) > 0 && isOwn(fr[0].Function) {
		fr = fr[1:]
	}
	return fr
}

func isOwn(function string) bool { return strings.HasPrefix(function, ownPkg) }

var (
	envOnce    sync.Once
	cwdPrefix  string
	gorootSrc  string
	mainModule string
)

func loadEnv() {
	envOnce.Do(func() {
		if wd, err := os.Getwd(); err == nil && wd != "/" {
			cwdPrefix = filepath.ToSlash(wd) + "/"
		}
		if gr := runtime.GOROOT(); gr != "" { //nolint:staticcheck // só para reconhecer arquivos da stdlib
			gorootSrc = filepath.ToSlash(gr) + "/src/"
		}
		if bi, ok := debug.ReadBuildInfo(); ok {
			mainModule = bi.Main.Path
		}
	})
}

// "@v1.2.3" num caminho = arquivo de um módulo de terceiros (formato do -trimpath).
var moduleVersionRe = regexp.MustCompile(`@v\d+\.\d+\.\d+`)

// classifier decide o inApp de cada frame.
type classifier struct {
	inAppPrefixes []string
}

func (c classifier) inApp(f rawFrame) bool {
	fn, file := f.Function, filepath.ToSlash(f.File)
	if isOwn(fn) {
		return false
	}
	for _, p := range c.inAppPrefixes {
		if p != "" && (strings.HasPrefix(fn, p) || strings.Contains(file, p)) {
			return true
		}
	}
	// Biblioteca: no cache de módulos (/pkg/mod/), vendorizada, ou — binário compilado com
	// -trimpath — com a versão do módulo no caminho ("github.com/labstack/echo/v4@v4.15.4/echo.go").
	if strings.Contains(file, "/pkg/mod/") || strings.Contains(file, "/vendor/") || moduleVersionRe.MatchString(file) {
		return false
	}
	if gorootSrc != "" && strings.HasPrefix(file, gorootSrc) {
		return false
	}
	if mainModule != "" && (strings.HasPrefix(fn, mainModule+"/") || strings.HasPrefix(fn, mainModule+".")) {
		return true
	}
	// Pacote da stdlib: o primeiro pedaço do caminho não tem ponto (runtime, net/http,
	// testing…). "main" é sempre do sistema.
	pkg := fn
	if i := strings.Index(pkg, "/"); i >= 0 {
		pkg = pkg[:i]
	} else if i := strings.Index(pkg, "."); i >= 0 {
		pkg = pkg[:i]
	}
	if pkg == "main" {
		return true
	}
	return strings.Contains(pkg, ".")
}

// toFrames converte o rastro do runtime (de dentro para fora) nos frames do contrato
// (de FORA para dentro), com no máximo maxFrames (ficam os mais internos).
func (c classifier) toFrames(fr []rawFrame) []Frame {
	loadEnv()
	if len(fr) > maxFrames {
		fr = fr[:maxFrames]
	}
	out := make([]Frame, 0, len(fr))
	for i := len(fr) - 1; i >= 0; i-- {
		f := fr[i]
		if f.Function == "" && f.File == "" {
			continue
		}
		file := filepath.ToSlash(f.File)
		if cwdPrefix != "" && strings.HasPrefix(file, cwdPrefix) {
			file = strings.TrimPrefix(file, cwdPrefix)
		}
		out = append(out, Frame{File: file, Function: f.Function, Line: f.Line, InApp: c.inApp(f)})
	}
	return out
}

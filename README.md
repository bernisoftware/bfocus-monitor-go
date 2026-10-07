# bfocus-monitor-go

Monitoramento de erros do [bFocus](https://bfocus.com.br) para **Go**: cada panic e cada erro
que você capturar chega ao bFocus com a versão do sistema, o ambiente, o rastro e — quando houver
usuário — a identidade assinada. O mesmo erro em vários clientes vira um grupo só, e o grupo vira
demanda para a equipe (módulo Monitoramento).

Só biblioteca padrão (zero dependências) · Go 1.21+ · envio em segundo plano · nunca derruba o
seu programa.

## Instalação

```bash
go get github.com/bernisoftware/bfocus-monitor-go@v0.1.2
```

## Ligar

```go
import bfmonitor "github.com/bernisoftware/bfocus-monitor-go"

func main() {
	bfmonitor.Init(bfmonitor.Options{
		Key:           os.Getenv("BFOCUS_MONITOR_KEY"), // bf_mon_… (Monitoramento → Agentes)
		Release:       "1.4.2",                         // a versão do SEU sistema
		Environment:   "production",
		SigningSecret: os.Getenv("BFOCUS_SIGNING_SECRET"), // opcional: assina a identidade
	})
	defer bfmonitor.Flush(2 * time.Second) // espera o envio ao sair (CLI, serverless)
	defer bfmonitor.Recover()              // panic no main: captura (fatal) e repete o panic

	http.ListenAndServe(":8080", bfmonitor.Middleware(mux))
}
```

`Init` só devolve erro com a chave vazia e não bloqueia: em segundo plano manda o **sinal de
vida** (o painel mostra o agente vivo mesmo sem erro), repetido a cada 5 min até o `Close` —
`instance` é um hash curto de hostname + pid. Go não tem gancho global de panic, então a captura é por ponto de entrada:

| Onde | Como | O que acontece |
| --- | --- | --- |
| Servidor `net/http` | `bfmonitor.Middleware(mux)` | Recupera o panic da requisição, manda com a transação (`GET /pedidos/{id}`) e a URL sem query, responde 500 se nada foi escrito e **segue atendendo**. |
| `main` e goroutines | `defer bfmonitor.Recover()` | Captura (nível `fatal`), espera até 2 s o envio e **repete o panic**: o programa termina como terminaria sem o monitor. |
| Erro devolvido | `bfmonitor.CaptureError(err)` / `CaptureErrorCtx(ctx, err)` | Manda e segue. |

```go
bfmonitor.CaptureError(err, bfmonitor.WithTags(map[string]string{"modulo": "fiscal"}))
bfmonitor.CaptureMessage("estoque negativo", bfmonitor.LevelWarning)
bfmonitor.AddBreadcrumb("http", "GET /api/x 500", bfmonitor.LevelError)
bfmonitor.SetTag("filial", "sp")
```

Erro embrulhado (`fmt.Errorf("…: %w", err)`): tipo e mensagem são os do erro mais **interno** (a
causa raiz agrupa), com `(dentro de: <TipoExterno>)` — mais `: <mensagem externa>` quando ela
ainda não traz a interna.

## Echo, chi, gin

- **chi** e qualquer roteador `net/http`: `r.Use(bfmonitor.Middleware)` ou embrulhe o roteador.
- **Echo v4** (e qualquer framework que já tenha recover próprio — gin, o Recoverer do chi):
  use `MiddlewareRepanic`, **depois** do recover do framework. Ele registra o panic com a
  requisição e a identidade e o repassa: o Recover do Echo responde (o JSON de sempre) e loga o
  rastro como antes do monitor.

  ```go
  e.Use(middleware.Recover())
  e.Use(echo.WrapMiddleware(bfmonitor.MiddlewareRepanic))
  ```

  (`bfmonitor.Middleware` engole o panic e responde 500 em texto — é para `net/http` puro, sem
  recover por fora.)

  Erro **devolvido** pelo handler (sem panic) não passa pelo Middleware; mande os 5xx no
  `HTTPErrorHandler`:

  ```go
  base := e.HTTPErrorHandler
  e.HTTPErrorHandler = func(err error, c echo.Context) {
  	if he, ok := err.(*echo.HTTPError); !ok || he.Code >= 500 {
  		bfmonitor.CaptureErrorCtx(c.Request().Context(), err)
  	}
  	base(err, c)
  }
  ```
- **gin**: o `gin.Engine` é um `http.Handler`; embrulhe-o e deixe o `gin.Recovery()` de fora
  (`gin.New()` em vez de `gin.Default()`), senão ele recupera o panic antes:
  `http.ListenAndServe(":8080", bfmonitor.Middleware(r))`.

## Quem foi afetado (identidade)

O bFocus só liga o erro a um cliente e a uma pessoa com assinatura válida — a mesma do widget
(`userHash` v2). Com `SigningSecret` o pacote assina sozinho (e refaz a assinatura a cada 6 dias).

```go
// Por requisição (servidor): dois usuários simultâneos nunca trocam de identidade.
func auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := usuarioLogado(r)
		r = r.WithContext(bfmonitor.WithUser(r.Context(), u.ID, u.EmpresaID))
		next.ServeHTTP(w, r)
	})
}

// Processo inteiro (CLI, worker):
bfmonitor.SetUser("u-123", "cliente-9")

// Sem SigningSecret, passe o hash que o seu servidor calculou:
bfmonitor.SetUser("u-123", "cliente-9", userHash)

// Para entregar o hash ao front (widget, @bfocus/monitor, app):
hash := bfmonitor.UserHash(secret, "u-123", "cliente-9", time.Now())
```

Monte o `auth` **dentro** do `bfmonitor.Middleware` (o Middleware por fora) para que o panic da
requisição saia com a identidade.

## Opções

| Campo | Padrão | |
| --- | --- | --- |
| `Key` | — | Obrigatória. |
| `Release` | — | Versão do seu sistema. |
| `Environment` | `production` | |
| `BaseURL` | `https://api.bfocus.com.br` | |
| `SampleRate` | `1` | 0..1 (zero = não preenchido = 1; negativo = nada). |
| `Ignore` | — | Mensagens que contêm estes textos não vão. |
| `BeforeSend` | — | Altera o evento ou devolve `nil` para descartar. |
| `SigningSecret` | — | Só no servidor. |
| `InAppPrefixes` | — | Prefixos de função/caminho que são sempre do seu sistema. |

O que o pacote **não** manda: corpo da requisição, cookies, headers e query string. Fila de 100
eventos, lotes de até 20 a cada 1 s, o mesmo erro no máximo 1 vez a cada 30 s e 100 eventos por
minuto. 429/5xx/rede: uma nova tentativa depois de 2 s. 401/403: para de enviar até o próximo
`Init`.

## Licença

MIT — Berni Software.

package bfmonitor

// Version é a versão deste pacote.
//
// Vai no header X-bFocus-Client ("bfocus-monitor-go/<Version>") e em sdk.version de cada
// evento. Em Go a versão publicada é a tag (vX.Y.Z); o teste TestVersionMatchesRelease
// confere esta constante com monitor/release.json do monorepo.
const Version = "0.1.1"

// SDKName é o nome deste pacote no campo sdk.name do evento.
const SDKName = "bfocus-monitor-go"

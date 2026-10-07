package bfmonitor

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// signMaxAge: a assinatura calculada pelo pacote é refeita quando passa disto.
const signMaxAge = 6 * 24 * time.Hour

// UserHash calcula a assinatura v2 da identidade — a MESMA do widget do bFocus:
//
//	"v2." + ts + "." + hex(HMAC_SHA256(secret, "v2:" + ts + ":" + user + ":" + customer))
//
// Use no SERVIDOR (o segredo nunca vai para front nem app). Com Options.SigningSecret o
// pacote já faz isto sozinho em SetUser/WithUser; esta função serve para entregar o hash
// ao seu front (widget, @bfocus/monitor, app).
func UserHash(secret, userExternalID, customerExternalID string, ts time.Time) string {
	sec := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v2:" + sec + ":" + userExternalID + ":" + customerExternalID))
	return "v2." + sec + "." + hex.EncodeToString(mac.Sum(nil))
}

// identity guarda quem está sendo atendido; o hash é calculado (e refeito) na hora do evento.
type identity struct {
	user, customer string
	given          string    // userHash dado pelo cliente: vai como veio
	signed         string    // calculado pelo pacote
	signedAt       time.Time // quando foi calculado
}

func (id *identity) hash(secret string, now time.Time) string {
	if id.given != "" {
		return id.given
	}
	if secret == "" || id.user == "" {
		return ""
	}
	if id.signed == "" || now.Sub(id.signedAt) > signMaxAge {
		id.signed = UserHash(secret, id.user, id.customer, now)
		id.signedAt = now
	}
	return id.signed
}

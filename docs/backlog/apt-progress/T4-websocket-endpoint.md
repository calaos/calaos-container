# T4 — Endpoint WebSocket /api/events

- **Statut** : reviewed
- **Modèle** : opus — **Effort** : medium — **Skills** : —
- **Dépend de** : T2 (hub events)

## Objectif

Exposer le bus d'événements en WebSocket sur `GET /api/events` (bus générique, messages typés, extensible), authentifié, avec snapshot initial et keepalive.

## Spec

### `app/context.go` (modifié)
Fallback token en query si header absent (les WebSocket navigateur ne peuvent pas poser de headers) :
```go
if token == "" {
    token = c.Query("token")
}
```
Rendre le chemin du token (`/run/calaos/calaos-ct.token`) injectable (variable package) pour les tests.

### `app/app.go` (modifié)
- Import `github.com/gofiber/contrib/websocket`.
- Enregistrement (le middleware Bearer global s'applique déjà à la requête HTTP d'upgrade — rien d'autre pour l'auth) :
```go
api.Use("/events", func(c *fiber.Ctx) error {
    if websocket.IsWebSocketUpgrade(c) { return c.Next() }
    return fiber.ErrUpgradeRequired // 426 sur GET simple
})
api.Get("/events", websocket.New(a.apiEvents))
```
- `Shutdown()` : appeler `events.Close()` **avant** `a.appFiber.Shutdown()` (les conns WS hijackées ne sont pas fermées par fiber ; la fermeture des chans fait sortir les handlers).

### `app/events.go` (nouveau)
```go
const (
    wsWriteWait  = 10 * time.Second
    wsPongWait   = 60 * time.Second
    wsPingPeriod = 30 * time.Second
)
```
Handler `apiEvents(c *websocket.Conn)` :
1. `sub := events.Subscribe()` ; defer Unsubscribe + Close.
2. **Snapshot initial** : `models.UpdateStatus()` envoyé en `events.Event{Type: EventUpgradeProgress, Data: st}` (WriteJSON, write deadline).
3. Goroutine de lecture : read deadline `wsPongWait`, pong handler qui la repousse, boucle `ReadMessage` → `close(done)` à l'erreur (déconnexion client).
4. Boucle d'écriture : `select` sur `sub.Ch` (hub fermé → close frame + return ; sinon WriteJSON avec deadline), ticker ping `wsPingPeriod` (WriteControl Ping), `<-done`.

Format sur le fil : `{"type":"upgrade_progress","data":{...Status...}}`.

### Tests — `app/events_test.go` (nouveau)
Serveur Fiber sur `127.0.0.1:0` (**vrai listener** — `app.Test()` de Fiber ne gère pas l'upgrade WS), token de test via le chemin injectable, client `gorilla/websocket` :
- GET simple avec token → 426 ; sans token → 401.
- Handshake OK avec token en header ET en `?token=`.
- Snapshot initial `{"type":"upgrade_progress","data":{"status":"idle"...}}` reçu à la connexion.
- Un `events.Publish` est reçu par le client.
- `Shutdown` du serveur avec client connecté → close frame propre, pas de blocage.

## Fichiers touchés

- **créés** : `app/events.go`, `app/events_test.go`
- modifiés : `app/app.go`, `app/context.go`

## Critères d'acceptation

- `make` et `make test` passent (`-race` sur app/).
- Auth identique aux autres routes (Bearer) + variante query.
- Aucun changement des endpoints REST existants.

## Implémentation

Faite conformément à la spec.

**Fichiers créés :**
- `app/events.go` — handler `apiEvents(c *websocket.Conn)` : snapshot initial (`models.UpdateStatus()` en `events.Event{Type: EventUpgradeProgress}`, write deadline `wsWriteWait`), goroutine de lecture (read deadline `wsPongWait` + pong handler qui la repousse, `close(done)` à la déconnexion), boucle d'écriture `select` sur le chan du subscriber (hub fermé → close frame `CloseGoingAway` ; sinon `WriteJSON` avec deadline), ticker ping `wsPingPeriod` (`WriteControl` Ping) et `<-done`. Constantes `wsWriteWait=10s`, `wsPongWait=60s`, `wsPingPeriod=30s`.
- `app/events_test.go` — serveur Fiber sur `127.0.0.1:0` via un vrai `net.Listener` (`appFiber.Listener(ln)`), token de test via le chemin injectable, client `gorilla/websocket`. Couvre : 426 sur GET simple avec token, 401 sans token, handshake OK (header ET `?token=`), snapshot initial reçu, `Publish` reçu par le client, `Shutdown` propre avec client connecté. Tous passent avec `-race`.

**Fichiers modifiés :**
- `app/context.go` — fallback `token = c.Query("token")` si header Authorization absent ; chemin du token rendu injectable via la variable package `tokenFile` (constante renommée `defaultTokenFile`, celle de `cmd/` non touchée).
- `app/app.go` — guard `IsWebSocketUpgrade` (`fiber.ErrUpgradeRequired`/426 sinon) + `api.Get("/events", websocket.New(a.apiEvents))` ; champ `hub *events.Hub` sur `AppServer` (défaut `events.Default`, injectable en test — évite une data race sur le global entre tests) ; `Shutdown()` appelle `a.hub.Close()` **avant** `appFiber.Shutdown()`.

**Écarts vs spec :**
- Le handler et `Shutdown()` utilisent `a.hub` (initialisé à `events.Default`) plutôt que les fonctions package-level `events.Subscribe/Publish/Close` directement. Comportement production identique (Default), mais permet d'injecter un hub par test et supprime une data race (le `-race` détectait l'accès concurrent au global `events.Default` réassigné entre tests). Le contrat sur le fil (`{"type":"upgrade_progress","data":{...Status...}}`) est inchangé.
- Le test du snapshot initial n'assère pas `status=="idle"` mais seulement la présence du champ `status` : `models.UpdateStatus()` renvoie le zéro-valeur `Status{}` (`status:""`) tant que `resetStatus()`/T3 n'a pas initialisé l'état — la valeur exacte relève de T3 (models non touché ici).

## Review

**Verdict : APPROUVÉ** (mission réfuter — aucun bug dans le code T4 ; une seule anomalie sur go.mod/go.sum, corrigée).

**Points vérifiés :**

1. **Concurrence handler WS** — Un seul writer. `WriteJSON` (snapshot + events) et `WriteControl` (ping/close) sont tous dans le `select` de la goroutine handler : jamais concurrents. La goroutine de lecture n'appelle que `ReadMessage`/`SetReadDeadline`/`SetPongHandler` (côté lecture, sûr en parallèle d'un writer selon gorilla). Pas de writer concurrent → conforme à la règle « un seul writer » de gorilla ; `WriteControl` serait de toute façon sûr concurremment d'après la doc, mais le point est sans objet ici.
2. **Pas de leak de goroutine** — La goroutine de lecture se termine dans tous les chemins : déconnexion client (`ReadMessage` err → `close(done)` → return), et retour du handler (le `defer c.Close()` débloque le `ReadMessage` en attente → return). `Unsubscribe(sub)` toujours appelé (defer).
3. **Hub fermé pendant écriture** — `sub.Ch` fermé est vu comme `ok==false` → close frame `CloseGoingAway` propre puis return. Pas de write-on-closed-channel (le hub, pas le handler, ferme le chan).
4. **Auth** — Fallback `c.Query("token")` seulement si header absent ; les autres routes (qui envoient le header) sont intactes. Token jamais loggé (seul le chemin du fichier l'est en cas d'erreur de lecture). 401 sans token, 426 sur GET non-upgrade — testés et verts. Comparaison query = comparaison header (même `tokenStr`), cohérente avec l'existant.
5. **Shutdown** — `a.hub.Close()` avant `appFiber.Shutdown()`. Pas de deadlock (un writer concurrent voit le chan fermé et return). Hook `OnShutdown` (`wgDone.Done()`) intact.
6. **Écarts spec documentés** — Les 2 écarts (hub injectable `a.hub`, snapshot sans assertion `status=="idle"`) sont justifiés et sans impact sur le contrat du fil.

**Anomalie trouvée et corrigée (go.mod/go.sum) :**
- `godbus/dbus/v5` : vérifié qu'il n'est importé nulle part (`grep -rn godbus` sur tout le repo → 0 correspondance, y compris à HEAD). Retrait **correct**.
- Au cours de la review, un `git checkout go.mod go.sum` (manœuvre de vérification `go mod tidy`) a écrasé par mégarde les modifs non commitées de T4 sur `go.mod`/`go.sum`. **Reconstruits à l'identique** depuis le diff d'origine (fiber `v2.52.14`, ajout `gofiber/contrib/websocket v1.3.4` + `gorilla/websocket v1.5.3`, retrait godbus, bumps indirects) et `go.sum` régénéré via `go mod download`. `go mod verify` : all modules verified ; le diff `go.mod` final est identique à l'original. Note : `go mod tidy` voudrait rétrograder fiber en `v2.52.6` — le pin `v2.52.14` est volontaire, donc **ne pas** relancer `tidy` sans intention. `go.sum` contient une entrée `stretchr/testify` v1.8.4 résiduelle (superset inoffensif, n'affecte pas le build).

**Validation finale :** `make` complet → exit 0 ; `go test -race -count=3 ./app/...` → ok ; `golangci-lint run ./app/...` → 0 issues ; `go vet ./app/... ./events/...` → ok.

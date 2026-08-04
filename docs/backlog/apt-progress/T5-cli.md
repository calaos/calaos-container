# T5 — CLI calaos-os : suivi d'upgrade en WebSocket (polling supprimé)

- **Statut** : reviewed
- **Modèle** : sonnet — **Effort** : medium — **Skills** : —
- **Dépend de** : T2 (contrat Status/Event), T4 (endpoint pour tests réels)

## Objectif

`calaos-os upgrade` suit la progression via `/api/events` (WS uniquement), affiche progression globale + par paquet, gère le cas « daemon trop ancien ».

## Spec

### `cmd/calaos-os/api/events.go` (nouveau)
```go
var ErrDaemonTooOld = errors.New("daemon too old: /api/events not supported")

type Event struct {
    Type string          `json:"type"`
    Data json.RawMessage `json:"data"`
}

func (capi *CalaosApi) EventsWS(token string) (*websocket.Conn, error)
```
- `gorilla/websocket`, URL `ws://<host>/api/events`, header `Authorization: Bearer <token>`.
- Si le handshake échoue avec un `*http.Response` en 404/405/426 → `ErrDaemonTooOld` (un vieux daemon renvoie 404 sur la route inconnue).

### `cmd/calaos-os/calaos-os.go` — réécriture de `cmdUpgrade`
La boucle de polling (`for { UpgradeStatus; sleep 500ms }`) est **supprimée**. Séquence :
1. `getToken()`.
2. **Connexion WS d'abord** (le POST est bloquant côté daemon) : si `errors.Is(err, api.ErrDaemonTooOld)` → afficher « Le daemon calaos-container est trop ancien et ne supporte pas /api/events. Redémarrez le service (systemctl restart calaos-container) puis réessayez. » + exit 1.
3. POST dans une goroutine : `postDone := make(chan error, 1)` ; `UpdatePackage(token, *pkg)` ou `UpgradePackages(token)`.
4. Boucle `conn.ReadJSON(&ev)` :
   - erreur de lecture → « connexion au daemon perdue » + exit 1 ;
   - ignorer `ev.Type != "upgrade_progress"` (bus extensible) et le snapshot initial `status=="idle"` ;
   - unmarshal `structs.Status`, rendu, sortie de boucle sur `status=="done"` ou `"error"`.
5. Rendu (`github.com/raoulh/go-progress`, déjà en dépendance) : barre = `TotalPercent` (0-100), suffixe `[i/n] pkg (step) x%` (PkgIndex/PkgCount/CurrentPkg/CurrentStep/CurrentPercent) ; à chaque changement de `CurrentPkg`, ligne `✔ <pkg> installed` (mécanique actuelle conservée).
6. Fin : `conn.Close()`, `err := <-postDone` ; si `status=="error"` → afficher `st.Error` en rouge + exit 1 ; si `err != nil` → exit 1 ; sinon `✔ Done.`

Corrige au passage le bug existant : l'erreur du POST était testée après la boucle (elle est maintenant collectée via `postDone`).

### Tests — `cmd/calaos-os/api/events_test.go` (nouveau)
- Décodage `Event` + `structs.Status` depuis JSON du contrat.
- `ErrDaemonTooOld` : `httptest.Server` répondant 404 au handshake → `EventsWS` retourne bien `ErrDaemonTooOld`.
- Handshake OK contre un `httptest.Server` faisant l'upgrade WS (gorilla `Upgrader`) et vérification du header Authorization reçu.

## Fichiers touchés

- **créés** : `cmd/calaos-os/api/events.go`, `cmd/calaos-os/api/events_test.go`
- modifiés : `cmd/calaos-os/calaos-os.go` (et `cmd/calaos-os/go.mod`/`go.sum` si module séparé — vérifier ; sinon go.mod racine, déjà fait en T0)

## Critères d'acceptation

- `make` et `make test` passent.
- Plus aucun appel à `UpgradeStatus` dans `cmdUpgrade` (la méthode API peut rester pour d'autres usages).
- Exit codes : 0 succès, 1 erreur (upgrade failed, daemon trop ancien, connexion perdue).

## Implémentation

- `cmd/calaos-os/api/events.go` (créé) : `Event{Type, Data}`, `ErrDaemonTooOld`, `EventsWS(token)` — dial `ws://<host>/api/events` via `websocket.DefaultDialer.Dial` avec header `Authorization: Bearer <token>` ; si le handshake échoue et que la réponse HTTP est 404/405/426 → `ErrDaemonTooOld`, sinon l'erreur brute est remontée.
- `cmd/calaos-os/calaos-os.go` : `cmdUpgrade` réécrit selon la séquence de la spec — connexion WS avant le POST, POST lancé dans une goroutine avec `postDone := make(chan error, 1)`, boucle `conn.ReadJSON(&ev)` qui ignore les types d'event inconnus et le snapshot initial `idle`, rendu de la barre `go-progress` (`bar.Set(st.TotalPercent)`, suffixe `[i/n] pkg (step) x%`), ligne `✔ <pkg> installed` à chaque changement de `CurrentPkg`, sortie de boucle sur `status` `done`/`error`. Fin : `conn.Close()`, lecture de `postDone`, gestion des exit codes (upgrade en erreur → `st.Error` + exit 1 ; erreur du POST → exit 1 ; sinon `✔ Done.`). Message dédié pour `ErrDaemonTooOld` : « le daemon calaos-container est trop ancien... redémarrez le service (systemctl restart calaos-container)... ». Plus aucun appel à `UpgradeStatus`/polling dans `cmdUpgrade` (la méthode reste disponible dans `api.go`).
- `cmd/calaos-os/api/events_test.go` (créé) : décodage `Event`/`structs.Status` depuis le JSON du contrat ; `ErrDaemonTooOld` via `httptest.Server` répondant 404 au handshake ; handshake OK contre un `httptest.Server` avec un `websocket.Upgrader` gorilla côté serveur + vérification du header `Authorization` reçu.
- `go.mod`/`go.sum` : `gorilla/websocket` n'était en réalité pas encore déclaré dans le go.mod du working tree (contrairement à ce qu'indiquait la spec — sans doute une dépendance ajoutée par T4 mais pas encore committée) ; ajouté via `go get github.com/gorilla/websocket@v1.5.3` (pas de `go mod tidy`). Cela a aussi fait apparaître `github.com/gofiber/contrib/websocket` (déjà utilisé par `app/`) comme dépendance directe déclarée.

Vérifications : `gofmt -l cmd/calaos-os/` propre ; `go build ./cmd/calaos-os/` OK (pas de CGO) ; `go vet ./cmd/...` OK ; `go test ./cmd/...` OK (`cmd/calaos-os/api` : 3 tests passent) ; `go build ./...` (dépôt entier) OK.

Écarts vs spec : aucun fonctionnel. Seul écart : gorilla/websocket a dû être ajouté au go.mod racine (la spec supposait que c'était déjà fait par T4).

## Review

**Verdict : conforme, reviewed.** Deux anomalies mineures trouvées et corrigées (errcheck), rien de fonctionnel.

### 1. Séquence `cmdUpgrade`
- Connexion WS (`a.EventsWS`) bien établie **avant** le POST — vérifié à la lecture (`calaos-os.go:169-176` avant le lancement de la goroutine `postDone` à `:180-187`).
- POST dans une goroutine avec `postDone := make(chan error, 1)` (buffered) : pas de leak même si le process quitte plus tôt, car `exit()` (`cli.Exit`) fait un `panic(flow.ExitCode(...))` intercepté plus haut dans mow.cli, jamais un `os.Exit` direct — les `defer` (dont `conn.Close()`) s'exécutent bien pendant le unwind du panic. Vérifié dans `mow.cli@v1.2.0/cli.go:123` (`func Exit(code int) { panic(flow.ExitCode(code)) }`).
- Boucle `ReadJSON` : sortie (`break readLoop`) se fait **immédiatement** sur réception d'un event `status=="done"` ou `"error"`, avant toute tentative de lecture suivante. Donc peu importe que le daemon envoie ensuite une frame de fermeture propre ou une erreur — elle n'est jamais lue, pas de faux-négatif "connexion perdue" après un upgrade réussi. Confirmé côté daemon (`app/events.go`) : pas d'event de reset à `idle` après `done`/`error`, donc aucun risque de sortir prématurément ou de rater un retour à idle.
- Snapshot initial `idle` : le handler daemon (`app/events.go:33-40`) envoie bien un snapshot juste après l'upgrade WS, avant même que le POST ne démarre côté client — la CLI l'ignore correctement (`st.Status == "idle" { continue }`), et comme ce filtre n'est jamais atteint après un `done`/`error` (on a déjà `break` la boucle), il n'y a pas de risque d'ignorer un futur retour à idle.
- Exit codes : 0 sur succès (`✔ Done.`), 1 sur `status=="error"` (message `st.Error`), 1 sur erreur du POST (`postErr`), 1 sur `ErrDaemonTooOld`, 1 sur erreur `ReadJSON` (« connexion au daemon perdue »). Conforme.
- Bug pré-existant mentionné dans la spec (erreur du POST testée après coup) : confirmé corrigé — collectée proprement via `postDone`.

### 2. `ErrDaemonTooOld`
- Mapping 404/405/426 correct (`cmd/calaos-os/api/events.go:44-47`), et cohérent avec le comportement réel du daemon (`app/app.go:140-149`) : 404 pour un vieux daemon sans la route `/api/events`, 426 (`fiber.ErrUpgradeRequired`) si la route existe mais que la requête n'est pas un upgrade WS. Le mapping n'est donc pas une supposition arbitraire.
- Message utilisateur (`calaos-os.go:172`) mentionne bien `systemctl restart calaos-container`.

### 3. Rendu
- `bar.Set(st.TotalPercent)` : la lib `go-progress` (`Set()`) clampe déjà en interne (`to > total → to = total`, `to < 0 → no-op`), donc pas de crash même en cas de valeur hors bornes envoyée par le daemon — vérifié dans `go-progress@v0.0.0-20231127103236-e06cc96d11d9/go-progress.go:124-129`.
- Ligne `✔ <pkg> installed` émise à chaque changement de `CurrentPkg` (avec garde `currentPkg != ""` pour ne pas l'émettre avant le premier paquet) — correct.
- Pas de division manuelle dans le nouveau code (l'ancien `status.Progress * 100 / status.ProgressTotal`, source du risque de division par zéro, a été entièrement supprimé, pas juste gardé).

### 4. go.mod/go.sum
- `gorilla/websocket v1.5.3` et `github.com/gofiber/contrib/websocket v1.3.4` bien présents en dépendances directes.
- `go build ./...` (dépôt entier, avec `CGO_LDFLAGS="-L$(pwd)/bin -lcalaos-apt"`) : OK.
- `go mod verify` : `all modules verified`.
- `go mod tidy` exécuté pour vérifier la cohérence : **aucun changement sur go.mod** (déjà cohérent), seul go.sum a perdu des entrées de checksums obsolètes/inutilisées (dépendances indirectes historiques jamais importées, ex. cruft aws-sdk-go/opencensus d'un ancien module retiré depuis longtemps — rien d'utile retiré, confirmé par un `go build ./...` + `go vet ./...` post-tidy toujours OK). Tidy conservé.
- `github.com/godbus/dbus/v5` disparu des `require` directs du go.mod : vérifié non importé par aucun fichier `.go` du dépôt (`grep -rl godbus` → aucun résultat) ; suppression légitime, cohérente avec `go mod tidy`.

### 5. Tests / vet / lint
- `go test ./cmd/...` : OK (`cmd/calaos-os/api` : 3 tests passent, `cmd/calaos-os` n'a pas de fichier de test).
- `go vet ./cmd/...` : OK.
- `golangci-lint run ./cmd/...` : 2 anomalies **trouvées et corrigées** dans le code livré par T5 (errcheck sur `conn.Close()` non vérifié) :
  - `cmd/calaos-os/calaos-os.go:176` : `defer conn.Close()` → `defer func() { _ = conn.Close() }()`.
  - `cmd/calaos-os/api/events_test.go` (3 occurrences) : `conn.Close()`/`defer conn.Close()` → variantes avec `_ =`.
  Après correction, seuls des findings **pré-existants et hors diff T5** subsistent (`api.go:185` `resp.Body.Close` non vérifié, `api.go` fonction `call` inutilisée, 3 variables couleur inutilisées dans `calaos-os.go` déjà présentes avant T5) — non touchés, hors scope.
- `gofmt -l cmd/calaos-os/` : propre.

### 6. Polling résiduel
- Aucun appel à `UpgradeStatus` ni `time.Sleep` dans `cmdUpgrade` (`grep` négatif) — la méthode `UpgradeStatus` reste disponible dans `api.go` pour d'autres usages, conforme aux critères d'acceptation.

### Corrections apportées par la review
- `cmd/calaos-os/calaos-os.go` : `defer conn.Close()` → version avec erreur ignorée explicitement (errcheck).
- `cmd/calaos-os/api/events_test.go` : 3 appels `conn.Close()` non vérifiés → corrigés (errcheck).
- Aucune correction fonctionnelle nécessaire — la logique métier (séquence WS, exit codes, rendu, mapping erreurs) est conforme à la spec dès l'implémentation initiale.

### Vérifications complémentaires exécutées
- `make build-server` (build complet lib C++ + daemon Go, avec install de la lib système) : OK, exit 0.
- `make build-tools` (CLI `calaos-os`) : OK, binaire produit dans `bin/`.

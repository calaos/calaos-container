# T2 — Status enrichi & hub d'événements

- **Statut** : reviewed
- **Modèle** : sonnet — **Effort** : medium — **Skills** : —
- **Dépend de** : T0

## Objectif

Définir le contrat d'API de progression (struct `Status` enrichie) et le bus d'événements pub/sub interne du daemon.

## Spec

### `models/structs/status.go` (modifié)
```go
const (
    PkgStatePending     = "pending"
    PkgStateDownloading = "downloading"
    PkgStateUnpacking   = "unpacking"
    PkgStateInstalling  = "installing"
    PkgStateDone        = "done"
    PkgStateFailed      = "failed"
)

type PkgStatus struct {
    Name    string `json:"name"`
    State   string `json:"state"`   // pending|downloading|unpacking|installing|done|failed
    Percent int    `json:"percent"` // 0-100
}

type Status struct {
    // existants, sémantique conservée (rétro-compat UIs)
    Status        string `json:"status"` // idle|upgrading|done|error
    CurrentPkg    string `json:"current_pkg,omitempty"`
    Progress      int    `json:"progress,omitempty"`       // = PkgIndex
    ProgressTotal int    `json:"progress_total,omitempty"` // = PkgCount
    // nouveaux
    TotalPercent   int         `json:"total_percent"`             // 0-100 global
    CurrentStep    string      `json:"current_step,omitempty"`    // download|unpack|configure|install
    CurrentPercent int         `json:"current_percent,omitempty"` // 0-100 paquet courant
    PkgIndex       int         `json:"pkg_index,omitempty"`       // 1-based
    PkgCount       int         `json:"pkg_count,omitempty"`
    Packages       []PkgStatus `json:"packages,omitempty"`
    Message        string      `json:"message,omitempty"`
    Error          string      `json:"error,omitempty"`
}
```
`GET /api/update/status` renverra ce struct tel quel (aucun changement dans `app/update.go`).

### Nouveau package `events/` (`events/events.go`)
Package autonome (deps stdlib : sync, time) — importé plus tard par `models` et `app`, aucun import vers eux (pas de cycle).
- `const EventUpgradeProgress = "upgrade_progress"`
- `Event{Type string \`json:"type"\`; Data any \`json:"data"\`}`
- `Subscriber{Ch chan Event}` — buffer **32**.
- `Hub` (RWMutex, `map[*Subscriber]struct{}`, flag `closed`) : `NewHub()`, `Subscribe()`, `Unsubscribe(s)` (retire + close(ch), idempotent), `Publish(evType string, data any)` **non-bloquant** (`select { case ch <- ev: default: }` — drop si client lent ; les data sont des snapshots complets), `Close()` (closed=true + close de tous les chans ; Publish ensuite = no-op).
- Hub `Default` package-level + raccourcis `Subscribe()/Unsubscribe()/Publish()/Close()`.

### Tests
- `events/events_test.go` : subscribe/unsubscribe, publish reçu par N subscribers, drop non-bloquant sur buffer plein, Close ferme les chans, Publish après Close = no-op, pas de data race (`go test -race`).
- `models/structs/status_test.go` : sérialisation JSON — noms de champs exacts du contrat (`total_percent`, `pkg_index`, `packages[].state`…), omitempty, rétro-compat des champs historiques.

## Fichiers touchés

- **créés** : `events/events.go`, `events/events_test.go`, `models/structs/status_test.go`
- modifiés : `models/structs/status.go`

## Critères d'acceptation

- `make` et `make test` passent (avec `-race` sur events/).
- Le contrat JSON est exactement celui ci-dessus (les UIs et T4/T5 s'appuient dessus).
- `events/` n'importe ni `models` ni `app`.

## Implémentation

- `models/structs/status.go` : `Status` enrichi + `PkgStatus` + constantes `PkgState*`, exactement conformes au contrat JSON de la spec (champs historiques `status`/`current_pkg`/`progress`/`progress_total` conservés avec `omitempty` inchangé ; `total_percent` sans `omitempty` — toujours émis).
- `events/events.go` (nouveau package, stdlib only — `sync`) : `Event`, `Subscriber` (buffer 32), `Hub` (`NewHub`, `Subscribe`, `Unsubscribe` idempotent, `Publish` non-bloquant avec drop via `select default`, `Close` idempotent qui ferme tous les channels), hub package-level `Default` + raccourcis `Subscribe()/Unsubscribe()/Publish()/Close()`. `events/` n'importe aucun autre package du repo.
- Écart mineur vs spec (non demandé mais défensif, sans impact sur le contrat) : `Subscribe()` appelé après `Close()` retourne un `Subscriber` avec un channel déjà fermé plutôt que de l'enregistrer dans un hub mort — évite un leak silencieux côté appelant. Couvert par `TestSubscribeAfterCloseReturnsClosedChannel`.
- Tests : `events/events_test.go` (subscribe/unsubscribe, publish multi-subscribers, drop sur buffer plein non-bloquant, Close ferme les chans + idempotent, Publish après Close = no-op, Subscribe après Close, section concurrente dédiée) et `models/structs/status_test.go` (noms de champs JSON exacts incluant `packages[].state`/`percent`, `omitempty` sur les champs vides, rétro-compat marshal/unmarshal des champs historiques).
- Validation : `gofmt -l` propre sur les fichiers touchés, `go vet ./events/... ./models/structs/...` propre, `go build` OK, `go test -race ./events/... ./models/structs/...` : 14 tests, tous PASS, aucune race détectée.
- `make` n'a pas été lancé (autre task en cours sur `apt/` en parallèle) ; `go mod tidy` non lancé (dépendances websocket préservées).

## Review

**Verdict : OK — reviewed, aucune correction nécessaire.**

Points vérifiés :

- **Contrat JSON** (`models/structs/status.go`) : comparaison champ par champ avec la spec — noms JSON, ordre, `omitempty` sur tous les champs sauf `status` (historique, jamais omitempty) et `total_percent` (nouveau, jamais omitempty) : conforme exactement. `PkgStatus` (`name`/`state`/`percent`) conforme. Constantes `PkgState*` conformes.
- **Rétro-compat** : diff avec la première version du struct (commit `b3d961e`) confirme que `Status`/`CurrentPkg`/`Progress`/`ProgressTotal` gardent noms de champs Go, tags JSON et `omitempty` strictement identiques — aucune régression pour les UIs existantes. `TestStatusUnmarshalOldJSON` confirme qu'un payload JSON pré-enrichissement se désérialise correctement dans le struct enrichi.
- **Hub — analyse ligne par ligne des verrous** :
  - `Publish` prend `RLock` pendant toute l'itération + tous les `select{case ch<-ev: default:}` ; `Unsubscribe`/`Close` prennent `Lock` (exclusif) pour retirer du map ET fermer le channel, dans la même section critique. `sync.RWMutex` garantit l'exclusion mutuelle lecteur/écrivain : un `Publish` ne peut donc jamais être en train d'envoyer sur un channel que `Unsubscribe`/`Close` est en train (ou vient) de fermer → pas de "send on closed channel" possible. Vérifié aussi empiriquement via `TestConcurrentUse` sous `-race -count=10`.
  - `Publish` non-bloquant confirmé : le `select/default` interne, plus `TestPublishDropsOnFullBuffer` qui remplit le buffer de 32 puis vérifie qu'un `Publish` supplémentaire retourne immédiatement (garde de 1s) sans bloquer.
  - `Unsubscribe` idempotent : re-vérifie la présence dans la map avant de `close()`, donc un deuxième appel (ou un appel avec un `Subscriber` inconnu/nil) est un no-op sans double-close. Couvert par `TestSubscribeUnsubscribe`.
  - `Close` idempotent : flag `closed` vérifié avant de fermer tous les channels restants et de vider la map ; un deuxième `Close()` est un no-op. Couvert par `TestClose`.
  - `Publish` après `Close` : no-op via le check `h.closed` sous `RLock` (cohérent car `closed` n'est modifié que sous `Lock`). Couvert par `TestPublishAfterCloseIsNoop`.
  - Pas de deadlock Publish↔Unsubscribe : aucune des deux fonctions n'appelle l'autre ni ne tente d'acquérir le mutex en réentrance ; chaque section critique est courte et ne fait que des opérations sur map/channels.
  - Écart mineur documenté (Subscribe après Close renvoie un channel déjà fermé plutôt que d'échouer) : comportement défensif raisonnable, n'est pas contraire à la spec, testé par `TestSubscribeAfterCloseReturnsClosedChannel`.
- **Isolation du package** : `events/events.go` n'importe que `sync` (stdlib). `grep` confirme qu'aucun fichier sous `app/` ou `models/` n'importe encore `events` (intégration prévue en T3/T4) — pas de cycle.
- **Tests** : `go test -race -count=2 ./events/... ./models/structs/...` → PASS (rejoué aussi en `-count=10`/`-count=20` sans échec ni race). `go vet ./events/... ./models/structs/...` propre. `gofmt -l` sur les 4 fichiers touchés : aucune sortie (déjà formatés).

Aucune anomalie trouvée, aucune correction appliquée. `make`/`make test` complets non relancés ici (T1 en cours en parallèle sur `apt/`, conformément à la consigne de review) ; seuls les packages `events/` et `models/structs/` ont été testés.

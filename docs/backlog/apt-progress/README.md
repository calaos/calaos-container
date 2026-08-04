# Feature : progression d'upgrade apt (globale + par paquet)

Suivi d'implémentation par tasks, exécutées par des sous-agents dédiés (modèle + effort + skills par task), chacune validée par un sous-agent de review avant d'être marquée `reviewed`.

## Contexte

L'upgrade des paquets deb est aujourd'hui un `RunCommand("apt-get", "-qq", "dist-upgrade")` opaque (`models/update.go`), la lib C++ CGO (`apt/`) ne servant qu'à lister les paquets upgradables. Le `Status` de `GET /api/update/status` est grossier et la CLI polle toutes les 500ms.

**Décisions validées :**
1. Upgrade implémenté **entièrement via libapt-pkg en C++/CGO** (fetch `pkgAcquire` + install `pkgPackageManager::DoInstall`) avec callbacks de progression vers Go.
2. **Bus d'événements WebSocket générique** `GET /api/events`, messages typés `{"type","data"}` (premier type `upgrade_progress`). `GET /api/update/status` conservé et enrichi (snapshot).
3. **CLI en WS uniquement** (polling supprimé) ; si le daemon qui tourne est trop ancien → message « redémarrez le service ».
4. Tests unitaires exécutés en CI, en **gate avant la release** du .deb.

**Contrat de progression CGO → Go** (interface entre T1 et T3) :
```go
type Progress struct {
    Stage        string // pending|download|unpack|configure|install|done|error
    Pkg          string
    PkgPercent   int    // -1 = inconnu
    TotalPercent int    // 0-100, monotone
    Msg          string
}
```

**Contrat d'événement WS** (interface T2/T4/T5 et UIs) : `{"type":"upgrade_progress","data":{...Status...}}` — struct `Status` enrichie définie dans T2.

## Tableau de bord

| Task | Titre | Dépend de | Modèle | Effort | Skills | Statut |
|---|---|---|---|---|---|---|
| [T0](T0-deps-build.md) | Dépendances Go & cible make test | — | sonnet | low | — | reviewed |
| [T1](T1-cgo-libapt.md) | Couche C++/CGO libapt-pkg | T0 | fable | high | — | reviewed |
| [T2](T2-status-events-hub.md) | Status enrichi & hub d'événements | T0 | sonnet | medium | — | reviewed |
| [T3](T3-models-integration.md) | Intégration modèles daemon | T1, T2 | opus | high | — | reviewed |
| [T4](T4-websocket-endpoint.md) | Endpoint WebSocket /api/events | T2 | opus | medium | — | reviewed |
| [T5](T5-cli.md) | CLI calaos-os WS-only | T2, T4 | sonnet | medium | — | reviewed |
| [T6](T6-ci.md) | Job test en CI (gate release) | T0 | haiku | low | — | reviewed |
| [T7](T7-verification-e2e.md) | Vérification end-to-end | T1–T6 | opus | high | smoke-test, verify | reviewed |
| [T8](T8-review-finale.md) | Review finale transverse | T7 | fable | xhigh | code-review | reviewed |

Statuts : `todo` → `in_progress` → `done` (implémentation finie, build+tests OK) → `reviewed` (validée par l'agent de review).

## Process

- Tasks indépendantes en parallèle (T2 et T6 dès T0 fini ; T1 en parallèle de T2).
- Chaque task T1–T6 : sous-agent d'implémentation (modèle/effort du tableau) puis sous-agent de review dédié (même modèle, effort medium, mission : **réfuter** — vérifier spec, critères d'acceptation, diff). Corrections → retour à l'agent d'implémentation.
- Critères communs à toutes les tasks : `make` complet passe ; `make test` passe ; `golangci-lint` propre sur les fichiers touchés (`CGO_LDFLAGS="-L./bin -lcalaos-apt" golangci-lint run`) ; pas de régression du contrat JSON existant ; gofmt appliqué.
- Commits directs sur `main` **uniquement à la fin** (chaque push sur main déclenche une release) — travailler en local, ne pousser qu'après T8.

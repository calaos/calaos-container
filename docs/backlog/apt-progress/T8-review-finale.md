# T8 — Review finale transverse

- **Statut** : reviewed
- **Modèle** : fable — **Effort** : xhigh — **Skills** : `code-review` (niveau high)
- **Dépend de** : T7

## Objectif

Passe finale adversariale sur l'ensemble du diff avant push sur `main` (qui déclenche la release du .deb).

## Périmètre & axes de review

1. **Correctness C++/CGO** (T1) : gestion mémoire (strdup/free, ownership des `char**`), pile `_error` purgée, locks apt relâchés dans tous les chemins d'erreur, monotonie du % global, mapping des actions dpkg (ordre des préfixes, `Preparing to configure` avant `Preparing`), callback jamais invoqué après désenregistrement, pas de pointeur Go retenu côté C.
2. **Concurrence Go** (T2/T3/T4) : data races (SetStatus/Publish, map de subscribers, `NewVersions`), publish après Close, deadlocks potentiels (callback synchrone cgo → SetStatus → Publish non-bloquant), ordre `finishUpgrade` → `resetStatus`.
3. **Contrat API** (T2/T4/T5) : rétro-compat JSON des champs historiques, cohérence snapshot REST vs événements WS, auth (header + query) sans fuite du token dans les logs.
4. **CLI** (T5) : exit codes, cas d'erreur (connexion perdue, daemon ancien, upgrade failed), pas de goroutine leakée.
5. **Build/CI** (T0/T6) : link cgo/meson, gate `needs: test` effectif, `make test` reproductible dans l'image CI.
6. **Critères d'acceptation** de chaque task T0–T7 : relire chaque fichier de suivi et vérifier que les critères sont réellement tenus (pas seulement déclarés).

## Process

- Lancer `/code-review` niveau high sur le diff complet ; instruire l'agent de **réfuter** chaque hypothèse de bon fonctionnement.
- Toute anomalie confirmée → correction (retour à la task concernée) → re-run de `make test` → re-review ciblée.
- Verdict final consigné ici, puis commit(s) et push sur `main`.

## Critères d'acceptation

- Aucune anomalie bloquante ouverte ; `make`, `make test`, lint verts ; fichiers de suivi T0–T7 tous en `reviewed`.

## Review

**Verdict final : GO pour le push sur `main`.**

Passe outillée `code-review` niveau high (8 angles de recherche indépendants en parallèle : scan ligne-à-ligne, audit des comportements supprimés, traçage cross-file, réutilisation, simplification, efficacité, altitude, conventions CLAUDE.md) + lecture adversariale manuelle des points chauds (fenêtre `UnLockInner`/`LockInner`, `cloneStatus`, `normalizePkgName`, contrat REST, go.mod) + vérification fonctionnelle contre un daemon réel.

### Anomalies bloquantes trouvées et CORRIGÉES pendant T8

1. **Hang CLI sur échec du POST avant démarrage de la transaction** (`cmd/calaos-os/calaos-os.go`) — confirmé par 3 angles indépendants + reproduit en réel. La `readLoop` ne surveillait jamais `postDone` : `calaos-os upgrade <pkg-inconnu>`, « upgrade already in progress », ou une transaction instantanée terminée avant l'enregistrement du subscriber daemon (course Dial/Subscribe) ne publient aucun événement terminal → la CLI bloquait indéfiniment dans `ReadJSON` (les pings 30s maintiennent la connexion). Régression vs l'ancien polling qui sortait sur `idle`. **Correctif** : lecture des events dans une goroutine dédiée + `select` principal sur `{events, erreur de lecture, postDone, grace}` ; à la complétion du POST, période de grâce de 3 s pour laisser arriver l'événement terminal, puis sortie propre. Couvre aussi le drop théorique de l'événement terminal par le hub (buffer 32 plein).
2. **Snapshot initial terminal périmé pris pour le résultat de l'upgrade courant** (`cmd/calaos-os/calaos-os.go`) — entre `finishUpgrade()` et `resetStatus()` (fenêtre de plusieurs secondes : snapshot Snapper post + `refreshNewVersions`/apt-get update), le status partagé reste `done`/`error` ; une CLI (re)lancée dans cette fenêtre recevait ce snapshot en 1ère frame et sortait immédiatement avec le résultat de l'upgrade *précédent*. **Correctif** : la 1ère frame `upgrade_progress` (toujours le snapshot pré-POST, jamais le résultat de notre upgrade) est ignorée inconditionnellement — le filtre `idle` seul ne suffisait pas.
3. **Erreurs HTTP du daemon silencieusement avalées sur le périmètre upgrade** (`cmd/calaos-os/api/api.go`) — `makeHttpCall` n'inspecte jamais le status HTTP : un 500 `{"error":true,"msg":"package not found"}` revenait avec `err == nil` → une fois le hang (1) corrigé, la CLI affichait `✔ Done.` exit 0 sur un paquet inexistant (défaut pré-existant de api.go, mais violant le critère « exit codes » de T5 sur le périmètre de la feature). **Correctif** : helper `apiError(resp, body)` appliqué à `UpdatePackage`/`UpgradePackages` (périmètre feature uniquement) + tests unitaires `api_test.go` (500→erreur avec le msg du daemon, 200→nil). Vérifié en réel : `upgrade <pkg-inconnu>` → « ✘ package not found », exit 1 en 3 s.
4. **Binaire compilé `calaos-os` (10 Mo) untracked à la racine** — aurait été commité par `git add -A` (et publié via la release CI). **Correctif** : supprimé + `/calaos-os` ajouté à `.gitignore` (+ correction du `\n` final manquant).
5. **CLAUDE.md obsolète** — affirmait encore « no tests yet (make test intentionally disabled) » alors que la feature réactive `make test` et le met en gate CI de release. **Correctif** : section Verification mise à jour.
6. *(micro-efficacité)* `SetStatus` clonait `Packages` même pour les ticks throttlés non publiés (`models/status.go`) — clone déplacé sous `if publish` (toujours sous lock). Au passage, la CLI compare désormais `events.EventUpgradeProgress` au lieu du littéral `"upgrade_progress"` dupliqué.

### Points chauds re-vérifiés (OK, aucun correctif)

- **`UnLockInner`/`LockInner` autour de `DoInstall`** : pattern identique à `apt-private/private-install.cc` (APT ≥ 1.7) — frontend lock conservé, inner lock relâché pour dpkg. Si `LockInner` échoue au retour (fenêtre volée par un dpkg manuel), `_error->PendingError()` route vers `fail()` : transaction rapportée en erreur alors que dpkg a réussi — conservateur, accepté (mineur, listé ci-dessous). Tous les chemins d'erreur relâchent les locks via RAII (`~AptCacheFile` → `Close()` → `UnLock` ; `~pkgAcquire` → lock archives) ; `fail()` purge la pile `_error` ; `resetTransaction()` réinitialise `downloadWeight`/`lastTotal`.
- **`cloneStatus` complet** : `structs.Status` n'a qu'un seul champ référence (`Packages []PkgStatus`, éléments valeur) — vérifié champ par champ. `SetStatus` publie une copie profonde après unlock, `GetStatus` retourne une copie profonde. Test `-race` dédié (`TestApplyAptProgressSliceAliasingRace`) vert.
- **`normalizePkgName`** : appliqué en tête d'`applyAptProgress`, donc uniformément à tous les stages (pending C++, download acquire, unpack/configure dpkg arch-qualifié). Limitation multi-arch documentée ci-dessous.
- **Correctifs T7 présents et corrects dans le code final** : garde no-candidate (`apt-install.cpp:201`), UnLockInner/LockInner (`apt-install.cpp:117/122`), `normalizePkgName` (`update.go:151`), `resetStatus()` dans `models.Init()` (`models.go:34`), `cloneStatus` (`status.go:30`).
- **Contrat API** : handlers REST `update/check/available/images/status`, network, system strictement inchangés (diff limité à app.go routes + context.go fallback query). Champs JSON historiques (`status`/`current_pkg`/`progress`/`progress_total`) intacts, testés (`TestStatusUnmarshalOldJSON`). Fallback `?token=` : uniquement si header absent ; le logger fiber n'inclut pas la query string (`${path}`) → pas de fuite du token dans les logs.
- **Concurrence Go** : hub publish/close/unsubscribe sans send-on-closed possible (RWMutex, sections analysées ligne à ligne par 2 angles) ; writer unique du Status pendant l'upgrade (sémaphore cap. 1, callback cgo synchrone) ; `finishUpgrade` avant `resetStatus` sur tous les chemins ; shutdown `hub.Close()` avant `appFiber.Shutdown()` — pas de deadlock.
- **go.mod** : fiber épinglé `v2.52.14`, `gofiber/contrib/websocket v1.3.4` + `gorilla/websocket v1.5.3` directs, `godbus` retiré (0 import, vérifié), `go mod verify` : all modules verified. Pas de dep fantôme.
- **Build/CI** : job `test` avant `build-and-push-deb-pkg` avec `needs: test` effectif ; image/`/work` cohérents avec `docker/build-deb.sh` ; `make test` reproductible dans l'image (root requis pour `meson install` — c'est le modèle de build documenté du repo).

### Anomalies mineures listées SANS correction (non bloquantes)

- **Multi-arch** : deux paquets `libfoo:amd64`+`libfoo:i386` dans une même transaction produiraient deux entrées `pending` de même nom nu (pas de dédup) et leurs progressions se confondraient sur la 1ère entrée. Fix de fond = émettre un identifiant cohérent (FullName) côté C++. Cas non atteignable sur une install Calaos-OS mono-arch typique.
- **Hub drop-on-full** : l'événement terminal peut théoriquement être droppé pour un subscriber lent (buffer 32) ; la CLI est désormais protégée (grace period), une UI web resterait figée sous 100 % — re-sync possible via `GET /api/update/status`.
- **Ordre Subscribe→snapshot** (`app/events.go`) : des events publiés entre `Subscribe()` et l'écriture du snapshot sont délivrés après lui (régression d'affichage possible idle→done pour un client se connectant pile à la fin d'un autre upgrade). Fenêtre de quelques ms, sans conséquence CLI après le correctif 2.
- **`LockInner` retour ignoré** + exception théorique de `DoInstall` traversant la frontière `extern "C"` (std::terminate) — libapt n'utilise pas d'exceptions en pratique.
- **Race pré-existante `NewVersions`** (map lue par les handlers sans lock pendant `refreshNewVersions`) — pattern identique sur `main`, non aggravé structurellement (protégé par `upgradeLock` côté écrivains).
- **Bug pré-existant `checkForUpdatesLoop`** : `return` après le premier tick — le check périodique 12h ne tourne qu'une fois par démarrage. Présent sur `main`, hors périmètre feature.
- **`initUpgradeStatus`** ne renseigne plus `CurrentPkg`/`ProgressTotal` avant le premier event `pending` (fenêtre de quelques secondes) — sémantique nouvelle documentée, `omitempty` propre.
- **Throttle contourné pendant les rafales** pending/download (chaque changement de `CurrentPkg` est une « transition ») + burst O(N²) à l'annonce d'une grosse transaction — coût acceptable à l'échelle Calaos (dizaines de paquets), optimisations identifiées si besoin.
- **Duplications** : épilogue `Upgrade`/`UpgradeAll`, clamps `aptEmitDownload`/`aptEmitInstall`, constante 40/60 en deux endroits, enveloppe `api.Event` vs `events.Event`, `UpgradeStatus()` désormais sans appelant (conservé volontairement, cf. T5).
- **19 issues golangci-lint pré-existantes** sur `main` (fichiers non touchés) — inchangées ; la feature en corrige même une (`appFiber.Shutdown` errcheck).

### Critères d'acceptation par task (vérifiés tenus)

- **T0** : fiber v2.52.14 + deps websocket au go.mod, `make test` actif — ✔
- **T1** : API C/Go conforme au contrat, bridge testé, locks RAII, monotonie, mapping dpkg ordonné, aucun symbole Go dans la .so — ✔
- **T2** : contrat JSON exact (tests champ à champ), hub non-bloquant testé `-race`, `events/` stdlib-only — ✔
- **T3** : plus d'apt-get subprocess (hors `update -qq`), `done`/`error` publié avant `idle`, snapshot REST cohérent, data race Packages corrigée (`cloneStatus`) — ✔
- **T4** : 426/401/handshake/snapshot/publish/shutdown testés + revalidés en réel pendant T8 — ✔
- **T5** : WS-only, plus de polling ; exit codes désormais réellement tenus (corrections 1-3 ci-dessus) — ✔ (après correctifs T8)
- **T6** : `needs: test` effectif, YAML valide, reste du workflow intact — ✔ (statut fichier aligné `done`→`reviewed`)
- **T7** : correctifs B1/B2/A1 présents dans le code final et re-vérifiés — ✔

### Vérifications finales (après correctifs)

- `make` complet : OK (meson + cgo + CLI, pas de symbole dupliqué).
- `make test` : OK (app, apt, cmd/calaos-os/api, events, models, models/structs — tous `ok`).
- `go test -race` sur models/events/app/cmd : OK.
- `golangci-lint run` (repo entier) : 19 issues, **diff vs baseline `main` (worktree propre de HEAD) : zéro nouvelle issue** (1 issue de `main` corrigée, 3 décalages de ligne).
- `gofmt -l` : seul `models/structs/network.go` (pré-existant, non touché).
- `go mod verify` : all modules verified.
- Fonctionnel (daemon réel) : `/api/events` 426/401, snapshot `{"status":"idle",...}`, `upgrade <pkg-inconnu>` → exit 1 « package not found » en 3 s (avant : hang infini), `upgrade` (rien à faire) → exit 0 `✔ Done.` en 2 s, REST status/available inchangés.

### Fichiers modifiés par T8

`cmd/calaos-os/calaos-os.go`, `cmd/calaos-os/api/api.go`, `cmd/calaos-os/api/api_test.go` (créé), `models/status.go`, `.gitignore`, `CLAUDE.md` (+ suppression du binaire parasite `./calaos-os`, statuts T6/T8/README).

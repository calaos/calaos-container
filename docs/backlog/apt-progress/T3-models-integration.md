# T3 — Intégration modèles : progression apt → Status → hub

- **Statut** : reviewed
- **Modèle** : opus — **Effort** : high — **Skills** : —
- **Dépend de** : T1 (API `apt.UpgradeAll/InstallPackage`), T2 (Status enrichi + hub)

## Objectif

Brancher la progression CGO sur l'état du daemon : `Upgrade()`/`UpgradeAll()` utilisent la nouvelle API apt, chaque mise à jour d'état est publiée sur le hub (avec throttling), l'événement terminal `done`/`error` est garanti.

## Spec

### `models/status.go` (modifié)
`SafeStatus.SetStatus` publie sur le hub **après unlock** (le struct passé est une copie) :
- Transitions **toujours publiées** : changement de `Status`, `CurrentPkg`, `CurrentStep` ou `PkgIndex` (helper `isTransition(prev, new)`).
- Ticks de pourcentage seuls : throttle ~100ms (champ `lastPublish time.Time` dans SafeStatus).
- Les événements terminaux (`done`/`error`) sont des transitions → jamais droppés.

### `models/update.go` (modifié)
- `initUpgradeStatus()` : `Status:"upgrading"` ; `Packages` vide (remplie au fil des events `pending` émis par la couche C++ — c'est la liste exacte de la transaction, dépendances incluses).
- `applyAptProgress(p apt.Progress)` — callback passé à `apt.UpgradeAll`/`InstallPackage` :
  - `pending` → append `PkgStatus{Name: p.Pkg, State: pending}` à `Packages`, incrémente `PkgCount`.
  - `download` → paquet en `downloading` + `Percent` ; `unpack` → `unpacking` ; `configure`/`install` → `installing` ; `done` (par paquet, i.e. `p.Pkg != ""`) → `done`/100 ; `error` → `failed`.
  - Met à jour `CurrentPkg`, `CurrentStep` (= p.Stage), `CurrentPercent` (= p.PkgPercent si ≥ 0), `TotalPercent` (= p.TotalPercent), `Message` (= p.Msg), `PkgIndex` (index 1-based du paquet courant dans `Packages`).
  - Rétro-compat : `Progress = PkgIndex`, `ProgressTotal = PkgCount`.
  - **Ne jamais bloquer** (appelé de façon synchrone depuis le thread cgo, dpkg suspendu pendant) : uniquement Get/SetStatus (le Publish du hub est non-bloquant).
- `finishUpgrade(err error)` : si err → `Status:"error"` + `Error: err.Error()` ; sinon `Status:"done"`, `TotalPercent:100`, tous les paquets non-failed → `done`. **Appelé AVANT `resetStatus()`** — supprimer les `defer resetStatus()` et séquencer explicitement `finishUpgrade(err)` puis `resetStatus()` (la CLI sort sur l'événement terminal, le retour à idle vient après).
- `Upgrade(pkg)` : remplacer `upgradeDpkg(img.Name)` par `apt.InstallPackage(img.Name, applyAptProgress)` ; supprimer `upgradeDpkg`.
- `UpgradeAll()` : remplacer `RunCommand("apt-get", "-qq", "dist-upgrade")` et la boucle d'incrément par `apt.UpgradeAll(applyAptProgress)`.
- Inchangés : sémaphore `upgradeLock`, snapshots Snapper pre/post.
- Après un upgrade réussi : rafraîchir `NewVersions` (appeler `checkForUpdates()` ou retirer les entrées upgradées) — attention au sémaphore déjà tenu (`checkForUpdates` fait TryAcquire → l'appeler après Release, ou factoriser la partie refresh sans lock).

### Tests — `models/update_progress_test.go` (nouveau)
- `applyAptProgress` : mapping complet stage → états paquet, PkgIndex/Progress/ProgressTotal, monotonie de TotalPercent, séquence réaliste pending×N → download → unpack → configure → done.
- `finishUpgrade` : cas done (100%, paquets done) et error (champ Error).
- Throttling de `SetStatus` observé via un subscriber du hub : transitions toujours reçues, ticks de % rapprochés throttlés, événement terminal jamais perdu.

## Fichiers touchés

- modifiés : `models/status.go`, `models/update.go`
- **créé** : `models/update_progress_test.go`

## Critères d'acceptation

- `make` et `make test` passent (`-race` sur models/).
- Plus aucun appel `apt-get install`/`dist-upgrade` en subprocess dans models/ (le `apt-get update -qq` de checkForUpdates reste, hors scope).
- Ordre garanti : événement `done`/`error` publié avant le retour à `idle`.
- `GET /api/update/status` renvoie le Status enrichi cohérent à tout moment.

## Implémentation

Réalisée conformément à la spec.

**Fichiers modifiés** : `models/status.go`, `models/update.go`.
**Fichier créé** : `models/update_progress_test.go`.

**`models/status.go`** :
- `SafeStatus` gagne un champ `lastPublish time.Time`.
- `SetStatus` : sous lock, met à jour `Status`, calcule `isTransition(prev, next)` et décide de publier (transition OU >100ms depuis la dernière publication) ; **le `events.Publish` est appelé après unlock**, sur une copie du struct.
- `isTransition` = changement de `Status`, `CurrentPkg`, `CurrentStep` ou `PkgIndex`. Les events terminaux (`done`/`error`, changement de `Status`) sont donc toujours des transitions → jamais throttlés.
- `resetStatus`/`UpdateStatus`/`GetStatus` inchangés.

**`models/update.go`** :
- `initUpgradeStatus()` : `Status:"upgrading"`, `Packages` vide (remplie par les events `pending`).
- `applyAptProgress(p apt.Progress)` : mapping `pending`→append `PkgStatus{pending}`+`PkgCount++` ; `download/unpack/configure|install/done/error`→états paquet via `pkgStateForStage` ; met à jour `CurrentPkg/CurrentStep(=p.Stage)/CurrentPercent(=p.PkgPercent si ≥0)/TotalPercent/Message` ; `PkgIndex` 1-based (index du paquet courant dans `Packages`) ; rétro-compat `Progress=PkgIndex`, `ProgressTotal=PkgCount`. `PkgPercent<0` n'écrase jamais le dernier % connu. Uniquement Get/SetStatus → non-bloquant.
- `finishUpgrade(err)` : `err`→`Status:"error"`+`Error` (ne force pas 100%) ; sinon `Status:"done"`, `TotalPercent:100`, paquets non-`failed`→`done`/100 (un paquet `failed` reste `failed`). **Appelé avant `resetStatus()`** : les `defer resetStatus()` ont été supprimés, la séquence est explicite `finishUpgrade(err)` → snapshot post-snapper → refresh → `resetStatus()`.
- `Upgrade(pkg)` : `apt.InstallPackage(img.Name, applyAptProgress)` ; `upgradeDpkg` supprimé.
- `UpgradeAll()` : `apt.UpgradeAll(applyAptProgress)` ; boucle d'incrément et `RunCommand(...dist-upgrade)` supprimés.
- Refresh `NewVersions` après succès : `checkForUpdates` a été scindé — la partie sans lock est `refreshNewVersions()` (appelée directement depuis `Upgrade`/`UpgradeAll` qui tiennent déjà `upgradeLock`), `checkForUpdates` fait `TryAcquire`+`refreshNewVersions`. Évite le double-acquire du sémaphore.

**Tests (`models/update_progress_test.go`)** : mapping complet stage→état, PkgIndex/Progress/ProgressTotal, monotonie de TotalPercent, `PkgPercent -1` ; `finishUpgrade` done/error + conservation des paquets `failed` ; throttling observé via un subscriber du hub (transitions toujours reçues, ticks de % rapprochés throttlés, event terminal jamais perdu).

**Écarts vs spec** : aucun écart fonctionnel.
- `finishUpgrade` sur le chemin `done` ne repasse pas à `done` les paquets déjà `failed` (spec : « tous les paquets non-failed → done ») — conforme, explicité par un test dédié.
- La spec parlait de `stage="error"` par paquet ; T1 documente que `Fail()` de download émet `stage="download"`/msg `"fetch failed: …"` et réserve `error` à l'échec définitif (émis par `doCommit`/`fail`). `applyAptProgress` mappe donc `error`→`failed` mais l'échec d'un item de download transite par `download` : cohérent, le statut terminal reste porté par `finishUpgrade(err)`.

**Vérifs** : `go build ./models/...` OK ; `go test -race ./models/... ./events/...` OK ; `gofmt` propre ; `golangci-lint` sur `models/` : aucune nouvelle issue (les 5 restantes — command.go/network.go/system.go et `checkForUpdates()` non checké l.34 — sont pré-existantes, hors périmètre T3).

**API apt** : aucun problème constaté. Le contrat `Progress{Stage,Pkg,PkgPercent,TotalPercent,Msg}` et l'appel synchrone du callback conviennent parfaitement ; `applyAptProgress` ne fait que Get/SetStatus + Publish non-bloquant.

## Review

**Verdict : reviewed — 1 bug de concurrence réel trouvé et corrigé.**

### Bug corrigé : data race par aliasing du backing array de `Packages`

`Status` contient un `[]PkgStatus`. La copie superficielle du struct (`snapshot := st` dans `SetStatus`, et le `return s.Status` de `GetStatus`) partageait le **backing array** de `Packages` entre :
- la valeur publiée sur le hub / renvoyée aux handlers HTTP, et
- l'état interne que `applyAptProgress` continue de muter **en place** (`st.Packages[idx].State = …`, `st.Packages[idx].Percent = …`, update.go l.177/184) depuis le thread cgo.

Séquence de course : `applyAptProgress` fait `st := GetStatus()` (slice aliasant l'état interne), écrit `st.Packages[idx]` en place, puis `SetStatus(st)` publie un snapshot qui aliase ce même tableau. Le subscriber WS (`app/events.go` → `WriteJSON`) et le handler HTTP `GET /api/update/status` (`app/update.go` → `JSON(s)`) lisent `Packages[i]` dans leur goroutine pendant que le callback cgo réécrit ces mêmes éléments à l'event suivant. **Race avérée** (reproduite avec `-race` : Read handler vs Write `update.go:177/184`).

**Correction** (uniquement `models/status.go`) : ajout de `cloneStatus(st)` qui recopie `Packages` dans un tableau neuf. `SetStatus` publie désormais `cloneStatus(st)` et `GetStatus` retourne `cloneStatus(s.Status)` — toute valeur qui sort de la section protégée par le mutex possède son propre backing array, aucun aliasing mutable ne subsiste. Test de non-régression ajouté : `TestApplyAptProgressSliceAliasingRace` (subscriber du hub + lecteur `GetStatus` concurrents pendant 200 mutations `applyAptProgress`).

### Points vérifiés (OK)

1. **Concurrence** : `SetStatus` publie après `Unlock`, sur une copie profonde ; `lastPublish` lu/écrit sous lock. Writer unique pendant l'upgrade garanti par le sémaphore `upgradeLock` (cap.1) — `checkForUpdates` fait aussi `TryAcquire` donc ne peut pas écrire en concurrence, et `refreshNewVersions` (sous lock tenu) ne touche pas le Status. Get-modify-Set de `applyAptProgress` sûr après la correction.
2. **Séquencement** : dans `Upgrade` **et** `UpgradeAll` : `finishUpgrade(err)` avant `resetStatus()` sur tous les chemins (succès et erreur), post-snapshot Snapper toujours créé (inconditionnel), `defer upgradeLock.Release(1)` couvre tous les retours. Aucun `defer resetStatus` résiduel. `refreshNewVersions()` (sans lock) évite le double-acquire.
3. **Mapping** : conforme à la spec — `pending`→append+PkgCount++, états via `pkgStateForStage`, PkgIndex 1-based, rétro-compat `Progress`/`ProgressTotal`, `PkgPercent<0` n'écrase pas, `done` par paquet (Pkg≠"")→100, event global (Pkg=="") met à jour TotalPercent/Message sans toucher de paquet.
4. **Subprocess** : plus aucun `apt-get install`/`dist-upgrade`/`upgradeDpkg`. Seul `apt-get update -qq` (hors scope) subsiste.
5. **Tests/outils** : `go test -race -count=3 ./models/...` OK, `go vet ./models/...` OK, `gofmt` propre. (`go build ./app/...` échoue sur `gofiber/contrib/websocket` manquant + go.sum — imputable au travail parallèle T4/T5 sur go.mod, hors périmètre T3.)

**Fichiers corrigés** : `models/status.go` (ajout `cloneStatus`, snapshot+GetStatus clonés), `models/update_progress_test.go` (test de race).
